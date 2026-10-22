package schema

import (
	"fmt"
	"math/big"
	"regexp"
	"slices"
	"strings"

	"github.com/xwb1989/sqlparser"
)

var integerWidth = regexp.MustCompile(`\b(tinyint|smallint|mediumint|int|integer|bigint)\(\d+\)`)
var temporalZero = regexp.MustCompile(`\b(timestamp|datetime|time)\(0\)`)
var decimalScaleZero = regexp.MustCompile(`\b(decimal|numeric)\((\d+)\)`)

// Compare 对照一个约束，不将同名对象直接视为结构一致。
// 参数 check：解析出的目标约束；参数 actual：该目标表的实时元数据。
// 返回：一致时空字符串；不一致时返回对象及差异字段，不包含连接信息。
func Compare(check Check, actual Table) string {
	// 表、字段、索引分别定位，名称相同不能跨对象类型匹配。
	// 表不存在时，列与索引的存在性查询都没有有效上下文。
	// 字段名按 MySQL 列名规则比较，表名仍保留原始大小写。
	// 删除与新增的目标相反，删除检查必须确认对象确实消失。
	// 字段存在之后仍需对照类型和其他属性，不接受仅名称一致。
	// 索引名称只是定位键，不能替代列顺序和唯一性比较。
	// 复合索引缺列、换序或前缀长度不同都影响实际查询行为。
	// 主键沿用索引检查通道，但必须同时满足唯一性定义。
	// 存在性约束没有字段定义时，只验证该项明确表达的目标。
	// 不一致原因定位到表和对象，便于在任务结果中逐库核对。
	// 表缺失优先反馈；列或索引不能在不存在的表上被认定为已满足。
	if check.Kind == "table" {
		if !actual.Exists {
			return check.Table + "：表不存在"
		}
		return compareOptions(check.Table, check.Options, actual)
	}
	if !actual.Exists {
		return check.Table + "：表不存在"
	}
	name := strings.ToLower(check.Name)
	switch check.Kind {
	case "position":
		// 位置约束独立于字段定义，避免后续仅改类型时丢掉顺序要求。
		// 元数据不足的情况由 verify 另行标为待核验。
		detail, _ := compareColumnPosition(check, actual)
		return detail
	case "column":
		column, exists := actual.Columns[name]
		// 删除约束核对实际不存在，而非默认把 DROP 无报错当作删除成功。
		if check.Absent {
			if exists {
				return check.Table + "." + check.Name + "：字段仍存在"
			}
			return ""
		}
		if !exists {
			return check.Table + "." + check.Name + "：字段不存在"
		}
		if check.Column == nil {
			return ""
		}
		return compareColumn(check.Table+"."+check.Name, *check.Column, column)
	case "index":
		index, exists := actual.Indexes[name]
		if check.Absent {
			if exists {
				return check.Table + "." + check.Name + "：索引仍存在"
			}
			return ""
		}
		if !exists {
			return check.Table + "." + check.Name + "：索引不存在"
		}
		// 唯一性、列顺序、前缀及索引方法都属于索引结构。
		if check.Index != nil && (!slices.Equal(check.Index.Columns, index.Columns) || check.Index.Unique != index.Unique || !strings.EqualFold(check.Index.Method, index.Method)) {
			return check.Table + "." + check.Name + "：索引列、顺序、前缀、唯一性或类型不一致"
		}
	}
	return ""
}

// compareColumn 比较字段中会影响数据与业务行为的属性。
// 参数 name：用于错误定位的表字段；参数 expected：脚本定义；参数 actual：实时元数据。
// 返回：差异清单；不把字段已存在等同于类型、默认值等符合预期。
func compareColumn(name string, expected, actual Column) string {
	// 可空性影响数据约束，不能与类型名称一起模糊匹配。
	// 数值默认值允许服务器格式规范化，字符默认值保持精确比较。
	// 自动增长属于字段行为，即使字段类型相同也必须检查。
	// 显式字符集与排序规则决定数据比较语义，不能忽略。
	// 未声明字符集时使用目标库默认值，不主动猜测某种编码。
	// 差异列表用于本地核验详情，不重复打印默认业务值。
	// 整数显示宽度与数据库自动归一的时间精度不影响类型语义。
	differences := []string{}
	if normalizeType(expected.Type) != normalizeType(actual.Type) {
		differences = append(differences, "类型：预期 "+expected.Type+"，实际 "+actual.Type)
	}
	if expected.Nullable != actual.Nullable {
		differences = append(differences, "可空属性")
	}
	if !defaultEqual(expected.Default, actual.Default, expected.Type) {
		differences = append(differences, "默认值")
	}
	if expected.AutoIncrement != actual.AutoIncrement {
		differences = append(differences, "自动增长")
	}
	// 自动更新时间与注释不能被简单的名称存在性检查遗漏。
	if normalizeTemporal(expected.OnUpdate) != normalizeTemporal(actual.OnUpdate) {
		differences = append(differences, "自动更新时间")
	}
	if expected.Comment != actual.Comment {
		differences = append(differences, "注释")
	}
	if expected.Charset != "" && normalizeCharset(expected.Charset) != normalizeCharset(actual.Charset) {
		differences = append(differences, "字符集")
	}
	if expected.Collation != "" && !strings.EqualFold(expected.Collation, actual.Collation) {
		differences = append(differences, "排序规则")
	}
	if len(differences) == 0 {
		return ""
	}
	// 只给出结构差异，不把字段默认值或业务内容写进应用日志。
	return name + "：" + strings.Join(differences, "、") + "不一致"
}

// compareOptions 只核对脚本明确声明的表选项。
// 参数 name：表名；参数 expected：显式选项；参数 actual：实时表定义。
// 返回：选项差异或空字符串。
func compareOptions(name string, expected *Table, actual Table) string {
	// 不同测试库可能有不同默认配置，只检查脚本明确提出的要求。
	// 存储引擎影响事务与索引行为，不允许仅靠表存在而通过。
	// 表排序规则与字符集分别比较，不能互相替代。
	// 未显式指定的默认引擎或字符集由目标库决定，不制造虚假差异。
	if expected == nil {
		return ""
	}
	if expected.Engine != "" && !strings.EqualFold(expected.Engine, actual.Engine) {
		return name + "：存储引擎不一致"
	}
	if expected.Charset != "" && normalizeCharset(expected.Charset) != normalizeCharset(actual.Charset) {
		return name + "：表字符集不一致"
	}
	if expected.Collation != "" && !strings.EqualFold(expected.Collation, actual.Collation) {
		return name + "：表排序规则不一致"
	}
	// 空注释未显式声明时不检查；字段级注释仍完整比较。
	if expected.CheckComment && expected.Comment != actual.Comment {
		return name + "：表注释不一致"
	}
	return ""
}

// normalizeType 消除 MySQL 版本之间不改变语义的展示差异。
// 参数 value：完整 COLUMN_TYPE 或脚本字段类型。
// 返回：稳定类型表示；枚举值的大小写保持不变。
func normalizeType(value string) string {
	// 整数显示宽度不决定存储范围，MySQL 不同版本可能省略它。
	// 时间类型零精度与未写精度的展示可以等价。
	// DECIMAL 省略精度时按 MySQL 固定缺省定义对照。
	// 枚举值是业务数据，不能随类型关键字一起转小写。
	// 词法引用保留枚举内部空白，不能用普通字符串压缩改变值。
	// 有符号与无符号仍是不同类型，规范化不会抹去该信息。
	// 只把类型关键字转小写，枚举和集合的业务值必须保持原样。
	value = strings.TrimSpace(value)
	prefix := value
	if index := strings.Index(value, "("); index >= 0 {
		prefix = value[:index]
	}
	value = strings.ToLower(prefix) + strings.TrimPrefix(value, prefix)
	// ZEROFILL 的显示宽度影响格式，不能采用普通整数的忽略宽度规则。
	if !strings.Contains(strings.ToLower(value), "zerofill") {
		value = integerWidth.ReplaceAllString(value, "$1")
	}
	value = temporalZero.ReplaceAllString(value, "$1")
	value = decimalScaleZero.ReplaceAllString(value, "$1($2,0)")
	// DECIMAL(n) 的缺省小数位是零；不能把服务器补出的 ,0 当成结构漂移。
	// INT/INTEGER 和 BOOL 是 MySQL 的类型别名。
	value = strings.ReplaceAll(value, "integer", "int")
	if strings.HasPrefix(value, "numeric") {
		value = "decimal" + strings.TrimPrefix(value, "numeric")
	}
	if value == "bool" || value == "boolean" {
		value = "tinyint"
	}
	if value == "decimal" {
		value = "decimal(10,0)"
	}
	if value == "char" {
		value = "char(1)"
	}
	// 使用词法单元规范化空白，枚举字符串内的连续空格不得被折叠。
	parts := readTokens(value)
	normalized := make([]string, len(parts))
	for i, part := range parts {
		normalized[i] = part.raw
		if part.kind != sqlparser.STRING {
			normalized[i] = strings.ToLower(part.raw)
		}
	}
	return strings.Join(normalized, " ")
}

// defaultEqual 比较默认值，同时处理数值与时间函数的服务器规范化。
// 参数 expected：脚本默认值；参数 actual：元数据默认值；参数 kind：字段类型。
// 返回：默认语义一致为 true，字符串值区分大小写并保留空字符串。
func defaultEqual(expected, actual *string, kind string) bool {
	// 元数据 NULL 表示没有可用默认字符串，不同于空文本默认值。
	// 时间函数的大小写差异只对时间类型规范化。
	// 数值类型使用精确有理数比较，避免浮点舍入造成误判。
	// NULL 与字符串 NULL 不同，不能把缺省或空字符串混为一谈。
	if expected == nil || actual == nil {
		return expected == nil && actual == nil
	}
	if *expected == *actual {
		return true
	}
	// 只有时间类型允许规范化函数大小写，普通字符默认值必须精确匹配。
	if slices.Contains([]string{"timestamp", "datetime", "date", "time"}, strings.TrimSpace(strings.Split(kind, "(")[0])) {
		return normalizeTemporal(*expected) == normalizeTemporal(*actual)
	}
	if regexp.MustCompile(`^(tinyint|smallint|mediumint|int|bigint|decimal|numeric|float|double)\b`).MatchString(kind) {
		a, ok := new(big.Rat).SetString(*expected)
		b, other := new(big.Rat).SetString(*actual)
		return ok && other && a.Cmp(b) == 0
	}
	return false
}

// normalizeTemporal 规范化服务器对当前时间函数的展示。
// 参数 value：时间默认值或自动更新时间，不接收任意 SQL 执行指令。
// 返回：用于纯文本相等比较的时间表达式。
func normalizeTemporal(value string) string {
	// 不会移除非零精度，CURRENT_TIMESTAMP(3) 与零精度仍须区分。
	// CURRENT_TIMESTAMP、CURRENT_TIMESTAMP() 与零精度版本语义相同。
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "current_timestamp()", "current_timestamp")
	return strings.ReplaceAll(value, "current_timestamp(0)", "current_timestamp")
}

// normalizeCharset 处理 MySQL 对 utf8 的旧别名。
// 参数 value：元数据或脚本声明的字符集。
// 返回：统一的小写字符集，不更改 utf8mb4 与 utf8mb3 的区别。
func normalizeCharset(value string) string {
	// 三字节 utf8 与四字节 utf8mb4 不可互换，避免错误通过容量差异。
	// utf8 在本核验协议中对应 MySQL 的三字节字符集，不能等同于 utf8mb4。
	value = strings.ToLower(value)
	if value == "utf8" {
		return "utf8mb3"
	}
	return value
}

// describe 用有限对象位置生成核验标签。
// 参数 check：真实解析出的结构目标。
// 返回：用于纯文本进度的对象名称。
func describe(check Check) string {
	// 标签不包含 SQL 原文、连接账号或字段数据。
	return fmt.Sprintf("%s.%s", check.Table, check.Name)
}
