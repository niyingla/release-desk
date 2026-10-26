package schema

import (
	"context"
	"strings"
	"testing"
)

// TestEnumAndSetValuesCannotBeNormalizedAsSQLTypes 防止类型展示归一改写枚举业务值。
// 参数 t：测试上下文；仅比较内存定义，不连接真实数据库。
func TestEnumAndSetValuesCannotBeNormalizedAsSQLTypes(t *testing.T) {
	cases := [][2]string{
		{"enum('int(8)')", "enum('int')"},
		{"enum('integer')", "enum('int')"},
		{"enum('timestamp(0)')", "enum('timestamp')"},
		{"set('decimal(10)')", "set('decimal(10,0)')"},
	}
	for _, values := range cases {
		// 关键字出现在引号内时代表业务值，不能采用数字或时间类型的同义规则。
		check := Check{Table: "t", Kind: "column", Name: "state", Column: &Column{Type: values[0], Nullable: true}}
		actual := Table{Exists: true, Columns: map[string]Column{"state": {Type: values[1], Nullable: true}}}
		if Compare(check, actual) == "" {
			t.Errorf("不同业务值被误判一致：%s / %s", values[0], values[1])
		}
	}
}

// TestAlterFilteringPreservesDependentClauses 防止按初始结构省略同一 ALTER 内依赖前文的子句。
// 参数 t：测试上下文；使用内存元数据与执行记录，不执行实际 DDL。
func TestAlterFilteringPreservesDependentClauses(t *testing.T) {
	cases := []struct {
		name, source string
		initial      Table
		fragments    []string
	}{
		{"add-then-drop", "ALTER TABLE t ADD COLUMN a int, DROP COLUMN a", Table{Exists: true, Columns: map[string]Column{}, Indexes: map[string]Index{}}, []string{"ADD", "DROP"}},
		{"drop-then-add", "ALTER TABLE t DROP COLUMN a, ADD COLUMN a bigint", Table{Exists: true, Columns: map[string]Column{"a": actualColumn("int")}, Indexes: map[string]Index{}}, []string{"DROP", "ADD"}},
		{"replace-index", "ALTER TABLE t DROP INDEX idx_a, ADD INDEX idx_a (a)", Table{Exists: true, Columns: map[string]Column{"a": actualColumn("int")}, Indexes: map[string]Index{"idx_a": {Columns: []string{"a"}, Method: "BTREE"}}}, []string{"DROP", "ADD"}},
		{"column-order", "ALTER TABLE t MODIFY COLUMN a int FIRST, MODIFY COLUMN b int AFTER a", Table{Exists: true, Columns: map[string]Column{"c": {Type: "int", Nullable: true, Ordinal: 1}, "a": {Type: "int", Nullable: true, Ordinal: 2}, "b": {Type: "int", Nullable: true, Ordinal: 3}}, Indexes: map[string]Index{}}, []string{"FIRST", "AFTER"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			access := &fakeAccess{table: test.initial}
			// 只检查实际将发送的 SQL，独立的最终结构比较已有专门覆盖。
			source, skipped, err := pendingStatement(context.Background(), access, Parse(test.source))
			if err != nil || skipped {
				t.Fatalf("有依赖的 ALTER 不应整条省略：%v %v", skipped, err)
			}
			for _, fragment := range test.fragments {
				// 前序子句会改变同名对象状态，后序动作必须按原顺序保留。
				if !strings.Contains(strings.ToUpper(source), fragment) {
					t.Errorf("依赖子句被省略：%s", source)
				}
			}
		})
	}
}

// TestIndependentExistingClauseStillSkipped 防止依赖保护退化为重发所有已有对象。
// 参数 t：测试上下文；只使用模拟元数据与 SQL 文本。
func TestIndependentExistingClauseStillSkipped(t *testing.T) {
	access := &fakeAccess{table: Table{Exists: true, Columns: map[string]Column{"a": actualColumn("int")}, Indexes: map[string]Index{}}}
	// 后置的已存在字段与前文新增字段无依赖，仍应省略以避免重复 ADD。
	source, skipped, err := pendingStatement(context.Background(), access, Parse("ALTER TABLE t ADD COLUMN b int, ADD COLUMN a int"))
	if err != nil || skipped || strings.Contains(source, "`a`") || !strings.Contains(source, "`b`") {
		t.Fatalf("无关已有对象被重复发送：%s / %v", source, err)
	}
}
