package policy

// 具有明确业务单位的上限统一命名，避免在执行流程中散布魔数。
const (
	MaxVersionFileBytes    = 15 * 1024 * 1024
	GitTimeoutSeconds      = 90
	PlanExpiryMinutes      = 5
	DatabaseConnectSeconds = 8
	DatabaseReadSeconds    = 90
	DatabaseWriteSeconds   = 30
	DatabaseScriptMinutes  = 10
	CodexTimeoutMinutes    = 15
	// 用户指定批量验证最多六项同时取证，扫描与 SQL 仍各自保持原有执行策略。
	VerificationConcurrency = 6
	UpdateTimeoutSeconds    = 12
	UpdateIntervalHours     = 6
	MaxUpdateResponseBytes  = 1024 * 1024
)
