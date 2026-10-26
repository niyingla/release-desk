package aiservice

import (
	"context"
	"errors"

	"release-desk/internal/storage/workspace"
)

// RunVerifyQueueItem 执行一个独立事项，业务结论失败不阻断其他工作者。
// 参数 ctx：整个批次的可取消上下文；用户停止、退出或保存失败都会取消。
// 参数 queue：本轮固定队列，所有进度修改必须持有工作区锁。
// 参数 index：此事项在队列中的稳定索引，不随完成顺序变化。
// 参数 req：批次版本及统一的北京时间窗口。
// 参数 item：启动时克隆的业务快照，缺少参考条件仍会交给 AI 分析。
// 参数 config：启动时固定的非敏感设置，不能读取正在编辑的配置。
// 参数 run：单项只读取证边界，真实执行器调用本机 Codex。
// 返回：可以继续领取下一项时为 true；取消或持久化异常时为 false。
func (s *Service) RunVerifyQueueItem(ctx context.Context, queue *workspace.VerifyQueue, index int, req VerifyQueueRequest, item workspace.Item, config workspace.Settings, run VerificationRunner) bool {
	// 开始状态和停止请求共用锁；已取消的索引保持等待，最后收为未验证。
	s.Mu.Lock()
	if ctx.Err() != nil || queue.CancelRequested || s.Closing {
		// 已领取但尚未调用模型的项也应明确结束，不能遗留“验证中”。
		s.UpdateVerifyQueue(queue, index, "已停止", "本轮验证在取证前停止", nil)
		s.Mu.Unlock()
		return false
	}
	// 开始状态已由领取器原子登记，立即释放锁，不阻塞页面追加事项。
	s.Mu.Unlock()
	request := VerifyRequest{Version: req.Version, ID: item.ID, Start: req.Start, End: req.End}
	// 回调绑定队列与独立行号，六个并行调用的实时摘要不会串到其他事项。
	ctx = context.WithValue(ctx, verificationProgressKey{}, func(summary string) { s.recordVerificationProgress(ctx, queue, index, summary) })
	ReportVerificationProgress(ctx, "正在定位核心业务目标，对比修复前后代码并准备关键日志取证")
	// 每项调用持有独立输出目录；未完成项不能借用其他项的证据。
	// 起止时间来自该项确认时的快照，不能借用最早入队事项的窗口。
	result, err := run(ctx, request, item, config)
	if ctx.Err() != nil {
		// 故意忽略工具取消后返回的通过，不能把迟到结论写回版本。
		s.FinishVerifyQueueRow(queue, index, "已停止", "本轮验证已停止，原有结论保留", nil)
		return false
	}
	if err != nil {
		// 工具故障只表示证据不足，业务失败和不足仍允许继续领取后续项。
		result = workspace.VerifyResult{Status: "证据不足", Confirmation: "未确认", Blocker: "取证受阻", Summary: err.Error(), Environment: "生产", Window: req.Start + " ~ " + req.End, Evidence: []string{}, NextSteps: "检查取证工具和网络后重试；已取得证据的缺口按实际结果补充"}
	}
	// 本轮时间和原始依据先写入事项历史，写盘成功后才显示最终状态。
	result.At = workspace.Stamp()
	if err = s.FinishVerification(ctx, request, item, result); err != nil {
		state, detail := "保存失败", err.Error()
		// 结果在等待写锁期间也可能被用户取消，因此需区分停止与真正的磁盘故障。
		if errors.Is(err, context.Canceled) {
			state, detail = "已停止", "本轮验证已停止，迟到结论未写入"
		}
		// 保存失败由工作者取消整个批次，避免继续消耗额度却无法可靠留档。
		s.FinishVerifyQueueRow(queue, index, state, detail, nil)
		return false
	}
	// 保存已经成功，即使此刻收到停止，也必须保留这份已接受的独立结论。
	s.FinishVerifyQueueRow(queue, index, result.Status, result.Summary, &result)
	return true
}

// FinishVerifyQueueRow 原子更新单行终态和对应证据，不修改其他行。
// 参数 queue：仍持有批次占用的队列；参数 index：已校验的独立行号。
// 参数 state：本轮明确终态；参数 detail：可回看的脱敏摘要。
// 参数 result：已持久化的验证结论；取消或故障时为 nil。
// 不返回业务值；任务历史保存失败由统一 TrackTask 提示。
func (s *Service) FinishVerifyQueueRow(queue *workspace.VerifyQueue, index int, state, detail string, result *workspace.VerifyResult) {
	// 与其他工作者和停止操作串行保存，不能在并发遍历时修改队列。
	s.Mu.Lock()
	defer s.Mu.Unlock()
	// 复用同一进度计算与任务持久化入口，结果不依赖完成先后顺序。
	s.UpdateVerifyQueue(queue, index, state, detail, result)
}
