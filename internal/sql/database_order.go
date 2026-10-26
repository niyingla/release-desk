package sqlservice

import "strings"

// databaseNameLess 按同前缀库名的数字尾号比较，模板库排在所属前缀最前。
// 参数 left：左侧实际数据库名称，尾号允许超过固定宽度整数范围。
// 参数 right：右侧待比较的实际数据库名称，必须保持真实拼写。
// 返回：左侧应排前为 true；不同前缀按名称排列，相等数字以原名稳定兜底。
func databaseNameLess(left, right string) bool {
	// 拆出完整尾号，避免 sp10 被字典序排到 sp2 前面。
	lp, ln := databaseNumber(left)
	rp, rn := databaseNumber(right)
	// 独立租户和共享库保持各自前缀分组，不把不同业务命名混成一组尾号。
	if lp != rp {
		return lp < rp
	}
	// 无尾号表示模板库；零尾号是独立名字，仍排在模板之后。
	if ln == "" || rn == "" {
		return ln < rn
	}
	// 数字长度再按文本比较，不受 int64 溢出或大编号精度限制。
	if len(ln) != len(rn) {
		return len(ln) < len(rn)
	}
	if ln != rn {
		return ln < rn
	}
	// 前导零相同数值时仍有确定顺序，重复刷新不会抖动。
	return left < right
}

// databaseNumber 分离库名前缀和无多余前导零的数字尾号。
// 参数 name：实际数据库名称；允许模板及共享库前缀，不推测所属业务端。
// 返回：非数字前缀及规范尾号；没有尾号时返回空数字，纯零规范为 "0"。
func databaseNumber(name string) (string, string) {
	index := len(name)
	// 仅识别 ASCII 尾号，前缀中的下划线和其他字符保持原名。
	for index > 0 && name[index-1] >= '0' && name[index-1] <= '9' {
		index--
	}
	number := strings.TrimLeft(name[index:], "0")
	// 有尾号但全为零与无尾号模板必须区分，不能同时化成空值。
	if index < len(name) && number == "" {
		number = "0"
	}
	// 前缀不做大小写或下划线改写，展示和实际执行继续使用真实库名。
	return name[:index], number
}
