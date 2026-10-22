package sqlscript

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/xwb1989/sqlparser"
)

type SQLToken struct {
	Kind int
	Text string
}

// SplitSQL 拆分并校验库内 SQL 脚本。
// 参数 source：完整 SQL 原文；保留字面量、中文和换行。
// 返回：返回保持原文的有序合法语句列表；任一语法、操作类型或范围违规时返回错误。
func SplitSQL(source string) ([]string, error) {
	// 拆分结果保留用户原始语句，避免格式化改变语义。
	// 字符串中的分号不能切成多条语句。
	// 注释本身不算可执行业务语句。
	// 每条语句都要通过相同范围校验。
	// 一条不支持的语句会使整次预览失败。

	// MySQL 可执行注释会让服务端执行解析器忽略的内容，因此不接受此写法。

	if strings.Contains(source, "/*!") {
		return nil, errors.New("暂不支持 MySQL 可执行注释，请拆成普通 SQL")
	}
	parts, err := sqlparser.SplitStatementToPieces(source)

	if err != nil {
		return nil, fmt.Errorf("SQL 拆分失败：%w", err)
	}

	// 分号可以出现在字符串与注释中，使用成熟的词法分析器避免手写状态机漂移。
	// 每条语句单独执行，不启用驱动的 multiStatements。

	result := []string{}
	for _, part := range parts {
		tokens, err := ReadSQLTokens(part)
		if err != nil {
			return nil, err
		}
		if len(tokens) == 0 {
			continue
		}
		if err = ValidateSQLStatement(part, tokens); err != nil {
			return nil, err
		}
		result = append(result, strings.TrimSpace(part))
	}

	if len(result) == 0 {
		return nil, errors.New("没有可执行 SQL")
	}
	return result, nil
}

// ReadSQLTokens 读取排除字符串与注释的 SQL 词法单元。
// 参数 source：完整 SQL 原文；保留字面量、中文和换行。
// 返回：返回排除注释与字符串后的词法列表；词法错误时返回错误。
func ReadSQLTokens(source string) ([]SQLToken, error) {
	// 词法处理忽略注释和字符串数据。
	// 不能把说明文字里的 USE 误认为命令。
	// 标点保留以识别真正的限定符。
	// 数字小数点不是数据库名前缀。
	// 中文字符串内容不参与范围匹配。

	// 字符串值与注释只作为数据，不能误判其中的 USE 或库名前缀。

	lexer := sqlparser.NewStringTokenizer(source)

	lexer.AllowComments = true
	tokens := []SQLToken{}

	for {
		kind, value := lexer.Scan()
		if kind == 0 {
			break
		}
		if kind == sqlparser.COMMENT || kind == sqlparser.STRING {
			continue
		}

		// 标点直接保留其字符值，识别限定符和会话变量时不依赖空白布局。
		text := string(value)
		if text == "" && kind < 256 {
			text = string(rune(kind))
		}
		tokens = append(tokens, SQLToken{Kind: kind, Text: strings.ToLower(text)})
	}

	if lexer.LastError != nil {
		return nil, fmt.Errorf("SQL 词法分析失败：%w", lexer.LastError)
	}
	return tokens, nil
}

// ValidateSQLStatement 校验单条 SQL 的允许操作类型。
// 参数 source：完整 SQL 原文；保留字面量、中文和换行。
// 参数 tokens：已去掉字符串与注释内容的词法单元，用于判断命令与限定符。
// 返回：允许的库内语句返回 nil；操作类型、事务控制或语法无效时返回错误。
func ValidateSQLStatement(source string, tokens []SQLToken) error {
	// 脚本不允许自行选择数据库。
	// 白名单只覆盖普通库内表结构和数据变更。
	// 数据库级、账号级与存储程序操作明确拒绝。

	// 批量执行只接受常规表结构、数据和本连接会话设置，数据库选择由工具统一负责。
	allowed := []string{"select", "insert", "update", "delete", "create", "alter", "drop", "truncate", "set", "replace"}
	first := tokens[0].Text

	if !slices.Contains(allowed, first) {
		return errors.New("不支持切库、动态 SQL、存储过程或自行控制事务")
	}
	if len(tokens) > 1 && slices.Contains([]string{"create", "alter", "drop"}, first) && slices.Contains([]string{"database", "schema", "user", "role", "procedure", "function", "trigger", "event"}, tokens[1].Text) {
		return errors.New("仅允许目标库内表结构与数据变更")
	}

	// 连接由工具管理事务；修改 autocommit 或全局变量会破坏逐库执行语义。
	// 字面量已剔除，因此说明文字中的这些词不会影响正常业务脚本。
	if first == "set" {
		for _, token := range tokens {
			if slices.Contains([]string{"global", "persist", "password", "autocommit", "@"}, token.Text) || strings.HasPrefix(token.Text, "@@") {
				return errors.New("禁止修改全局设置、账号或事务提交方式")
			}
		}
		// 服务商初始化脚本使用 SET @tenant := ... DATABASE()，按用户变量规则验证，不改写执行原文。
		if len(tokens) > 2 && strings.HasPrefix(tokens[1].Text, "@") {
			return ValidateUserVariable(tokens)
		}
	}

	// 旧解析器仅接受 SET 的等号形式；仅归一化校验副本，实际执行仍保留原始 := 语句。
	node, err := sqlparser.Parse(strings.ReplaceAll(source, ":=", "="))
	if err != nil {
		return fmt.Errorf("SQL 解析失败，暂不支持此扩展语法：%w", err)
	}
	return ValidateSQLScope(node, tokens)
}

// ValidateUserVariable 检查普通连接内用户变量赋值，兼容旧解析器不支持的 DATABASE() 表达式。
// 参数 tokens：已经由词法器完成拆分的单条 SET 语句，目标必须是单个 @ 前缀变量。
// 返回：合法的连接内赋值返回 nil；服务器变量、跨库限定名或不完整赋值返回错误。
func ValidateUserVariable(tokens []SQLToken) error {
	// 用户变量只属于当前单库连接，不允许通过 @@ 访问服务器级变量。
	// 每个目标使用独立连接，上一租户设置的变量不会泄漏到下一租户。
	if strings.HasPrefix(tokens[1].Text, "@@") || (tokens[2].Text != "=" && tokens[2].Text != ":") {
		return errors.New("用户变量必须是 SET @name = 表达式或 := 表达式")
	}
	// 不解析表达式值来生成新 SQL，原文仍交给 MySQL 按单条语句执行。
	// 词法层继续禁止库名前缀，不能利用兼容分支绕过作用范围检查。
	for index := 0; index+1 < len(tokens); index++ {
		if tokens[index].Kind == sqlparser.ID && tokens[index+1].Text == "." {
			return errors.New("用户变量表达式不能包含数据库限定名")
		}
	}
	return nil
}

// ValidateSQLScope 校验表引用不会越过当前目标数据库。
// 参数 node：SQL 解析得到的语法树，用于区分跨库表与普通字段别名。
// 参数 tokens：已去掉字符串与注释内容的词法单元，用于判断命令与限定符。
// 返回：无跨库引用返回 nil；显式数据库限定名或未声明限定符返回错误。
func ValidateSQLScope(node sqlparser.Statement, tokens []SQLToken) error {
	// AST 表节点与字段限定符分别检查。
	// 明确的跨库表即使名称符合租户规则也不放行。
	// 批量目标选择只属于执行器。
	// 已声明表别名可以用于字段引用。
	// DDL 尾部还要做词法补充检查。

	aliases := map[string]bool{}
	// AST 检查真实表节点，区分别名字段 a.id 与跨库表 db.table。

	err := sqlparser.Walk(func(n sqlparser.SQLNode) (bool, error) {
		switch value := n.(type) {
		case sqlparser.TableName:
			if !value.Qualifier.IsEmpty() {
				return false, errors.New("请移除数据库名前缀，批量执行会自动选择目标库")
			}
		case *sqlparser.AliasedTableExpr:
			if !value.As.IsEmpty() {
				aliases[strings.ToLower(value.As.String())] = true
			}
		}
		return true, nil
	}, node)

	if err != nil {
		return err
	}

	// 兼顾解析器未展开的 DDL 尾部（例如 REFERENCES other.table）。
	// 只放行前面已明确声明的表别名，未声明的限定名不能改变目标数据库。

	for i := 0; i+1 < len(tokens); i++ {
		token := tokens[i]
		if token.Kind == sqlparser.ID && tokens[i+1].Text == "." && !aliases[token.Text] {
			return errors.New("发现数据库限定名或未声明别名：" + token.Text + "；请使用库内 SQL")
		}
	}
	return nil
}
