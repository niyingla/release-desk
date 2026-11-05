package logquery

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/volcengine/volc-sdk-golang/service/tls"
	"release-desk/internal/policy"
)

// contextTransport 将旧式 SDK 的 HTTP 请求绑定到本轮取消上下文。
type contextTransport struct {
	ctx  context.Context
	base http.RoundTripper
}

// boundedBody 限制 SDK 一次读取的响应体，Close 始终释放原始连接。
type boundedBody struct {
	io.Reader
	io.Closer
}

// RoundTrip 透传一次签名后的 HTTPS 请求，并保留应用取消与超时约束。
// 参数 request：官方 SDK 组装的只读请求，不在本层输出请求头或正文。
// 返回：HTTP 响应或取消、传输错误；不自行重试或记录凭据。
func (t contextTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	// SDK 的接口没有逐请求 context 参数，传输层绑定可保证停止任务及时生效。
	response, err := t.base.RoundTrip(request.Clone(t.ctx))
	if err != nil {
		return nil, err
	}
	// 多个并发验证不能因单次巨量日志响应占满内存，SDK 解码仍保留其真实错误语义。
	if response.ContentLength > policy.MaxLogResponseBytes {
		_ = response.Body.Close()
		return nil, errors.New("TLS 响应过大，请缩小检索条件或采样量")
	}
	// 未提供长度的响应同样有界；保留原 Close，不让截断样本泄漏 HTTP 连接。
	response.Body = boundedBody{Reader: io.LimitReader(response.Body, policy.MaxLogResponseBytes+1), Closer: response.Body}
	return response, nil
}

// newTLSClient 建立仅供本模块只读方法调用的官方 TLS SDK 客户端。
// 参数 ctx：当前操作已经有界的上下文，页面测试和 AI 查询各自独立。
// 参数 config：当前固定的内存连接，密钥仅交给 SDK 签名，不进入工具参数。
// 返回：独立客户端；不更改 SDK 全局客户端、默认超时或用户的代理配置。
func newTLSClient(ctx context.Context, config Config) tls.Client {
	client := tls.NewClient(config.Endpoint, config.AccessKey, config.SecretKey, "", config.Region)
	// 使用独立 HTTP 客户端，不能通过 SDK SetTimeout 意外修改其他并发查询。
	_ = client.SetHttpClient(&http.Client{
		Timeout:   policy.LogRequestSeconds * time.Second,
		Transport: contextTransport{ctx: ctx, base: http.DefaultTransport},
		// 已签名凭据不能跟随跳转交给另一个主机。
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	})
	// 查询失败由界面或 AI 明确决定下一次取证，不隐式等待多轮 SDK 重试。
	client.SetRetryPolicy(tls.RetryPolicy{MaxAttempts: 1})
	return client
}

// safeError 将 SDK 错误转成不包含凭据、云端正文或请求参数的可读原因。
// 参数 err：客户端错误，可能含签名头或用户检索条件，不能直接打印。
// 返回：分类后的中文错误；调用成功时返回 nil。
func safeError(err error) error {
	// 连接错误只描述取证能力，不修改任何事项的业务验证结论。
	// 仅使用结构化状态分类，禁止直接拼接可能回显签名头的 SDK Error 文本。
	// 资源缺失时修正当前环境映射，不自动尝试其他环境的同名 Topic。
	// 限流需要用户或本轮 AI 显式决定是否再次查询，不在这里隐式重放。
	if err == nil {
		return nil
	}
	// 取消与超时保留稳定身份，外层不能将其当作业务无日志。
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var remote *tls.Error
	if errors.As(err, &remote) {
		// 不拼接 SDK Message 或 RequestID，服务端错误正文可能回显原始请求。
		if remote.HTTPCode == 401 || remote.HTTPCode == 403 {
			return errors.New("TLS 鉴权或读取权限不足，请检查 AK/SK、地域及只读授权")
		}
		if remote.HTTPCode == 429 {
			return errors.New("TLS 查询限流，请稍后重试")
		}
		if remote.HTTPCode == 404 {
			return errors.New("TLS 项目或 Topic 不存在，请检查当前环境映射")
		}
	}
	return errors.New("TLS 请求失败，请检查网络、地域、Endpoint 和日志资源权限")
}

// requestContext 为用户主动的连接检查建立独立超时，不占用扫描或 SQL 通道。
// 无业务入参；仅继承本软件退出上下文。
// 返回：有界上下文及必须调用的取消方法，不创建业务任务。
func (s *Service) requestContext() (context.Context, context.CancelFunc) {
	// 设置页的测试请求拥有独立生命周期，不复用正在运行的验证取消句柄。
	parent := s.Store.TaskContext
	// 纯端点隔离测试没有桌面生命周期，正常运行仍随软件退出一起取消。
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, policy.LogRequestSeconds*time.Second)
}

// Projects 读取用户本次连接可见的项目元数据，不保存或返回凭据。
// 参数 body：当前尚未保存的连接输入；空密钥复用已有本机连接。
// 返回：项目列表及是否截断；网络或鉴权失败明确返回错误，不伪造默认列表可访问。
func (s *Service) Projects(body []byte) (any, error) {
	// 使用当前未保存表单，便于先检查访问范围再决定是否覆盖已有密钥。
	// 本接口只列项目元数据，项目可见并不代表日志检索权限已经通过。
	// 返回字段限定为名称和 ID，SDK 响应头、完整元数据及凭据不回传。
	// 读取失败保留页面输入，用户可修正地域或授权后重新尝试。
	config, err := s.resolve(body)
	if err != nil {
		return nil, err
	}
	ctx, cancel := s.requestContext()
	defer cancel()
	// 项目读取不检索日志正文，用户可先核对环境，再决定保存哪一个映射。
	response, err := newTLSClient(ctx, config).DescribeProjects(&tls.DescribeProjectsRequest{PageNumber: 1, PageSize: policy.LogResourcePageSize})
	if err != nil {
		return nil, safeError(err)
	}
	list := []ResourceOption{}
	for _, project := range response.Projects {
		list = append(list, ResourceOption{ID: project.ProjectID, Name: project.ProjectName})
	}
	// 读取上限明确回显，不能把第一页当成全账号资源总量。
	return map[string]any{"projects": list, "truncated": response.Total > int64(len(list))}, nil
}

// checkTopic 核对当前环境固定 Topic 确实属于对应项目。
// 参数 client：本轮只读客户端；参数 config：已经固定的连接和环境映射。
// 参数 environment：prod、test 或 uat，不在缺失配置时回退另一环境。
// 返回：真实 Topic 元数据；未配置、归属不同或读取失败时拒绝继续检索。
func checkTopic(client tls.Client, config Config, environment string) (*tls.DescribeTopicResponse, error) {
	// 云端真实归属优先于随包缓存，主题 ID 不接受模型工具参数覆盖。
	// 相同主题名称可能存在于不同项目，必须在检索正文前校验所属环境。
	// 元数据读取失败就结束本次请求，不能凭默认映射假装当前账号可以读取。
	project, topic := config.Projects[environment], config.Topics[environment]
	// Topic 不能只凭名字或历史缓存决定，实际项目归属必须在本次连接下成立。
	if project == "" || topic == "" {
		return nil, errors.New("当前环境未配置固定的后端日志项目与 Topic")
	}
	response, err := client.DescribeTopic(&tls.DescribeTopicRequest{TopicID: topic})
	if err != nil {
		return nil, safeError(err)
	}
	if response.ProjectID != project || response.TopicID != topic {
		return nil, errors.New("日志 Topic 与当前环境项目不一致，未发起日志检索")
	}
	return response, nil
}

// Test 检查网络、签名、资源归属与只读检索权限，不保存本次输入或日志正文。
// 参数 body：当前连接及环境映射；用户明确点击测试后才调用云端只读接口。
// 返回：各环境独立结果；测试只查询最近一分钟最多一条，零命中不表示没有权限。
func (s *Service) Test(body []byte) (any, error) {
	// 测试使用当前表单快照，不保存密钥，也不更新发布事项的验证状态。
	// 固定展示顺序仅用于用户比较三套环境，不表示它们共享权限或日志内容。
	// 未启用的空白环境不发请求，配置不完整的已启用环境则独立给出原因。
	// 每个环境单独限时，生产鉴权失败不能掩盖测试环境的实际可访问状态。
	// 查询零样本也可能拥有权限，成功响应只证明读取能力，不代表业务已触发。
	// SDK 日志样本只在调用期间存在，最终结果仅返回固定的安全检查摘要。
	config, err := s.resolve(body)
	if err != nil {
		return nil, err
	}
	checks := []map[string]any{}
	// 一个环境失败不阻止其余环境检查，结果不能混成整体业务通过或失败。
	for _, environment := range []string{"prod", "test", "uat"} {
		if config.Projects[environment] == "" && config.Topics[environment] == "" {
			continue
		}
		ctx, cancel := s.requestContext()
		client := newTLSClient(ctx, config)
		topic, checkErr := checkTopic(client, config, environment)
		if checkErr == nil {
			// 仅验证 SearchLogs 读取权限，不返回或持久化此样本；业务验证仍使用用户指定窗口。
			now := time.Now()
			_, checkErr = client.SearchLogsV2(&tls.SearchLogsRequest{TopicID: topic.TopicID, Query: "*", StartTime: now.Add(-time.Minute).UnixMilli(), EndTime: now.UnixMilli(), Limit: 1})
			checkErr = safeError(checkErr)
		}
		cancel()
		row := map[string]any{"environment": environment, "success": checkErr == nil, "detail": "网络、鉴权、资源归属及日志读取权限通过"}
		// 只写入固定安全摘要，不把 SDK 原始响应带回页面或历史。
		if checkErr != nil {
			row["detail"] = fmt.Sprint(checkErr)
		}
		checks = append(checks, row)
	}
	return map[string]any{"checks": checks}, nil
}
