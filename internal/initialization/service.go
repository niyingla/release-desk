package initialization

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"release-desk/internal/storage/workspace"
)

const requestTimeout = 30 * time.Second
const responseLimit = 64 * 1024

// Service 只响应用户明确发送的单次初始化请求，Token 和响应不进入持久化记录。
type Service struct {
	Store  *workspace.Store
	mu     sync.Mutex
	active map[string]bool
}

type Request struct {
	Digest      string `json:"digest"`
	BaseURL     string `json:"base_url"`
	Version     string `json:"version"`
	ID          string `json:"id"`
	Environment string `json:"environment"`
	Token       string `json:"token"`
	Params      string `json:"params"`
	Body        string `json:"body"`
}

type Result struct {
	Status    int    `json:"status"`
	Body      string `json:"body"`
	Truncated bool   `json:"truncated"`
	ElapsedMS int64  `json:"elapsed_ms"`
}

// Send 发送本次确认的请求，避免同一事项重复点击；不自动重试或标记业务完成。
// 参数 body：版本、事项、环境、本次参数及临时 Token；Token 不得写入 Store。
// 返回：有界响应供本次弹窗查看；准备或网络失败返回不含凭据的错误。
func (s *Service) Send(body []byte) (any, error) {
	// 请求绑定稳定 ID，不能借此从其他类型事项触发任意业务调用。
	r, err := workspace.Decode[Request](body)
	// 格式错误不返回解析原文，避免令牌出现在错误提示或上层日志中。
	if err != nil {
		return nil, errors.New("初始化请求格式无效")
	}
	// 只在短临界区定位事项；HTTP 响应期间不持有数据文件锁。
	s.Store.Mu.Lock()
	_, item, _, findErr := s.Store.Find(r.Version, r.ID)
	// 固定路由与参数分类，运行中不会追随下一轮扫描覆盖后的事项内容。
	item = workspace.Clone(item)
	closing := s.Store.Closing
	s.Store.Mu.Unlock()
	// 普通验证、SQL 或已删除事项都没有调用业务接口的权限。
	if closing || findErr != nil || item.Kind != "init_api" {
		return nil, errors.New("初始化事项不存在或软件正在退出")
	}
	// 真实路由、方法和模板必须与用户看到的快照相同，避免扫描期间换了调用目标。
	if r.Digest == "" || r.BaseURL == "" || r.Digest != workspace.VerificationHash(item) {
		return nil, errors.New("接口事项已变更，请重新打开手动调用并核对参数")
	}
	// 同项请求占用只存在内存中，网络等待期间不锁住界面与其他业务。
	key := r.Version + "|" + r.ID
	s.mu.Lock()
	if s.active == nil {
		// 首次使用时建立占用表，Token 和请求正文不进入此表。
		s.active = map[string]bool{}
	}
	// 同项连点不能产生第二个写请求，等待结果后才能由用户重新发起。
	if s.active[key] {
		s.mu.Unlock()
		return nil, errors.New("此接口正在调用，请等待本次响应")
	}
	s.active[key] = true
	s.mu.Unlock()
	// 准备失败、网络错误和正常响应都释放本项占用，不沿用这次凭据。
	defer func() { s.mu.Lock(); delete(s.active, key); s.mu.Unlock() }()
	// 应用退出取消请求；超时只说明响应未确认，绝不自动重发可能已执行的初始化。
	ctx, cancel := context.WithTimeout(s.Store.TaskContext, requestTimeout)
	defer cancel()
	// 请求只接受本地配置的环境入口，临时 Token 仅放入即将发送的 Header。
	req, secret, err := buildRequest(ctx, item, r, s.Store.Config())
	if err != nil {
		return nil, err
	}
	// 单次调用不跟随重定向，防止写请求或 Authorization 被转送到另一入口。
	// 使用新连接，避免旧长连接失效时传输层自动重放 GET 型初始化接口。
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableKeepAlives = true
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	started := time.Now()
	// 入口日志用于区分用户实际发起过请求与仅编辑了接口记录。
	slog.Info("初始化接口请求开始", "version", r.Version, "item_id", r.ID, "environment", r.Environment)
	response, err := client.Do(req)
	// 网络错误不输出底层 URL 或请求对象，也不把未收到响应解释为未执行。
	if err != nil {
		slog.Warn("初始化接口未取得响应", "version", r.Version, "item_id", r.ID, "duration_ms", time.Since(started).Milliseconds())
		return nil, errors.New("请求未取得完整响应；可能已被服务端执行，请核对结果后再决定是否重发")
	}
	// 所有后续分支都会关闭响应流，不保留可跨请求读取的认证上下文。
	defer response.Body.Close()
	// 响应仅在本次弹窗返回，限制大小并隐藏服务端可能回显的本次 Token。
	// 多读取一个 Token 长度，先脱敏再截断，避免边界处留下半段凭据。
	data, err := io.ReadAll(io.LimitReader(response.Body, int64(responseLimit+len(secret)+1)))
	if err != nil {
		// 已收到部分响应也可能已完成业务，不能由软件自行再次发送。
		return nil, errors.New("响应读取中断，请核对服务端结果，未自动重发")
	}
	// 原始字节是否超过上限单独记录，脱敏后的文本长度可能更短。
	truncated := len(data) > responseLimit
	text := strings.ReplaceAll(string(data), secret, "[已隐藏 Token]")
	// 最终展示量有界，避免大响应长时间占用桌面渲染线程。
	if len(text) > responseLimit {
		text = text[:responseLimit]
	}
	result := Result{Status: response.StatusCode, Body: text, Truncated: truncated, ElapsedMS: time.Since(started).Milliseconds()}
	// 日志不记录请求 URL、参数、Token 或响应正文；HTTP 状态不能代替业务验收。
	slog.Info("初始化接口请求完成", "version", r.Version, "item_id", r.ID, "environment", r.Environment, "http_status", result.Status, "duration_ms", result.ElapsedMS)
	return result, nil
}
