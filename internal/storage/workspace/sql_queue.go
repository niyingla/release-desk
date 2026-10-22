package workspace

type SQLQueue struct {
	Environment string        `json:"environment,omitempty"`
	ID          string        `json:"id"`
	Version     string        `json:"version"`
	Running     bool          `json:"running"`
	Rows        []SQLQueueRow `json:"rows"`
}

type SQLQueueRow struct {
	ItemID     string      `json:"item_id"`
	Name       string      `json:"name"`
	Phase      string      `json:"phase"`
	Side       string      `json:"side"`
	State      string      `json:"state"`
	Detail     string      `json:"detail"`
	Executions []Execution `json:"executions"`
}
