package aiservice

import (
	"context"
	"regexp"
	"strings"
	"time"

	"release-desk/internal/policy"
	"release-desk/internal/storage/workspace"
	"release-desk/internal/tasks"
)

type verificationProgressKey struct{}

var progressSensitiveValue = regexp.MustCompile(`(?i)(password|passwd|token|secret|api[_-]?key|cookie)\s*[:=]\s*[^\s,;，；]+`)
var progressBearer = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/-]+=*`)
var progressPhone = regexp.MustCompile(`1[3-9][0-9]{9}`)

// ReportVerificationProgress 发布一条核验行动或发现的摘要。
// 参数 ctx：当前事项上下文，含独立进度回调；取消后不接受迟到内容。
// 参数 summary：适合用户阅读的简短说明，不应传入工具正文或内部推理。
// 不返回业务值；旧调用方没有回调时忽略，不影响最终结果读取。
func ReportVerificationProgress(ctx context.Context, summary string) {
	// 取消同时限制外部进程与进度回填，不能出现已停止后仍继续核验的假象。
	if ctx.Err() != nil {
		return
	}
	// 回调只存在于当前事项上下文，不通过全局变量广播给其他并行项。
	if publish, ok := ctx.Value(verificationProgressKey{}).(func(string)); ok {
		publish(summary)
	}
}

// recordVerificationProgress 将已过滤摘要写入当前事项及任务历史。
// 参数 ctx：该项取消上下文；参数 queue：固定的队列身份。
// 参数 index：领取时确定的稳定行索引；参数 summary：外部候选摘要，仍需脱敏和限长。
// 不返回业务值；重复、过期或停止后的消息丢弃，不改变正式验证结论。
func (s *Service) recordVerificationProgress(ctx context.Context, queue *workspace.VerifyQueue, index int, summary string) {
	// 候选先脱敏再存储，原始事件流和消息不会进入应用日志。
	summary = sanitizeVerificationSummary(summary)
	if summary == "" || ctx.Err() != nil {
		// 空白或已停止事件不会刷新修订号，也不会制造重复历史。
		return
	}
	// 进度只占用短临界区，外部模型调用和日志查询均不持有此锁。
	s.Mu.Lock()
	defer s.Mu.Unlock()
	// 结果写回与进度使用同一边界，旧任务不能污染下一轮队列。
	if s.VerifyQueue != queue || !queue.Running || queue.CancelRequested || ctx.Err() != nil || queue.Rows[index].State != "验证中" {
		return
	}
	progress := queue.Rows[index].Progress
	if len(progress) > 0 && progress[len(progress)-1].Summary == summary {
		// 工具可能连续发出相同活动提示，去重以保留真正有信息的新发现。
		return
	}
	// 时间记录消息被接收的时刻，不伪装为生产日志的发生时间。
	// 保存有限的最近进展，每项独立计数，六并发不会相互覆盖。
	progress = append(progress, tasks.ProgressEntry{At: time.Now().UTC().Format(time.RFC3339Nano), Summary: summary})
	if len(progress) > policy.MaxVerificationProgress {
		progress = progress[len(progress)-policy.MaxVerificationProgress:]
	}
	queue.Rows[index].Progress = progress
	// 只持久化可公开摘要；查询运行日志的原文仍由最终受限结果保存。
	s.TrackTask("verify")
	s.Revision++
}

// sanitizeVerificationSummary 限制进度摘要内容，补充屏蔽常见敏感字段。
// 参数 summary：模型明确标记的核验进展或程序生成的阶段说明。
// 返回：脱敏、限长后的纯文本；空内容不作为一次进度变更。
func sanitizeVerificationSummary(summary string) string {
	// Bearer 先处理，避免授权字段中的空格让凭据尾部残留。
	text := progressBearer.ReplaceAllString(strings.TrimSpace(summary), "Bearer [已脱敏]")
	text = progressSensitiveValue.ReplaceAllString(text, "$1=[已脱敏]")
	// 生产摘要只需要关联信息，不需要展示客户手机号码。
	text = progressPhone.ReplaceAllString(text, "[手机号已脱敏]")
	// 按 Unicode 字符限长，避免截断中文；完整取证结论仍在正式结果区。
	runes := []rune(text)
	if len(runes) > policy.MaxProgressSummaryRunes {
		text = string(runes[:policy.MaxProgressSummaryRunes]) + "…"
	}
	return text
}
