package workspace

type VerifyQueue struct {
	ID              string           `json:"id"`
	Version         string           `json:"version"`
	Running         bool             `json:"running"`
	Rows            []VerifyQueueRow `json:"rows"`
	Concurrency     int              `json:"concurrency"`
	CancelRequested bool             `json:"cancel_requested"`
}

type VerifyQueueRow struct {
	ItemID    string        `json:"item_id"`
	Title     string        `json:"title"`
	State     string        `json:"state"`
	Detail    string        `json:"detail"`
	Condition string        `json:"condition,omitempty"`
	Result    *VerifyResult `json:"result,omitempty"`
}
