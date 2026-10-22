package schema

import (
	"fmt"
	"strings"

	"github.com/xwb1989/sqlparser"
)

// positionedDefinitionChecks 将列定义与末尾 FIRST/AFTER 分别解析。
// 参数 table：实际目标表名；参数 body：移除 ADD/MODIFY/CHANGE 与旧列名后的定义单元。
// 返回：完整字段及位置约束；位置不完整、用于索引或指向自身时拒绝自动核验。
func positionedDefinitionChecks(table string, body []token) ([]Check, bool) {
	// 只识别定义末尾的语法，COMMENT、DEFAULT 和枚举字符串中的文字不参与判断。
	end := len(body)
	var position *ColumnPlacement
	if end > 1 && body[end-2].kind != sqlparser.STRING && lower(body[end-2]) == "after" {
		// AFTER 后只能是列标识符，不能把字符串或缺失目标当作有效位置。
		if body[end-1].kind != sqlparser.ID {
			return nil, false
		}
		position, end = &ColumnPlacement{After: body[end-1].text}, end-2
	} else if end > 0 && body[end-1].kind != sqlparser.STRING && lower(body[end-1]) == "first" {
		// 优先匹配 AFTER，参照列可以恰好命名为 first。
		position, end = &ColumnPlacement{First: true}, end-1
	}
	checks, ok := definitionChecks(table, joinTokens(body[:end]))
	// 先取得完整字段定义，避免只解析了位置却忽略尾部未知属性。
	if !ok || position == nil {
		return checks, ok
	}
	// 索引没有字段物理位置，只有单个列定义能够附带顺序约束。
	if len(checks) != 1 || checks[0].Kind != "column" || strings.EqualFold(checks[0].Name, position.After) {
		return nil, false
	}
	// 位置与类型分开保存，后续 MODIFY 不声明位置时仍保留原位置要求。
	// SQL 重组由独立方法处理，原始关键字不会被当作列名引用。
	return append(checks, Check{Table: table, Kind: "position", Name: checks[0].Name, Position: position}), true
}

// positionedClauseSQL 修复旧词法器将 FIRST/AFTER 识别为普通标识符的情况。
// 参数 tokens：完整原子句单元；参数 checks：已经完整解析的位置与定义约束。
// 返回：可发送的等价子句；只替换已经确认的位置关键字，列名继续安全引用。
func positionedClauseSQL(tokens []token, checks []Check) string {
	// 索引或普通定义没有位置，不能全局替换同名字段/字符串中的文字。
	for _, check := range checks {
		if check.Kind != "position" {
			continue
		}
		parts := append([]token{}, tokens...)
		// 原始词法单元供其他核验使用，只在副本修改输出关键字。
		// FIRST 在末尾，AFTER 后面还保留一个经过引用的目标列名。
		index, keyword := len(parts)-1, "FIRST"
		if !check.Position.First {
			index, keyword = len(parts)-2, "AFTER"
		}
		parts[index].raw = keyword
		// 参照列 token 未修改，即使列名恰好是 first/after 也仍作为标识符。
		return joinTokens(parts)
	}
	// 普通字段、索引和字符串全部沿用现有安全引用规则。
	return joinTokens(tokens)
}

// finalColumnPositions 按作者顺序归并位置操作，保留最终仍成立的相邻约束。
// 参数 checks：包含定义、删除、重命名及位置操作的顺序检查列表。
// 返回：独立的位置约束；缺少原始前驱的复杂重排显式保留待核验状态。
func finalColumnPositions(checks []Check) []Check {
	// 相同 AFTER 或多次 FIRST 是插入操作，后插入字段会改变先前字段的直接前驱。
	result := []Check{}
	for _, check := range checks {
		if check.Kind == "column" && check.RenameFrom != "" {
			// 名称比较按列名语义忽略大小写，输出仍保留作者使用的名称。
			// CHANGE 的新列名替换所有位置引用，位置本身默认保持不变。
			for index := range result {
				// 表名保持原样；不同表可以存在相同字段名，不能交叉修改参照。
				entry := &result[index]
				if entry.Table != check.Table {
					continue
				}
				if strings.EqualFold(entry.Name, check.RenameFrom) {
					entry.Name = check.Name
				}
				// 字段自身与其他字段指向它的 AFTER 引用要同时更新。
				if strings.EqualFold(entry.Position.After, check.RenameFrom) {
					entry.Position.After = check.Name
				}
			}
		}
		if check.Kind == "column" && check.Absent {
			// 删除中间列也会改变相邻关系，不能留下已不存在的参照列。
			result = detachColumnPosition(result, check.Table, check.Name)
		}
		if check.Kind != "position" {
			// 类型、默认值等普通定义不改变字段位置。
			continue
		}
		// 先从旧位置移除再插入新位置，不能让依赖方错误地随被移动的列一起移动。
		result = detachColumnPosition(result, check.Table, check.Name)
		for index := range result {
			// 后插入相同位置的字段成为原有字段的新直接前驱。
			entry := &result[index]
			if entry.Table == check.Table && !entry.Position.Unverified && entry.Position.First == check.Position.First && strings.EqualFold(entry.Position.After, check.Position.After) {
				entry.Position = &ColumnPlacement{After: check.Name}
			}
		}
		// 拷贝指针值，最终约束的归并不能改写原子句的执行前检查。
		position := *check.Position
		check.Position = &position
		result = append(result, check)
	}
	// 输出顺序稳定，差异提示不会随 map 遍历顺序随机变化。
	return result
}

// detachColumnPosition 删除某列旧位置，同时恢复依赖它的已知前驱关系。
// 参数 positions：本表及其他表的已知位置；参数 table：目标表；参数 name：被移动或删除的字段。
// 返回：移除目标后的约束列表；未知的原始位置不猜测为 FIRST 或任何字段。
func detachColumnPosition(positions []Check, table, name string) []Check {
	// 历史只读核验没有执行前快照，不能凭当前结构推测已移动参照列的旧前驱。
	var previous *ColumnPlacement
	for _, check := range positions {
		// 已知的上一次位置是恢复后继位置的唯一依据，不使用当前表倒推。
		if check.Table == table && strings.EqualFold(check.Name, name) {
			previous = check.Position
			break
		}
	}
	result := make([]Check, 0, len(positions))
	// 为每次归并返回独立切片，调用者仍可保留原始子句检查列表。
	// 多表约束共用列表，只有目标表和目标列的引用可以变化。
	for _, check := range positions {
		if check.Table == table && strings.EqualFold(check.Name, name) {
			// 目标旧位置本身被撤销，只有后续新位置可以重新建立约束。
			continue
		}
		if check.Table == table && strings.EqualFold(check.Position.After, name) {
			// 已知位置可恢复；无法还原时保持不确定，不能给出绿色通过。
			position := ColumnPlacement{Unverified: true}
			if previous != nil {
				position = *previous
			}
			check.Position = &position
		}
		result = append(result, check)
	}
	// 无依赖字段时只移除目标，不凭空为其他字段增加位置要求。
	return result
}

// compareColumnPosition 对照真实 ORDINAL_POSITION 核对 FIRST 或紧邻 AFTER。
// 参数 check：已经归并的最终位置约束；参数 actual：同库同表的实时元数据。
// 返回：差异文案及是否具备完整核验信息；缺失位置元数据不当作结构失败或成功。
func compareColumnPosition(check Check, actual Table) (string, bool) {
	// 字段定义与顺序分别核验，名称存在不足以证明位于正确位置。
	column, exists := actual.Columns[strings.ToLower(check.Name)]
	name := check.Table + "." + check.Name
	// 缺列是明确差异；有列但缺少位置信息才属于无法核验。
	if !exists {
		return name + "：字段不存在", true
	}
	if check.Position == nil || check.Position.Unverified {
		// 复杂依赖重排缺少初始前驱时保留人工核对，避免误报顺序一致。
		return name + "：参照字段已移动或删除，缺少原始顺序，需人工核对", false
	}
	if column.Ordinal < 1 {
		// 真实元数据的序号从 1 开始，零值不能作为合法位置参与比较。
		return name + "：未取得 ORDINAL_POSITION", false
	}
	if check.Position.First {
		// FIRST 只接受第一列，不受其他字段名称排序影响。
		if column.Ordinal != 1 {
			return fmt.Sprintf("%s：应为 FIRST，实际第 %d 列", name, column.Ordinal), true
		}
		return "", true
	}
	// AFTER 要求直接相邻，不能仅凭目标列排在参照列之后就算通过。
	anchor, exists := actual.Columns[strings.ToLower(check.Position.After)]
	if !exists {
		// 参照不存在是结构缺失，不能只看目标列存在就放行。
		return name + "：AFTER 参照字段不存在：" + check.Position.After, true
	}
	if anchor.Ordinal < 1 {
		// 参照位置同样必须可靠，不能把未知零值当作第一列的前驱。
		return name + "：未取得参照字段 ORDINAL_POSITION", false
	}
	if column.Ordinal != anchor.Ordinal+1 {
		return name + "：未紧邻 AFTER " + check.Position.After, true
	}
	// 两列真实序号严格相邻，才满足 AFTER 的语义。
	return "", true
}
