package workspace

// VersionScanState 只保存扫描进度，不与生产日志验证结论混用。
type VersionScanState struct {
	ID              string         `json:"id"`
	Version         string         `json:"version"`
	Author          string         `json:"author"`
	Running         bool           `json:"running"`
	Phase           string         `json:"phase"`
	CancelRequested bool           `json:"cancel_requested"`
	Cancelled       bool           `json:"cancelled"`
	Started         string         `json:"started"`
	Finished        string         `json:"finished"`
	Summary         string         `json:"summary"`
	Warnings        []string       `json:"warnings"`
	Counts          map[string]int `json:"counts"`
	Error           string         `json:"error"`
}
