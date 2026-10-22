package schema

import (
	"fmt"
	"slices"
	"strings"

	"github.com/xwb1989/sqlparser"
)

type token struct {
	kind      int
	text, raw string
}

// Parse 从真实语句解析目标表及支持核验的结构约束。
// 参数 source：已通过执行范围校验的单条 SQL 原文，不读取目录或文件名。
// 返回：可核验计划；无法完整理解的 DDL 显式标为不支持自动核验。
func Parse(source string) Operation {
	// 分类依据首部词法命令，说明文本和字符串不会成为 DDL。
	// 只支持可以完整表达目标结构的语法，不提供部分成功的核验标签。
	// CREATE TABLE 必须取得完整 TableSpec，LIKE 或 AS SELECT 不能猜测字段。
	// 表名取自语法树，保留大小写，不依赖文件名称。
	// 建表约束同时覆盖显式字段、索引和表级选项。
	// 字段默认值与注释通过解析器解码，不做正则切割。
	// ALTER 的顶层逗号与类型精度、索引内部逗号分别处理。
	// 所有 ALTER 子句都支持时才允许省略已有对象。
	// 一个未知子句会使整条语句退出自动重写通道。
	// CREATE INDEX 使用同义 ALTER ADD 形式完成对象定位和检查。
	// 独立索引必须找到真实 ON 目标，不把索引名称当成表名。
	// 返回计划保留完整原始语句，未知操作仍按原文处理。
	// 跨库目标已经由执行范围校验拦截，解析器不负责改变目标连接。
	// 扩展约束涉及额外元数据时必须明确提示人工核验。
	// 临时表不在普通 information_schema 中，不能用常规表检查冒充核验。
	// 非结构语句继续保持数据执行语义，不根据字段值中的 CREATE 等字样分类。
	tokens := readTokens(source)
	op := Operation{SQL: source, Supported: true}
	if len(tokens) == 0 || !slices.Contains([]string{"create", "alter", "drop", "rename"}, lower(tokens[0])) {
		return op
	}
	op.Structural = true
	// 扩展约束不能只检查其中一部分后声称全部正确。
	if unsupported(tokens) {
		return unknown(op, "包含暂不支持完整核验的结构选项")
	}
	if len(tokens) > 2 && lower(tokens[0]) == "create" && lower(tokens[1]) == "table" {
		node, err := sqlparser.ParseStrictDDL(source)
		if err != nil {
			return unknown(op, err.Error())
		}
		ddl, ok := node.(*sqlparser.DDL)
		if !ok || ddl.TableSpec == nil {
			return unknown(op, "建表语句缺少完整字段定义")
		}
		op.Table = ddl.NewName.Name.String()
		if op.Table == "" {
			op.Table = ddl.Table.Name.String()
		}
		op.Create = true
		checks, ok := tableSpecChecks(op.Table, ddl.TableSpec)
		if !ok {
			return unknown(op, "内联主键或扩展字段定义需人工核对")
		}
		op.Clauses = []Clause{{SQL: source, Checks: checks, SkipExisting: true}}
		// 表级显式引擎、字符集和注释也属于目标结构，不能只验证表名存在。
		options, supported := tableOptions(readTokens(ddl.TableSpec.Options))
		// 未支持的表级选项不允许被忽略后宣称整张表核验通过。
		if !supported {
			return unknown(op, "表选项包含暂未支持的结构属性")
		}
		op.Clauses[0].Checks[0].Options = &options
		return op
	}
	if len(tokens) > 3 && lower(tokens[0]) == "alter" && lower(tokens[1]) == "table" {
		op.Table = tokens[2].text
		for _, part := range splitClauses(tokens[3:]) {
			clause, ok := alterClause(op.Table, part)
			if !ok {
				return unknown(op, "ALTER 含暂不支持完整核验的子句")
			}
			op.Clauses = append(op.Clauses, clause)
		}
		// 多个 ALTER 子句独立保留，可只执行仍缺失的部分，避免重复字段阻塞后续修改。
		if len(op.Clauses) == 0 {
			return unknown(op, "ALTER 没有可核验子句")
		}
		return op
	}
	// 独立 CREATE INDEX 转为同义 ALTER ADD，只使用完整解析后的定义。
	if len(tokens) > 5 && lower(tokens[0]) == "create" {
		for i, part := range tokens {
			if lower(part) == "on" && i+2 < len(tokens) {
				op.Table = tokens[i+1].text
				definition := joinTokens(append(append([]token{}, tokens[1:i]...), tokens[i+2:]...))
				checks, ok := definitionChecks(op.Table, definition)
				if ok {
					op.Clauses = []Clause{{SQL: "ADD " + definition, Checks: checks, SkipExisting: true}}
					return op
				}
				break
			}
		}
	}
	return unknown(op, "该结构语句暂不支持自动核验")
}

// alterClause 将常规字段或索引操作转换为目标约束。
// 参数 table：语句真实目标表名；参数 tokens：一个顶层 ALTER 子句的词法单元。
// 返回：完整约束及支持标记，任何遗漏都必须回退到待核验。
func alterClause(table string, tokens []token) (Clause, bool) {
	// ADD 和 MODIFY 对字段的处理不同，不能在字段存在时都直接跳过。
	// CHANGE 同时约束新定义与旧名消失，保留重命名的真实含义。
	// COLUMN 关键字可选，省略时仍需完整对象名称与定义。
	// FIRST 与 AFTER 单独形成位置约束，定义与最终顺序都必须核验。
	// 新增索引与新增字段通过同一完整定义解析入口识别。
	// ADD 的同名对象存在性仅用于省略重复子句。
	// DROP COLUMN 的目标是字段不存在，不能与新增约束共用比较结果。
	// DROP INDEX 与 DROP KEY 都定位索引，不误当成字段。
	// 主键使用固定索引身份，与普通同名字段分开处理。
	// IF EXISTS 等扩展没有专门解析前，不将关键字当成对象名。
	// 缺少对象名称时拒绝自动核验，不能从上一条语句继承。
	// 删除字段没有类型定义，检查只覆盖明确的不存在目标。
	// 任何尚未支持的动作返回 false，由上层保留人工核验边界。
	// 多个检查仍属于同一个原始 ALTER 子句，不随意改变 SQL 作者顺序。
	// 同名 CHANGE 不要求字段消失，仍须验证新字段定义。
	// 解析器返回的字段定义可能还包含内联键，需要完整保留。
	// 重命名比较只归一列名大小写，不更改原始 SQL 文本。
	// 返回的 SQL 片段只来自词法单元，字符串与标识符都已正确引用。
	// 空子句不能当成成功，不允许仅凭 ADD 或 MODIFY 关键词猜测对象。
	clause := Clause{SQL: joinTokens(tokens)}
	if len(tokens) < 2 {
		return clause, false
	}
	action := lower(tokens[0])
	body := tokens[1:]
	// CHANGE 同时保留新定义与旧名消失的约束，不能只检查新字段名。
	if action == "change" {
		if lower(body[0]) == "column" {
			body = body[1:]
		}
		if len(body) < 3 {
			return clause, false
		}
		old := body[0].text
		checks, ok := positionedDefinitionChecks(table, body[1:])
		if !ok {
			return clause, false
		}
		for index, check := range checks {
			if check.Kind == "column" && !strings.EqualFold(check.Name, old) {
				// 重命名同步调整位置约束里的参照列，不把旧列名留在最终目标中。
				checks[index].RenameFrom = old
				checks = append(checks, Check{Table: table, Kind: "column", Name: old, Absent: true})
				break
			}
		}
		clause.Checks = checks
		clause.SQL = positionedClauseSQL(tokens, checks)
		return clause, true
	}
	if slices.Contains([]string{"add", "modify"}, action) {
		if lower(body[0]) == "column" {
			body = body[1:]
		}
		if len(body) == 0 {
			return clause, false
		}
		checks, ok := positionedDefinitionChecks(table, body)
		clause.Checks = checks
		clause.SkipExisting = action == "add"
		clause.SQL = positionedClauseSQL(tokens, checks)
		return clause, ok
	}
	if action == "drop" {
		kind, name := "column", ""
		if lower(body[0]) == "column" {
			body = body[1:]
		} else if slices.Contains([]string{"index", "key"}, lower(body[0])) {
			kind = "index"
			body = body[1:]
		} else if lower(body[0]) == "primary" {
			kind = "index"
			name = "primary"
			body = nil
		}
		// IF EXISTS 和其他扩展子句需要单独解析，不把关键字误认成对象名称。
		if name == "" {
			if len(body) != 1 {
				return clause, false
			}
			name = body[0].text
		}
		clause.Checks = []Check{{Table: table, Kind: kind, Name: name, Absent: true}}
		return clause, true
	}
	return clause, false
}

// definitionChecks 使用同一 MySQL 解析器理解列定义或索引定义。
// 参数 table：实际表名；参数 definition：单个 ADD/MODIFY 的定义，保留字符串语义。
// 返回：字段或索引约束，无法完全解析时返回 false。
func definitionChecks(table, definition string) ([]Check, bool) {
	// 合成 CREATE 文本只用于复用字段语法分析，不产生任何数据库副作用。
	// 索引定义需要前置虚拟列才能被旧解析器完整识别。
	// 虚拟列检查会在返回前移除，不可能作为真实目标保存。
	// 只有完整 TableSpec 才能参与核验，部分语法树视为不支持。
	// 真实表存在性由外层操作处理，合成表标识不会进入最终约束。
	// 临时文本只用于解析，不发送给数据库，不创建探测表。
	definitionTokens := readTokens(definition)
	indexOnly := len(definitionTokens) > 0 && slices.Contains([]string{"primary", "unique", "index", "key"}, lower(definitionTokens[0]))
	probe := definition
	// 解析器要求先出现列定义；该虚拟列只存在于解析文本中，永不发送数据库。
	if indexOnly {
		probe = "__rd_column int, " + definition
	}
	node, err := sqlparser.ParseStrictDDL("CREATE TABLE __rd_probe (" + probe + ")")
	if err != nil {
		return nil, false
	}
	ddl, ok := node.(*sqlparser.DDL)
	if !ok || ddl.TableSpec == nil {
		return nil, false
	}
	checks, ok := tableSpecChecks(table, ddl.TableSpec)
	// 合成表的存在性不是 ALTER 目标，不把它写入真实检查列表。
	if !ok || len(checks) < 2 {
		return nil, false
	}
	if indexOnly {
		checks = append(checks[:1], checks[2:]...)
	}
	return checks[1:], true
}

// tableSpecChecks 提取字段与索引的完整显式定义。
// 参数 table：真实目标表名；参数 spec：解析器返回的完整建表定义。
// 返回：表、字段与索引约束；无法处理的内联键返回 false。
func tableSpecChecks(table string, spec *sqlparser.TableSpec) ([]Check, bool) {
	// 表存在性与字段定义各自保留，已有表仍必须核对目标字段。
	// 表级主键会隐式使成员列不可空，需要在预期结构中体现。
	// 内联主键也有相同语义，不能只检查列类型。
	// 内联唯一键同时产生索引约束，不能丢失唯一性。
	// 不支持的内联键类型不能被忽略后当成普通字段通过。
	// 列名大小写按 MySQL 语义定位，默认值仍保持精确。
	// 索引名称为空时无法稳定定位，不猜测服务器自动命名规则。
	// 主键统一定位为 PRIMARY，与普通索引的业务名称分开。
	// 复合索引的顺序是定义的一部分，不能排序后比较。
	// 前缀长度影响索引容量与查询行为，必须保留。
	// 唯一性由解析器标志决定，不能从 uk 等名称前缀猜测。
	// 默认普通索引方法为 BTREE，显式 USING 仍覆盖该预期。
	// 未知索引选项需要额外元数据，当前不作部分核验。
	// 所有列与索引构建完后才返回，避免上层消费半份定义。
	// 每份字段定义使用独立对象，后续列不会覆盖前一列的类型。
	// 同一个列既可参与主键又参与其他索引，检查需要分别保存。
	// 首项表存在性供创建前检测，后续字段仍必须逐项核对。
	checks := []Check{{Table: table, Kind: "table"}}
	primary := map[string]bool{}
	for _, idx := range spec.Indexes {
		if idx.Info.Primary {
			for _, col := range idx.Columns {
				primary[strings.ToLower(col.Column.String())] = true
			}
		}
	}
	for _, column := range spec.Columns {
		// 内联键另有隐式可空语义，不能在未解析时假称一致。
		if column.Type.KeyOpt != 0 {
			key := ""
			for _, part := range readTokens(sqlparser.String(&column.Type)) {
				if part.kind != sqlparser.STRING && slices.Contains([]string{"primary", "unique"}, lower(part)) {
					key = lower(part)
				}
			}
			if key == "" {
				return nil, false
			}
			name := column.Name.String()
			indexName := name
			if key == "primary" {
				indexName = "primary"
				primary[strings.ToLower(name)] = true
			}
			expectedIndex := Index{Unique: true, Columns: []string{strings.ToLower(name)}, Method: "BTREE"}
			checks = append(checks, Check{Table: table, Kind: "index", Name: indexName, Index: &expectedIndex})
		}
		expected := columnDefinition(column.Type)
		if primary[strings.ToLower(column.Name.String())] {
			expected.Nullable = false
		}
		checks = append(checks, Check{Table: table, Kind: "column", Name: column.Name.String(), Column: &expected})
	}
	for _, idx := range spec.Indexes {
		name := idx.Info.Name.String()
		if idx.Info.Primary {
			name = "primary"
		}
		if name == "" {
			return nil, false
		}
		expected := Index{Unique: idx.Info.Unique || idx.Info.Primary, Method: "BTREE"}
		// 索引列顺序和前缀长度都参与比较，相同名字不代表相同索引。
		for _, col := range idx.Columns {
			value := strings.ToLower(col.Column.String())
			if col.Length != nil {
				value += "(" + string(col.Length.Val) + ")"
			}
			expected.Columns = append(expected.Columns, value)
		}
		for _, option := range idx.Options {
			if strings.EqualFold(option.Name, "using") {
				expected.Method = strings.ToUpper(option.Using)
			} else {
				return nil, false
			}
		}
		checks = append(checks, Check{Table: table, Kind: "index", Name: name, Index: &expected})
	}
	return checks, true
}

// columnDefinition 组装可与 information_schema 对照的字段属性。
// 参数 kind：解析器字段定义，字符串默认值和注释已经解码。
// 返回：包含类型、可空、默认值、自动增长及显式字符集的结构。
func columnDefinition(kind sqlparser.ColumnType) Column {
	// 类型参数与枚举值采用不同路径构建，不能把枚举内容转成数字精度。
	// UNSIGNED 改变数值范围，不能被整数显示宽度归一抹去。
	// ZEROFILL 隐含无符号语义，同时保留零填充属性。
	// 可空性影响约束，与默认值是否为空分别比较。
	// 字符串 NULL 仍是实际默认字符串，不等同于 SQL NULL。
	// 注释保留原文，结构修改后不能默默遗漏已有说明。
	// 自动更新时间与创建时默认值不是同一种字段行为。
	// 类型长度、精度、符号和枚举值属于结构语义，不能只检查字段名称。
	name := strings.ToLower(kind.Type)
	if kind.Length != nil {
		name += "(" + string(kind.Length.Val)
		if kind.Scale != nil {
			name += "," + string(kind.Scale.Val)
		}
		name += ")"
	}
	if len(kind.EnumValues) > 0 {
		name += "(" + strings.Join(kind.EnumValues, ",") + ")"
	}
	if kind.Unsigned || kind.Zerofill {
		name += " unsigned"
	}
	if kind.Zerofill {
		name += " zerofill"
	}
	column := Column{Type: normalizeType(name), Nullable: !bool(kind.NotNull), AutoIncrement: bool(kind.Autoincrement), Charset: kind.Charset, Collation: kind.Collate}
	// SQL NULL 与没有显式默认值在元数据中均表现为 NULL；字符串 'NULL' 仍为原文。
	if kind.Default != nil && !(kind.Default.Type != sqlparser.StrVal && strings.EqualFold(string(kind.Default.Val), "null")) {
		value := string(kind.Default.Val)
		column.Default = &value
	}
	if kind.Comment != nil {
		column.Comment = string(kind.Comment.Val)
	}
	if kind.OnUpdate != nil {
		column.OnUpdate = normalizeTemporal(string(kind.OnUpdate.Val))
	}
	return column
}

// readTokens 读取保留字符串与标识符语义的词法单元。
// 参数 source：用于结构解析的 SQL，注释不会成为判断依据。
// 返回：可重新组合为等价语法的单元；词法失败时返回空列表。
func readTokens(source string) []token {
	// 标识符与字符串分开引用，避免重组 ALTER 时改变语义。
	// 反引号内部的反引号再次转义，不能逃逸出目标名称。
	// 字符串通过解析器生成字面量，内部逗号与引号不参与子句拆分。
	// 标点保留词法类型，以便区分真正的括号与字符串中的括号。
	// 普通注释不参与目标定义，不把说明中的关键字当成操作。
	// 读取失败不返回前半段单元，否则上层可能误判为完整语句。
	// 重组只用于已支持语法，未知语法仍执行原始文本。
	// 解码后的字符串重新由解析器引用，逗号和引号不会被当成子句分隔符。
	lexer := sqlparser.NewStringTokenizer(source)
	result := []token{}
	for {
		kind, value := lexer.Scan()
		if kind == 0 {
			break
		}
		if kind == sqlparser.COMMENT {
			continue
		}
		text := string(value)
		raw := text
		if kind < 256 && text == "" {
			text = string(rune(kind))
			raw = text
		}
		if kind == sqlparser.STRING {
			raw = sqlparser.String(sqlparser.NewStrVal(value))
		}
		if kind == sqlparser.ID {
			raw = "`" + strings.ReplaceAll(text, "`", "``") + "`"
		}
		result = append(result, token{kind: kind, text: text, raw: raw})
	}
	// 词法故障不得生成部分可核验的结构约束。
	if lexer.LastError != nil {
		return nil
	}
	return result
}

// splitClauses 仅按顶层逗号拆分 ALTER，保留类型精度及索引列列表。
// 参数 tokens：表名之后的完整子句单元。
// 返回：保持原始顺序的子句列表。
func splitClauses(tokens []token) [][]token {
	// 类型精度、枚举列表和复合索引列都可能包含括号内逗号。
	// 字符串单元已经完整解码，其内部标点不能改变括号深度。
	// 顶层逗号才结束一个子句，原先子句顺序保持不变。
	// 结尾无逗号是常见写法，最后一个子句不能丢失。
	// 字符串 token 内的逗号不参与层级计算。
	depth, start := 0, 0
	parts := [][]token{}
	for i, part := range tokens {
		if part.kind == '(' {
			depth++
		}
		if part.kind == ')' {
			depth--
		}
		if part.kind == ',' && depth == 0 {
			parts = append(parts, tokens[start:i])
			start = i + 1
		}
	}
	// 最后一段没有结尾逗号也需要保留。
	if start < len(tokens) {
		parts = append(parts, tokens[start:])
	}
	return parts
}

// tableOptions 提取明确指定的表级属性。
// 参数 tokens：TableSpec 的选项词法单元，不含字段定义。
// 返回：需核对的显式选项；空值表示该属性由数据库默认决定。
func tableOptions(tokens []token) (Table, bool) {
	// 选项来自完整 TableSpec，不扫描字段默认值里的类似文字。
	// CHARACTER SET 与 CHARSET 表示相同的表级选项。
	// 等号在 MySQL 表选项中可省略，两种写法都按语法取值。
	// 选项缺少值时不越界读取后面的任意内存。
	// 引擎只表达显式要求，不预设所有租户默认相同。
	// 排序规则与字符集分别保留，不能仅比较共同前缀。
	// 表注释已经由词法器解码，内部空格与标点需要保留。
	// 其他选项不用于本轮已支持属性的相等判断。
	// 不猜测未声明的默认引擎或字符集，避免不同测试库默认值造成误报。
	result := Table{}
	for i := 0; i < len(tokens); i++ {
		key := lower(tokens[i])
		// DEFAULT 与选项分隔逗号不构成独立属性，其他未知键必须明确拒绝。
		if key == "default" || key == "," {
			continue
		}
		if key == "character" && lower(tokens[i+1]) == "set" {
			key = "charset"
			i++
		}
		next := i + 1
		if next < len(tokens) && tokens[next].text == "=" {
			next++
		}
		if next >= len(tokens) {
			// 不完整选项必须回退人工核验，不能把缺失值当作未声明。
			return result, false
		}
		switch key {
		case "engine":
			result.Engine = tokens[next].text
		case "charset":
			result.Charset = tokens[next].text
		case "collate":
			result.Collation = tokens[next].text
		case "comment":
			result.Comment = tokens[next].text
			result.CheckComment = true
		default:
			// 自动增长起点、行格式等尚未实现的选项不能悄悄跳过。
			return result, false
		}
		i = next
	}
	return result, true
}

// unsupported 阻止只理解一部分扩展 DDL 后宣称核验通过。
// 参数 tokens：排除注释、但保留字符串类别的词法单元。
// 返回：含未支持结构关键字时为 true，字符串中的同名词不参与判断。
func unsupported(tokens []token) bool {
	// 字段或字符串值恰好与关键词相同，不应被误判为约束语法。
	// 外键、表达式、分区和排序索引需要额外元数据，当前明确交给人工核验。
	for _, part := range tokens {
		if part.kind != sqlparser.STRING && part.kind != sqlparser.ID && slices.Contains([]string{"foreign", "references", "constraint", "check", "generated", "partition", "fulltext", "spatial", "invisible", "desc", "temporary"}, lower(part)) {
			return true
		}
	}
	return false
}

// joinTokens 重新组合一个已解析子句，不拼接外部标识符或参数。
// 参数 tokens：已安全引用的单元。
// 返回：用于保留原操作语义的 SQL 子句副本。
func joinTokens(tokens []token) string {
	// 标识符和字面量的安全引用已经在词法读取阶段完成。
	// 标识符与字面量各自引用，空白重排不改变字符串内部内容。
	parts := make([]string, len(tokens))
	for i, part := range tokens {
		parts[i] = part.raw
	}
	return strings.TrimSuffix(strings.Join(parts, " "), " ;")
}

// lower 只归一关键词比较，不改动实际 SQL 或数据值。
// 参数 value：单个词法单元。
// 返回：小写关键词用于控制分支。
func lower(value token) string {
	// 业务字符串和枚举值不使用本函数做结构相等比较。
	return strings.ToLower(value.text)
}

// unknown 保留结构任务的未知边界，避免返回虚假的成功计划。
// 参数 operation：已经识别为结构操作的原文；参数 reason：当前无法完整理解的原因。
// 返回：需要人工核验的结构操作。
func unknown(operation Operation, reason string) Operation {
	// 未支持标记会沿计划传到执行结果，不因数据库无报错而自动清除。
	// 未支持的原文不被改写，执行结果会与结构核验状态分开展示。
	operation.Supported = false
	operation.Reason = fmt.Sprintf("待核验：%s", reason)
	return operation
}
