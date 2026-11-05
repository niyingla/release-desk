package schema

import (
	"context"
	"strings"
	"testing"
)

// TestModifyStatementsKeepActionUnquoted 覆盖真实 MODIFY 脚本在执行重组后的关键字与逐项结构核验。
// 参数 t：Go 测试上下文；使用模拟元数据和执行适配器，不连接或修改真实数据库。
func TestModifyStatementsKeepActionUnquoted(t *testing.T) {
	cases := []struct {
		table, column, definition, comment string
		nullable                           bool
	}{
		{"le_jdc_smp_message_daily", "enterprise_uid", "NOT NULL", "企业主号抖音uid", false},
		{"le_jdc_smp_live", "room_id", "NOT NULL", "直播间ID", false},
		{"le_jdc_smp_video", "enterprise_uid", "DEFAULT NULL", "企业号uid", true},
	}
	for _, test := range cases {
		t.Run(test.table, func(t *testing.T) {
			source := "ALTER TABLE `" + test.table + "` MODIFY COLUMN `" + test.column + "` varchar(64) " + test.definition + " COMMENT '" + test.comment + "'"
			access := &fakeAccess{table: Table{Exists: true, Columns: map[string]Column{test.column: {Type: "varchar(32)", Nullable: test.nullable, Comment: test.comment}}}}
			access.apply = func(sent string, table *Table) {
				// 校验真正传给驱动的语法位置，旧解析器本身不能可靠拒绝错误的反引号关键字。
				if !strings.HasPrefix(sent, "ALTER TABLE `"+test.table+"` MODIFY ") {
					t.Fatalf("MODIFY 被改写成标识符：%s", sent)
				}
				if !strings.Contains(sent, "`"+test.column+"`") || !strings.Contains(sent, "'"+test.comment+"'") {
					t.Fatalf("字段名或中文注释被改写：%s", sent)
				}
				table.Columns[test.column] = Column{Type: "varchar(64)", Nullable: test.nullable, Comment: test.comment}
			}
			if _, err := Run(context.Background(), []string{source}, access, false); err != nil || len(access.execs) != 1 {
				t.Fatalf("执行或最终结构核验失败：%v", err)
			}
			// 实际定义一致后再次执行应跳过，不能为修复语法问题失去结构幂等检查。
			if _, err := Run(context.Background(), []string{source}, access, false); err != nil || len(access.execs) != 1 {
				t.Fatalf("重复执行没有按结构跳过：%v", err)
			}
		})
	}
}

// TestModifyKeywordFixDoesNotUnquoteNames 防止用全局替换修复动作时误改同名列、参照列和字符串。
// 参数 t：Go 测试上下文；只构建待执行文本，不发出数据库写操作。
func TestModifyKeywordFixDoesNotUnquoteNames(t *testing.T) {
	access := &fakeAccess{table: Table{Exists: true, Columns: map[string]Column{
		"modify": {Type: "varchar(32)", Nullable: true, Ordinal: 1},
		"after":  {Type: "int", Nullable: true, Ordinal: 2},
	}}}
	sent, skipped, err := pendingStatement(context.Background(), access, Parse("ALTER TABLE t MODIFY COLUMN `modify` varchar(64) COMMENT 'MODIFY, AFTER, FIRST' AFTER `after`"))
	if err != nil || skipped || !strings.HasPrefix(sent, "ALTER TABLE `t` MODIFY ") || !strings.Contains(sent, "`modify`") || !strings.HasSuffix(sent, "AFTER `after`") || !strings.Contains(sent, "'MODIFY, AFTER, FIRST'") {
		t.Fatalf("动作、位置与业务值的语法身份混淆：%s / %v", sent, err)
	}
}
