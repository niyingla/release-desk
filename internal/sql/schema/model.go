// Package schema 对照 MySQL 元数据核验 DDL 的目标结构，不把 Exec 成功视为核验通过。
package schema

type Column struct {
	Ordinal       int
	Type          string
	Nullable      bool
	Default       *string
	AutoIncrement bool
	OnUpdate      string
	Comment       string
	Charset       string
	Collation     string
}
type Index struct {
	Unique  bool
	Columns []string
	Method  string
}
type Table struct {
	CheckComment                        bool
	Exists                              bool
	Engine, Charset, Collation, Comment string
	Columns                             map[string]Column
	Indexes                             map[string]Index
}
type Check struct {
	Table, Kind, Name string
	RenameFrom        string
	Position          *ColumnPlacement
	Absent            bool
	Column            *Column
	Index             *Index
	Options           *Table
}

// ColumnPlacement 独立表达位置约束，后续只改类型时不会丢掉先前的顺序要求。
type ColumnPlacement struct {
	First      bool
	After      string
	Unverified bool
}
type Clause struct {
	SQL          string
	Checks       []Check
	SkipExisting bool
}
type Operation struct {
	SQL, Table                    string
	Structural, Supported, Create bool
	Clauses                       []Clause
	Reason                        string
}
type VerificationError struct {
	Message    string
	Unverified bool
}

// Error 返回核验失败或待核验的原因。
// 无业务入参；消息由核验器构造，不含连接凭据。
// 返回：纯文本原因，供逐库审计和任务中心展示。
func (e *VerificationError) Error() string {
	// 错误类型独立保留，执行器可以区分结构差异和普通 SQL 错误。
	return e.Message
}
