package workspace

// SQLEnvironment 定义允许执行脚本的环境；显示名称与持久化标识分离。
type SQLEnvironment struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

var SQLEnvironments = []SQLEnvironment{{ID: "test", Label: "测试环境"}, {ID: "uat", Label: "UAT 环境"}}

// EffectiveSQLEnvironment 兼容升级前未写环境的执行记录。
// 参数 value：记录中的环境标识；仅空值按历史协议解释为 test，未知非空值原样保留。
// 返回：有效历史标识，不将未知环境自动映射成测试或 UAT。
func EffectiveSQLEnvironment(value string) string {
	// 旧版只允许测试执行，因此缺失字段具有明确的兼容含义。
	if value == "" {
		return "test"
	}
	// 未知非空历史保留原值，不误标成已有环境。
	return value
}

// LookupSQLEnvironment 查找受支持环境的展示信息。
// 参数 value：环境标识；允许空值兼容旧客户端，未知非空值不允许执行。
// 返回：环境元数据及是否支持；不存在时不回退到其他环境。
func LookupSQLEnvironment(value string) (SQLEnvironment, bool) {
	// 执行环境属于安全边界，必须明确匹配产品支持的目标。
	value = EffectiveSQLEnvironment(value)
	for _, environment := range SQLEnvironments {
		// 显示名称变化不影响旧记录使用的稳定 ID。
		if environment.ID == value {
			return environment, true
		}
	}
	// 未知标识可保留在历史，但不能用于建立数据库连接。
	return SQLEnvironment{}, false
}
