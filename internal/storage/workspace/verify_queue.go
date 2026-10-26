package workspace

import "release-desk/internal/tasks"

type VerifyQueue struct {
	ID              string           `json:"id"`
	Version         string           `json:"version"`
	Running         bool             `json:"running"`
	Rows            []VerifyQueueRow `json:"rows"`
	Concurrency     int              `json:"concurrency"`
	CancelRequested bool             `json:"cancel_requested"`
	// 工作快照只在本轮进程中调度，重启后保留历史但不会自动重放收费任务。
	Work    []VerificationWork `json:"-"`
	Changed chan struct{}      `json:"-"`
}

type VerifyQueueRow struct {
	ID        string                `json:"id,omitempty"`
	Version   string                `json:"version,omitempty"`
	ItemID    string                `json:"item_id"`
	Title     string                `json:"title"`
	State     string                `json:"state"`
	Detail    string                `json:"detail"`
	Condition string                `json:"condition,omitempty"`
	Result    *VerifyResult         `json:"result,omitempty"`
	Progress  []tasks.ProgressEntry `json:"progress,omitempty"`
}

// VerificationWork 绑定逐项确认的版本、时间窗与业务快照，不共享其他事项的条件。
type VerificationWork struct {
	Version string
	Start   string
	End     string
	Item    Item
}
