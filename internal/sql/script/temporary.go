package sqlscript

import (
	"errors"
	"fmt"
	"strings"

	"github.com/xwb1989/sqlparser"
)

// validateTemporaryTable 严格校验会话临时表及其可选 SELECT，不修改执行原文。
// 参数 source：单条 CREATE TEMPORARY TABLE 原文，可能含注释及中文字符串。
// 参数 tokens：同一原文的有效词法单元，首部已确认为临时建表命令。
// 返回：首部、查询和库范围均合法时返回 nil；语法或跨库引用错误时拒绝预览。
func validateTemporaryTable(source string, tokens []SQLToken) error {
	// 仅擦除关键字所在原文位置，避免误替换注释、字段名或字符串中的同名内容。
	// 偏移按字节记录，前置中文注释不会改变后续 SELECT 的定位。
	end := tokens[1].end
	start := end - len("temporary")
	if start < 0 || !strings.EqualFold(source[start:end], "temporary") {
		// 被反引号引用的同名标识符不能替代语法关键字。
		return errors.New("临时表首部必须使用 CREATE TEMPORARY TABLE")
	}
	copySQL := source[:start] + strings.Repeat(" ", end-start) + source[end:]
	header, query := temporaryTableParts(copySQL, tokens)
	// 严格入口拒绝不完整定义，避免旧 Parse 吞掉 DDL 尾部错误后仍给出语法树。
	node, err := sqlparser.ParseStrictDDL(header)
	if err != nil {
		return fmt.Errorf("临时表建表定义解析失败：%w", err)
	}
	ddl, ok := node.(*sqlparser.DDL)
	// LIKE 等尚未展开的结构没有完整 TableSpec，不能当作本轮已支持的建表形式。
	if !ok || ddl.TableSpec == nil {
		return errors.New("临时表需要完整字段定义或 SELECT 查询")
	}
	// 目标库限定名和 REFERENCES 等尾部仍执行相同范围规则，不能借兼容分支跨库。
	// 首部与查询分别检查，查询中声明的别名不能为建表目标的库名前缀开脱。
	headerTokens, err := ReadSQLTokens(header)
	if err != nil {
		return err
	}
	if err = ValidateSQLScope(node, headerTokens); err != nil || query == "" {
		// 显式字段建表到此校验完成；只有真正的 CTAS 才需要再解析查询。
		return err
	}
	// 旧 DDL 解析会忽略 SELECT 尾部；把查询单独完整解析，保留别名及子查询范围检查。
	// 字符串内的分号仍由原拆分器处理，实际执行始终使用 SplitSQL 保存的原文。
	queryTokens, err := ReadSQLTokens(query)
	if err != nil {
		return err
	}
	queryNode, err := sqlparser.Parse(query)
	if err != nil {
		return fmt.Errorf("临时表 SELECT 解析失败：%w", err)
	}
	// 递归语法树检查所有真实表引用，外层安全不能掩盖子查询中的跨库访问。
	return ValidateSQLScope(queryNode, queryTokens)
}

// temporaryTableParts 从首层 SELECT 边界拆出校验用首部与查询，保留各部分原文。
// 参数 source：TEMPORARY 关键字已替换为空格的等长校验副本。
// 参数 tokens：原始词法单元及字节偏移，字符串和注释不参与关键字识别。
// 返回：校验用首部和 SELECT 原文；无字段 CTAS 首部补虚拟列，普通建表返回空查询。
func temporaryTableParts(source string, tokens []SQLToken) (string, string) {
	// 字段定义和表达式内的括号不能被误认成 CTAS 的查询分界。
	depth := 0
	hasColumns := false
	for i, token := range tokens {
		if token.Text == "(" {
			// 字符串和注释已被词法层排除，其括号不影响字段列表判断。
			hasColumns = true
			depth++
		} else if token.Text == ")" {
			// 字段类型精度或表达式的结束括号只恢复层级，不切断整个建表首部。
			depth--
		}
		if token.Kind != sqlparser.SELECT || depth != 0 {
			// 被反引号引用的 select 是列名，只有真正的顶层关键字才划分查询。
			continue
		}
		start := token.end - len("select")
		headerEnd := start
		// AS 为可选语法，只在紧邻顶层 SELECT 时从校验首部移除。
		// 不能对整段文本做 AS 替换，否则字段别名和字符串内容也会变化。
		if i > 0 && tokens[i-1].Kind == sqlparser.AS {
			headerEnd = tokens[i-1].end - len("as")
		}
		header := strings.TrimSpace(source[:headerEnd])
		// CTAS 未给字段时由 MySQL 推导类型，虚拟列仅补足旧解析器的首部语法。
		if !hasColumns {
			// 新起一行避免虚拟定义被首部末尾的单行注释吞掉；此副本永不执行。
			header += "\n (__rd_validation_column int)"
		}
		return header, source[start:]
	}
	// 没有顶层查询时保留完整定义，交由严格 DDL 解析报告缺项或未知尾部。
	return source, ""
}
