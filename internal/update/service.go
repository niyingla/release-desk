// Package updateservice 只读取公开 GitHub 发布信息，不上传业务资料或自动替换运行中的程序。
package updateservice

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"release-desk/internal/platform/desktop"
	"release-desk/internal/policy"
	"release-desk/internal/storage/workspace"
	"release-desk/internal/versioninfo"
)

//go:embed source.json
var source []byte

// Status 是界面可读的独立更新状态，不携带用户目录、业务版本或登录信息。
type Status struct {
	Checking    bool   `json:"checking"`
	Available   bool   `json:"available"`
	Current     string `json:"current"`
	Latest      string `json:"latest"`
	Note        string `json:"note"`
	CheckedAt   string `json:"checked_at"`
	ReleaseURL  string `json:"release_url"`
	DownloadURL string `json:"download_url"`
	Error       string `json:"error"`
}

// Service 使用独立互斥和 HTTP 客户端，更新网络异常不占用 SQL 或 AI 工作区。
type Service struct {
	mu         sync.Mutex
	ctx        context.Context
	client     *http.Client
	repository string
	current    string
	status     Status
}

// New 读取内嵌发布源和软件版本，不在创建组件时联网。
// 参数 ctx：软件退出时取消的上下文；最小化到托盘不会取消。
// 返回：独立更新服务；内嵌来源异常保留为可见错误，不阻断离线工作台。
func New(ctx context.Context) *Service {
	// GitHub 来源集中在资源文件，复制或迁移项目不需要修改多个业务方法。
	var config struct {
		Repository string `json:"repository"`
	}
	info, err := versioninfo.Read()
	// 客户端超时属于软件更新策略，不复用数据库连接或生产环境设置。
	s := &Service{ctx: ctx, current: info.Current, client: &http.Client{Timeout: policy.UpdateTimeoutSeconds * time.Second}}
	if json.Unmarshal(source, &config) != nil || err != nil {
		s.status.Error = "更新来源或软件版本不可用"
	}
	s.repository = config.Repository
	// 仓库名由请求层再验证，资源文件异常不能导向任意更新服务器。
	// 首次尚未查询不能显示成“已经是最新版”。
	s.status.Current = s.current
	return s
}

// Snapshot 返回可供轮询编码的状态副本。
// 无业务入参；不触发网络请求或读取用户业务文件。
// 返回：某一时刻的完整更新状态，不能被外部修改服务内存。
func (s *Service) Snapshot() Status {
	// 与后台请求只共享短暂状态锁，不能将 HTTP 慢查询放在这里。
	s.mu.Lock()
	defer s.mu.Unlock()
	// Status 只包含标量字符串和布尔值，按值返回即可隔离后续状态变化。
	return s.status
}

// Run 在启动和固定间隔检查公开发布，不使用 Codex 额度。
// 无业务入参；使用本实例的退出上下文和集中轮询间隔。
// 不返回业务值；结果由 Snapshot 获取，取消后停止后续网络请求。
func (s *Service) Run() {
	// 一次启动只建立一个周期；扫描、数据库和用户记录不参与更新协议。
	ticker := time.NewTicker(policy.UpdateIntervalHours * time.Hour)
	// 周期只做轻量发布查询，不建立持续下载连接或常驻模型进程。
	defer ticker.Stop()
	for {
		// 首轮立即查询，用户不需要先点检查更新才能发现新版。
		s.Check(s.ctx)
		select {
		// 退出立刻停止等待，不能因六小时检查间隔而拖延软件关闭。
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// CheckEndpoint 响应用户主动检查，网络请求在后台完成。
// 参数 body：保留端点统一签名，不接受自定义地址、登录凭据或下载命令。
// 返回：当前状态；轮询更新最终结果，不因断网阻塞界面。
func (s *Service) CheckEndpoint(_ []byte) (any, error) {
	// 合并检查由 Check 自身保证，连续点击也不会并行消耗 API 限额。
	go s.Check(s.ctx)
	return s.Snapshot(), nil
}

// OpenEndpoint 打开本轮已验证的 GitHub 页面，由用户决定下载和切换版本。
// 参数 body：kind 为 release 或 download；不接受任意 URL。
// 返回：Shell 打开结果；没有可信新版资源时明确报错，不自动退出或覆盖 EXE。
func (s *Service) OpenEndpoint(body []byte) (any, error) {
	// 请求只包含用途，最终 URL 始终从已校验的状态中读取。
	// 复用工作台严格 JSON 解码，不能通过附加未定义字段指定其他外部操作。
	req, err := workspace.Decode[struct {
		Kind string `json:"kind"`
	}](body)
	if err != nil {
		return nil, err
	}
	// 快照保证选择的版本、发布页和附件属于同一次已完成的检查。
	state := s.Snapshot()
	url := state.ReleaseURL
	// 软件只提供这两类链接，客户端不能用其他用途拼接命令。
	// 没有完成检查时 URL 为空，系统入口会拒绝打开并提示用户重新检查。
	if req.Kind != "release" && req.Kind != "download" {
		return nil, errors.New("更新操作无效")
	}
	if req.Kind == "download" {
		url = state.DownloadURL
	}
	// Windows 入口再次检查 HTTPS 及 GitHub 域名，不接受自定义协议执行程序。
	return nil, desktop.OpenWebURL(url)
}

// Check 执行一次有限时、可取消的公开发布查询。
// 参数 ctx：本次检查上下文，退出后不再保留迟到网络结果。
// 不返回业务值；错误仅写入更新状态和脱敏日志，不改变任何本地发版数据。
func (s *Service) Check(ctx context.Context) {
	// 正在检查时直接复用该轮请求，不能拿同一个客户端发起重复查询。
	s.mu.Lock()
	if s.status.Checking || ctx.Err() != nil {
		s.mu.Unlock()
		return
	}
	s.status.Checking = true
	// 在释放锁前占用检查标记，使启动轮询与用户手动检查能够合并。
	s.mu.Unlock()
	// 不持有业务或状态锁读取远端，离线时仍可编辑和执行本地任务。
	result, err := s.fetch(ctx)
	// 状态写回前重新获取短锁，轮询无需等候网络响应。
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.Checking = false
	// 软件已经退出时丢弃迟到响应，不再显示本轮检查成功或网络故障。
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		// 保留上次可靠的发布信息，不把临时网络失败当成没有新版。
		// 错误不包含响应正文、数据库地址或任何登录凭据。
		s.status.Error = "暂时无法检查更新，请稍后重试"
		slog.Warn("软件更新检查失败", "error", err)
		return
	}
	// 只接受完整响应；时间属于本轮成功检查，不伪造离线情况下的确认。
	result.CheckedAt = time.Now().UTC().Format(time.RFC3339)
	// 新状态整体替换，不能拼接上一次发布的下载地址和本次版本说明。
	s.status = result
	slog.Info("软件更新检查完成", "current", s.current, "latest", result.Latest, "available", result.Available)
}
