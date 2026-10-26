package aiservice

import (
	"context"
	"errors"
	"log/slog"

	"release-desk/internal/identity"
	"release-desk/internal/policy"
	"release-desk/internal/storage/workspace"
)

// VerificationReceipt 说明请求是否新加入队列；重复点击只返回已有任务身份。
type VerificationReceipt struct {
	QueueID  string `json:"queue_id"`
	Accepted bool   `json:"accepted"`
}

// EnqueueVerification 将一项独立验证加入可并行处理的队列。
// 参数 req：用户确认的版本、事项和北京时间窗口，每项独立固定。
// 参数 run：只读取证器；首项建立队列，后续项复用同一执行边界。
// 返回：队列身份和是否新加入；扫描、SQL、停止中或保存失败时不调用模型。
func (s *Service) EnqueueVerification(req VerifyRequest, run VerificationRunner) (*VerificationReceipt, error) {
	// 无效窗口和跨类型选择在占用工作区前拒绝，不生成收费任务。
	if err := ValidateVerifyWindow(req); err != nil {
		// 错误时间不进入任务历史，也不占用一个并发名额。
		return nil, err
	}
	items, err := s.SelectedItems(workspace.SelectionRequest{Version: req.Version, IDs: []string{req.ID}}, "verify")
	if err != nil {
		// 记录不存在或类型不符时拒绝整条请求，不推断相似标题。
		return nil, err
	}
	// 人工项在取得工作区占用前拒绝，不能混入正在运行的并发队列。
	if err := RequireAutomaticVerification(items); err != nil {
		return nil, err
	}
	// 追加、去重和队列空闲收尾在同一个临界区内衔接。
	s.Mu.Lock()
	defer s.Mu.Unlock()
	// 只放开验证项之间的并行；扫描、SQL 和退出仍保留原先的互斥规则。
	if s.Closing || s.SQLActive() || s.Scan != nil && s.Scan.Running {
		return nil, errors.New("扫描、SQL 或退出任务正在处理，暂不能加入验证队列")
	}
	queue := s.VerifyQueue
	if queue != nil && queue.Running {
		// 停止是针对整队列的明确操作，不能在取消传播期间插入新任务。
		if queue.CancelRequested {
			return nil, errors.New("验证队列正在停止，请在结束后重试")
		}
		for _, row := range queue.Rows {
			// 以版本和事项共同去重；不同版本即使 ID 相同也属于独立任务。
			if row.Version == req.Version && row.ItemID == req.ID && (row.State == "等待中" || row.State == "验证中") {
				return &VerificationReceipt{QueueID: queue.ID}, nil
			}
		}
		return s.appendVerificationLocked(queue, req, items[0])
	}
	// 已结束队列不再复用执行上下文，新请求会取得新的队列身份。
	// 兼容旧版已经运行的单项任务，不越过其取消和保存边界。
	if s.AIBusy {
		return nil, errors.New("请等待当前验证任务结束后加入队列")
	}
	count := s.Config().VerificationConcurrency
	if count == 0 {
		// 兼容没有保存过并发偏好的旧设置文件。
		count = policy.VerificationConcurrency
	}
	// 空闲时第一项立即建立完整工作者池，后续点击无需等待首项结束。
	err = s.startVerificationQueueLocked(VerifyQueueRequest{Version: req.Version, IDs: []string{req.ID}, Start: req.Start, End: req.End, Concurrency: count}, items, run)
	if err != nil {
		// 启动保存失败时不返回接收成功，界面可保留原状态供再次操作。
		return nil, err
	}
	return &VerificationReceipt{QueueID: s.VerifyQueue.ID, Accepted: true}, nil
}

// appendVerificationLocked 先保存新增范围，再通知空闲工作者。
// 参数 queue：当前运行且未取消的队列；参数 req：独立的验证窗口与目标。
// 参数 item：已克隆的事项快照；调用方须持有 Mu，禁止工作者读取未保存的条目。
// 返回：追加回执；保存失败回退新增范围，已在运行的任务保持原样。
func (s *Service) appendVerificationLocked(queue *workspace.VerifyQueue, req VerifyRequest, item workspace.Item) (*VerificationReceipt, error) {
	// 单行与批量追加共用原子保存入口，避免两套独立的去重与回退逻辑。
	return s.appendVerificationSelectionLocked(queue, req, []workspace.Item{item})
}

// appendVerificationSelectionLocked 将一组独立事项原子追加到已有队列。
// 参数 queue：当前运行队列；参数 req：这批新事项确认的版本与时间。
// 参数 items：已校验的独立快照，等待或运行的重复项自动合并；调用方须持有 Mu。
// 返回：是否实际追加及队列身份；保存失败整组回退，不出现只接收半批的状态。
func (s *Service) appendVerificationSelectionLocked(queue *workspace.VerifyQueue, req VerifyRequest, items []workspace.Item) (*VerificationReceipt, error) {
	// 记录旧长度，失败回退不会重置已经开始的进度或证据。
	length := len(queue.Rows)
	for _, item := range items {
		// 每个新选择独立判断，不因同批存在重复项而丢弃其他有效事项。
		duplicate := false
		for _, row := range queue.Rows {
			// 按版本及事项去重；已经结束的旧行不妨碍用户明确发起新一轮。
			if row.Version == req.Version && row.ItemID == item.ID && (row.State == "等待中" || row.State == "验证中") {
				duplicate = true
				break
			}
		}
		if !duplicate {
			// 只追加快照，此处持有工作区锁，尚不会有工作者读取未保存条目。
			appendVerificationWork(queue, req, item)
		}
	}
	// 全部已经排队时只返回已有身份，不能生成空任务或重复模型调用。
	if len(queue.Rows) == length {
		return &VerificationReceipt{QueueID: queue.ID}, nil
	}
	// 整组一次保存，任何磁盘错误都不允许留下半批等待项。
	if err := s.SaveVerifyTask(); err != nil {
		// 仅新追加项尚未交给任何工作者，可安全撤回而不影响现有任务。
		queue.Rows = queue.Rows[:length]
		queue.Work = queue.Work[:length]
		return nil, err
	}
	// 条件已可靠落盘，才广播可领取事件；没有空位时保持等待状态。
	notifyVerificationQueue(queue)
	s.Revision++
	// 运行日志仅含队列身份和规模，完整取证条件仍保存在本机历史。
	slog.Info("验证事项加入队列", "queue_id", queue.ID, "version", req.Version, "item_id", req.ID, "count", len(queue.Rows))
	return &VerificationReceipt{QueueID: queue.ID, Accepted: true}, nil
}

// appendVerificationWork 组装一份逐项任务及可回看的条件。
// 参数 queue：调用方锁内持有的队列；参数 req：用户确认的目标和时间。
// 参数 item：独立快照；本方法不启动模型，也不把旧验证结论当作新证据。
// 不返回业务值；进度与工作快照保持相同索引，追加后由调用方保存。
func appendVerificationWork(queue *workspace.VerifyQueue, req VerifyRequest, item workspace.Item) {
	// 行内进度不覆盖原事项状态，首轮结果仍能在重新取证期间查看。
	queue.Rows = append(queue.Rows, workspace.VerifyQueueRow{ID: identity.NewID(), Version: req.Version, ItemID: item.ID, Title: item.Data["title"], State: "等待中", Condition: VerificationCondition(req, item)})
	// 完整业务输入不进入状态轮询和任务目录，避免把大段正文重复发送到界面。
	queue.Work = append(queue.Work, workspace.VerificationWork{Version: req.Version, Start: req.Start, End: req.End, Item: workspace.Clone(item)})
}

// notifyVerificationQueue 广播队列条件变化，避免空闲工作者忙轮询。
// 参数 queue：调用方持有 Mu 的队列；运行过程中每次更新都会替换广播通道。
// 不返回业务值；所有旧通道等待者同时唤醒，新等待者订阅下一次变更。
func notifyVerificationQueue(queue *workspace.VerifyQueue) {
	// 关闭通道作为广播，只由锁内调用方执行，不会重复关闭同一通道。
	if queue.Changed != nil {
		close(queue.Changed)
	}
	// 后续等待监听新通道，过去的事件不能导致持续自旋。
	queue.Changed = make(chan struct{})
}

// takeVerificationWork 领取一个等待项；无等待项时等待追加或现有事项完成。
// 参数 ctx：整个队列的取消上下文；参数 queue：当前工作者所属的固定队列。
// 返回：稳定行索引、独立条件及是否领取成功；终止或队列收尾时返回 false。
func (s *Service) takeVerificationWork(ctx context.Context, queue *workspace.VerifyQueue) (int, workspace.VerificationWork, bool) {
	for {
		// 只在选取下一项时持锁，模型调用始终留在领取完成之后。
		s.Mu.Lock()
		// 占用和取消统一判定，旧工作者不能领取下一轮队列的内容。
		if ctx.Err() != nil || !queue.Running || queue.CancelRequested || s.Closing {
			// 释放锁再退出，取消处理器和其他工作者仍能完成收尾。
			s.Mu.Unlock()
			return 0, workspace.VerificationWork{}, false
		}
		active := false
		// 队列追加顺序即等待顺序，已经取消或结束的行不会再次被领取。
		for index, row := range queue.Rows {
			if row.State == "等待中" {
				// 领取与开始标记同锁，其他工作者不会重复拿到该索引。
				work := queue.Work[index]
				// 进度先登记再交付，页面能立即阻止该事项的重复入队。
				s.UpdateVerifyQueue(queue, index, "验证中", "正在结合版本代码与生产日志取证", nil)
				s.Mu.Unlock()
				return index, work, true
			}
			active = active || row.State == "验证中"
		}
		// 没有等待项但仍有取证运行时，必须留下工作者接收后续追加。
		if !active {
			// 收尾与入队原子衔接：请求要么加入本轮，要么建立下一轮，不会丢失。
			s.completeVerificationQueueLocked(queue)
			s.Mu.Unlock()
			return 0, workspace.VerificationWork{}, false
		}
		// 在锁内取得当前广播通道，避免错过刚刚发生的追加事件。
		changed := queue.Changed
		s.Mu.Unlock()
		// 不持有锁等待，页面可继续入队、查看结果或停止整个队列。
		select {
		case <-ctx.Done():
			return 0, workspace.VerificationWork{}, false
		case <-changed:
		}
	}
}
