package settingsservice

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"

	"release-desk/internal/policy"
)

// SaveVerificationConcurrency 保存验证队列的并发上限。
// 参数 body：包含 concurrency 的 JSON；仅允许 1～统一策略上限，不接受其他设置字段。
// 返回：持久化结果；运行中的队列固定上限，修改不会覆盖仓库或数据库配置。
func (s *Service) SaveVerificationConcurrency(body []byte) (any, error) {
	// 只更新这一项偏好，避免前端携带过期完整配置覆盖用户其他设置。
	var req struct {
		Concurrency int `json:"concurrency"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	// 该窄接口只有一个可修改字段，未知字段明确报错以便发现过期页面。
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		// 严格解码也会拒绝额外字段，不能通过这个端点修改仓库设置。
		return nil, err
	}
	// 不允许第二份 JSON 尾随，保持一次请求只修改一个明确偏好。
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("并发设置请求包含多份 JSON")
	}
	// 上限复用后端策略，不信任前端下拉框一定来自当前版本。
	if req.Concurrency < 1 || req.Concurrency > policy.VerificationConcurrency {
		return nil, fmt.Errorf("验证并发数必须为 1～%d", policy.VerificationConcurrency)
	}
	// 保存偏好与队列启动同锁，启动瞬间也不会读到半更新的上限。
	s.Mu.Lock()
	defer s.Mu.Unlock()
	// 本轮工作者池在启动时固定；运行中明确拒绝，不能显示与真实并行数不符的值。
	if s.VerifyQueue != nil && s.VerifyQueue.Running || s.SingleVerify != nil && s.SingleVerify.Running {
		return nil, fmt.Errorf("请在当前验证队列结束后修改并发数")
	}
	// 采用原子落盘与完整配置副本，只更改这一项且保留其他字段。
	if err := s.RememberVerificationConcurrency(req.Concurrency); err != nil {
		return nil, err
	}
	// 保存成功才刷新控件，任务仍由用户另行点击启动。
	s.Revision++
	slog.Info("验证并发数已保存", "concurrency", req.Concurrency)
	return nil, nil
}
