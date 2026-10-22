package schema

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// orderedTable 构造包含真实序号的内存结构，便于独立验证 SQL 位置语义。
// 参数 names：按物理顺序排列的字段名，统一使用可空 int 定义。
// 返回：仅供测试的独立结构，不访问数据库。
func orderedTable(names ...string) Table {
	table := Table{Exists: true, Columns: map[string]Column{}, Indexes: map[string]Index{}}
	for index, name := range names {
		column := actualColumn("int")
		column.Ordinal = index + 1
		table.Columns[name] = column
	}
	return table
}

// TestColumnOrderChecksFinalLayout 验证链式插入、相同锚点、FIRST、重命名及移除的最终语义。
// 参数 t：Go 测试上下文；只读核验中的任何 Exec 都视为错误。
func TestColumnOrderChecksFinalLayout(t *testing.T) {
	for _, test := range []struct {
		name              string
		statements        []string
		order             []string
		valid, unverified bool
	}{
		{"chain", []string{"ALTER TABLE t ADD a int AFTER store_id, ADD b int AFTER a, ADD c int AFTER b"}, []string{"id", "store_id", "a", "b", "c"}, true, false},
		{"chain-wrong", []string{"ALTER TABLE t ADD a int AFTER store_id, ADD b int AFTER a"}, []string{"store_id", "b", "a"}, false, false},
		{"not-adjacent", []string{"ALTER TABLE t ADD a int AFTER store_id"}, []string{"store_id", "extra", "a"}, false, false},
		{"same-anchor", []string{"ALTER TABLE t ADD a int AFTER b, ADD c int AFTER b"}, []string{"b", "c", "a"}, true, false},
		{"same-anchor-wrong", []string{"ALTER TABLE t ADD a int AFTER b, ADD c int AFTER b"}, []string{"b", "a", "c"}, false, false},
		{"first", []string{"ALTER TABLE t ADD a int FIRST"}, []string{"a", "b"}, true, false},
		{"first-wrong", []string{"ALTER TABLE t ADD a int FIRST"}, []string{"b", "a"}, false, false},
		{"repeated-first", []string{"ALTER TABLE t ADD a int FIRST, ADD c int FIRST"}, []string{"c", "a", "b"}, true, false},
		{"modify-keeps-position", []string{"ALTER TABLE t ADD a int AFTER b", "ALTER TABLE t MODIFY a int"}, []string{"a", "b"}, false, false},
		{"rename-target", []string{"ALTER TABLE t ADD a int AFTER b", "ALTER TABLE t CHANGE a x int"}, []string{"b", "x"}, true, false},
		{"rename-anchor", []string{"ALTER TABLE t ADD a int AFTER b", "ALTER TABLE t CHANGE b x int"}, []string{"x", "a"}, true, false},
		{"known-anchor-move", []string{"ALTER TABLE t ADD b int FIRST, ADD a int AFTER b", "ALTER TABLE t MODIFY b int AFTER c"}, []string{"a", "c", "b"}, true, false},
		{"unknown-anchor-move", []string{"ALTER TABLE t MODIFY a int AFTER b", "ALTER TABLE t MODIFY b int AFTER c"}, []string{"a", "c", "b"}, false, true},
		{"drop-anchor", []string{"ALTER TABLE t ADD b int FIRST, ADD a int AFTER b", "ALTER TABLE t DROP b"}, []string{"a", "c"}, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			access := &fakeAccess{table: orderedTable(test.order...)}
			_, err := Run(context.Background(), test.statements, access, true)
			if (err == nil) != test.valid || len(access.execs) != 0 {
				t.Fatalf("位置核验结论错误或发生写入：%v", err)
			}
			if err != nil {
				var result *VerificationError
				if !errors.As(err, &result) || result.Unverified != test.unverified {
					t.Fatalf("核验错误类型不正确：%v", err)
				}
			}
		})
	}
}

// TestColumnOrderPartialExecutionRetainsSuffix 验证已有字段跳过，缺失字段保留 AFTER 再核对实际位置。
// 参数 t：Go 测试上下文；修改只发生在模拟元数据。
func TestColumnOrderPartialExecutionRetainsSuffix(t *testing.T) {
	access := &fakeAccess{table: orderedTable("store_id", "a")}
	access.apply = func(sql string, table *Table) {
		if strings.Contains(sql, "ADD `a`") || strings.Contains(sql, "add `a`") || !strings.Contains(sql, "AFTER `a`") {
			t.Errorf("重组语句错误：%s", sql)
		}
		*table = orderedTable("store_id", "a", "b")
	}
	_, err := Run(context.Background(), []string{"ALTER TABLE t ADD a int AFTER store_id, ADD b int AFTER a"}, access, false)
	if err != nil || len(access.execs) != 1 {
		t.Fatalf("增量新增失败：%v", err)
	}
	// 当前定义与顺序已经一致，再次执行不应重放 ADD。
	if _, err = Run(context.Background(), []string{"ALTER TABLE t ADD a int AFTER store_id, ADD b int AFTER a"}, access, false); err != nil || len(access.execs) != 1 {
		t.Fatalf("重复执行已有字段：%v", err)
	}
}

// TestColumnOrderReturnedSQLStillNeedsMetadata 验证返回成功不能代替最终字段顺序核对。
// 参数 t：Go 测试上下文；模拟 Exec 成功但没有改变真实顺序。
func TestColumnOrderReturnedSQLStillNeedsMetadata(t *testing.T) {
	access := &fakeAccess{table: orderedTable("b", "a")}
	_, err := Run(context.Background(), []string{"ALTER TABLE t MODIFY a int FIRST"}, access, false)
	if err == nil || len(access.execs) != 1 || !strings.Contains(err.Error(), "FIRST") {
		t.Fatalf("未改变顺序却判通过：%v", err)
	}
	access.execs = nil
	column := access.table.Columns["a"]
	column.Ordinal = 0
	access.table.Columns["a"] = column
	_, err = Run(context.Background(), []string{"ALTER TABLE t MODIFY a int FIRST"}, access, true)
	var result *VerificationError
	if !errors.As(err, &result) || !result.Unverified || len(access.execs) != 0 {
		t.Fatalf("缺少位置信息未保持待核验：%v", err)
	}
}

// TestColumnOrderSyntaxIsSupported 覆盖本次真实语法以及可选 COLUMN、MODIFY、CHANGE。
// 参数 t：Go 测试上下文；只解析文本，不连接或修改数据库。
func TestColumnOrderSyntaxIsSupported(t *testing.T) {
	for _, source := range []string{
		"ALTER TABLE dy_member_apply ADD COLUMN customer_source tinyint(4) NOT NULL DEFAULT 1 COMMENT '客户来源' AFTER store_id, ADD COLUMN source_type tinyint(4) NULL AFTER customer_source, ADD COLUMN source_id int(11) NULL AFTER source_type",
		"ALTER TABLE t ADD a int FIRST",
		"ALTER TABLE t ADD a int AFTER `first`",
		"ALTER TABLE t MODIFY COLUMN a bigint AFTER b",
		"ALTER TABLE t CHANGE COLUMN old_name new_name varchar(20) FIRST",
		"ALTER TABLE t ADD a varchar(20) DEFAULT 'AFTER first' COMMENT 'FIRST,AFTER' AFTER b",
	} {
		if op := Parse(source); !op.Supported || len(op.Clauses) == 0 {
			t.Errorf("顺序语法不支持：%s：%s", source, op.Reason)
		}
	}
}

// TestReadOnlyColumnOrderCheckDoesNotReplayData 验证含初始化数据的脚本也只查询结构。
// 参数 t：Go 测试上下文；任何 Exec 调用都将使断言失败。
func TestReadOnlyColumnOrderCheckDoesNotReplayData(t *testing.T) {
	access := &fakeAccess{table: orderedTable("id", "a")}
	_, err := Run(context.Background(), []string{"ALTER TABLE t ADD a int AFTER id", "INSERT INTO t VALUES (1,2)"}, access, true)
	if err != nil || len(access.execs) != 0 {
		t.Fatalf("只读复核发生写入或核验失败：%v", err)
	}
}

// TestColumnOrderRewriteRetainsKeywords 防止旧词法器将位置关键字作为标识符重新引用。
// 参数 t：Go 测试上下文；只核对待发送文本，不执行 SQL。
func TestColumnOrderRewriteRetainsKeywords(t *testing.T) {
	op := Parse("ALTER TABLE t ADD a int AFTER b, ADD c int FIRST")
	for _, clause := range op.Clauses {
		if strings.Contains(clause.SQL, "`AFTER`") || strings.Contains(clause.SQL, "`after`") || strings.Contains(clause.SQL, "`FIRST`") || strings.Contains(clause.SQL, "`first`") {
			t.Errorf("位置关键字被错误引用：%s", clause.SQL)
		}
	}
}
