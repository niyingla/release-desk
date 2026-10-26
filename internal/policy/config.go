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
	// 只保存少量可公开核验摘要，异常工具流不能撑大本机任务记录。
	MaxVerificationProgress = 30
	MaxProgressSummaryRunes = 600
	MaxProgressEventBytes   = 64 * 1024
	// 用户当前日志所在区域的初始域名；其他区域可在本机设置中修改。
	DefaultVerificationLogDomain  = "tls-cn-guangzhou.volces.com"
	MaxVerificationLogDomains     = 16
	VerificationProbeSeconds      = 12
	VerificationProbeCacheSeconds = 30
	UpdateTimeoutSeconds          = 12
	UpdateIntervalHours           = 6
	MaxUpdateResponseBytes        = 1024 * 1024
)
