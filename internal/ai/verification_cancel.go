package aiservice

import (
	"errors"
	"log/slog"

	"release-desk/internal/storage/workspace"
)

// CancelQueuedVerification 单独撤销尚未领取的事项，不停止其他验证。
// 参数 body：包含 queue_id 和 row_id 的 JSON，防止旧页面撤销后来重新排队的同名事项。
// 返回：已取消返回 nil；已开始、身份过期或保存失败时拒绝并保留原状态。
func (s *Service) CancelQueuedVerification(body []byte) (any, error) {
	// 行身份与队列身份一起校验，不按标题或当前选中行猜测撤销目标。
	req, err := workspace.Decode[struct {
		QueueID string `json:"queue_id"`
		RowID   string `json:"row_id"`
	}](body)
	if err != nil {
		// 畸形 JSON 不能被解释为取消全部等待项。
		return nil, err
	}
	// 取消与工作者领取互斥，保证不会显示已取消后再调用模型。
	s.Mu.Lock()
	defer s.Mu.Unlock()
	// 仅查看当前队列；历史任务文件不是可执行的撤销命令。
	queue := s.VerifyQueue
	if queue == nil || !queue.Running || queue.ID != req.QueueID {
		// 队列已经切换时不借用新队列中的同名记录。
		return nil, errors.New("验证队列已结束或变化，请刷新后查看")
	}
	for index, row := range queue.Rows {
		if row.ID != req.RowID {
			// 每次重试都有独立行 ID，旧按钮不会影响重新加入的任务。
			continue
		}
		// 与领取使用同一把锁；若工作者先开始，取消不能伪装成没有调用模型。
		if row.State != "等待中" {
			return nil, errors.New("该项已开始或结束，无法再取消排队")
		}
		queue.Rows[index].State = "已取消"
		// 撤销只属于排队进度，不将原事项写成业务验证失败。
		queue.Rows[index].Detail = "开始前已取消排队，未调用 Codex，原有验证结论保留"
		// 先持久化再唤醒工作者，磁盘故障时完整撤回取消状态。
		if err := s.SaveVerifyTask(); err != nil {
			queue.Rows[index] = row
			return nil, err
		}
		queue.Work[index].Item = workspace.Item{}
		// 释放尚未使用的输入正文，并唤醒可能正在等待队列变化的工作者。
		notifyVerificationQueue(queue)
		s.Revision++
		// 仅记录稳定身份，不传播修改正文和生产证据。
		slog.Info("验证事项取消排队", "queue_id", queue.ID, "version", row.Version, "item_id", row.ItemID)
		return nil, nil
	}
	// 未找到精确行时给出过期提示，不能默默报告取消成功。
	return nil, errors.New("待取消的排队事项已变化")
}
