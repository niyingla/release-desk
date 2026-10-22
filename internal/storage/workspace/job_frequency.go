package workspace

import (
	"regexp"
	"strings"
)

// 这里只识别可分离的表达式边界；人类可读解释由离线前端组件派生。
// 星期、月份与 Quartz 特殊符号都保留，不把数字星期换成另一套编号。
var jobCronField = regexp.MustCompile(`(?i)^(?:[0-9*?,/#LW-]|JAN|FEB|MAR|APR|MAY|JUN|JUL|AUG|SEP|OCT|NOV|DEC|SUN|MON|TUE|WED|THU|FRI|SAT)+$`)

// NormalizeJobFrequency 将历史混合频率分为纯 Cron 和原有说明。
// 参数 data：定时任务字段字典，handler 已通过事项入口校验；允许未知频率留空。
// 返回：无返回值；仅原地调整 frequency、description，不推断或执行实际调度。
func NormalizeJobFrequency(data map[string]string) {
	// 旧文件可能只有“每天十点”等文字，不能猜测秒、时区或真实调度配置。
	// 所有未识别部分原样保留到描述，重复加载时不再追加。
	raw := strings.TrimSpace(data["frequency"])
	if raw == "" {
		return
	}
	expression, note := splitJobFrequency(raw)
	// 即使备注无法识别也不保留在可复制到调度平台的表达式字段中。
	data["frequency"] = expression
	if note == "" {
		return
	}
	// 说明与原描述分行，既保留来源提示，也避免把旧描述整体覆盖掉。
	description := strings.TrimSpace(data["description"])
	data["description"] = strings.TrimSpace(description + "\n原频率说明：" + note)
}

// splitJobFrequency 识别开头的 5、6 或 7 段 Cron，分离其后的自由文本。
// 参数 raw：已去除首尾空白的旧频率字符串，可能紧接中文括号或换行备注。
// 返回：依次返回空白规范化的表达式和保留的备注；无法安全分离时原文全部作为备注。
func splitJobFrequency(raw string) (string, string) {
	// 括号和换行属于说明边界，不属于 Cron 字段，允许无空格紧接表达式。
	prefix, suffix := raw, ""
	if index := strings.IndexAny(raw, "(（\r\n"); index >= 0 {
		prefix, suffix = raw[:index], raw[index:]
	}
	// 只规范化表达式字段间的空白，说明正文另行保留。
	fields := strings.Fields(prefix)
	count := 0
	for _, field := range fields {
		// 必须匹配完整字段，不能把“10点”截成合法的数字 10。
		if !jobCronField.MatchString(field) {
			break
		}
		count++
	}
	// 多出的纯 Cron 字段不能擅自当作备注截掉，否则可能改变原表达式含义。
	// 5 段用于标准 Cron，6/7 段用于包含秒及可选年份的 Quartz。
	if count < 5 || count > 7 {
		return "", raw
	}
	if count < len(fields) {
		// 从原文定位余下文字，保留备注内部空白和标点，不重新拼接其正文。
		// 备注可能含连续空格，不能用剩余 fields 重组。
		remaining := prefix
		for _, field := range fields[:count] {
			remaining = strings.TrimLeft(remaining, " \t")
			remaining = strings.TrimPrefix(remaining, field)
		}
		suffix = remaining + suffix
	}
	// 本层只做兼容分离，前端解释失败时明确提示用户修正，不猜测有效调度。
	return strings.Join(fields[:count], " "), strings.TrimSpace(suffix)
}
