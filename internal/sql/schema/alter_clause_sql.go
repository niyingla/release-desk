package schema

import "strings"

// alterClauseSQL 按已解析的语法位置重组 ALTER 子句，修正旧词法器对非保留关键字的分类。
// 参数 tokens：完整原子句单元，首项为动作，其余标识符和字符串保持安全引用。
// 参数 checks：已解析的定义及位置约束；为空时不推测 FIRST/AFTER 的含义。
// 返回：可发送的等价子句；只还原动作与已确认位置的关键字，不全局替换同名字段或值。
func alterClauseSQL(tokens []token, checks []Check) string {
	// 缺失子句仍交给上层拒绝，不能为了修正动作而访问空单元列表。
	if len(tokens) == 0 {
		return ""
	}
	// 复制后再调整输出，解析与核验共享的原单元不能被格式化副作用改变。
	parts := append([]token{}, tokens...)
	// MODIFY 在旧词法器里是 ID，但在动作位置必须作为语法关键字输出。
	// 只处理已支持的动作；后面的 `modify` 列名仍保留反引号。
	switch lower(parts[0]) {
	case "add", "modify", "change", "drop":
		parts[0].raw = strings.ToUpper(parts[0].text)
	}
	for _, check := range checks {
		// 只有已解析的位置约束才能改变 FIRST/AFTER，不能按文字全局替换。
		if check.Kind != "position" {
			continue
		}
		index, keyword := len(parts)-1, "FIRST"
		if !check.Position.First {
			index, keyword = len(parts)-2, "AFTER"
		}
		// 参照列未修改，即使名称恰好为 first/after，也仍作为标识符输出。
		parts[index].raw = keyword
	}
	// 字符串、默认值、字段定义以及子句内部顺序沿用原有安全重组规则。
	return joinTokens(parts)
}
