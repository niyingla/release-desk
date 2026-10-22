package schema

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
)

type fakeAccess struct {
	table   Table
	execs   []string
	readErr error
	execErr error
	apply   func(string, *Table)
}

func (f *fakeAccess) Read(context.Context, string) (Table, error) { return f.table, f.readErr }
func (f *fakeAccess) Exec(_ context.Context, source string) (int64, error) {
	f.execs = append(f.execs, source)
	if f.apply != nil {
		f.apply(source, &f.table)
	}
	return 0, f.execErr
}
func actualColumn(typ string) Column { return Column{Type: typ, Nullable: true} }

func TestExistingColumnWrongTypeIsNotSuccess(t *testing.T) {
	f := &fakeAccess{table: Table{Exists: true, Columns: map[string]Column{"name": actualColumn("varchar(10)")}, Indexes: map[string]Index{}}}
	_, err := Run(context.Background(), []string{"ALTER TABLE t ADD COLUMN name varchar(20)"}, f, false)
	var mismatch *VerificationError
	if !errors.As(err, &mismatch) || mismatch.Unverified || !strings.Contains(err.Error(), "类型") || len(f.execs) != 0 {
		t.Fatalf("同名字段被误认为成功：%v %v", err, f.execs)
	}
}

func TestPartialAlterOnlyExecutesMissingClauseAndVerifies(t *testing.T) {
	f := &fakeAccess{table: Table{Exists: true, Columns: map[string]Column{"a": actualColumn("int")}, Indexes: map[string]Index{}}}
	f.apply = func(source string, table *Table) {
		if !strings.Contains(source, "`b`") || strings.Contains(source, "`a`") {
			t.Fatalf("重复执行已有字段：%s", source)
		}
		table.Columns["b"] = actualColumn("varchar(20)")
	}
	detail, err := Run(context.Background(), []string{"ALTER TABLE t ADD COLUMN a int, ADD COLUMN b varchar(20)"}, f, false)
	if err != nil || len(f.execs) != 1 || !strings.Contains(detail, "核验通过") {
		t.Fatalf("增量执行错误：%s %v", detail, err)
	}
}

func TestSuccessfulExecStillRequiresActualChange(t *testing.T) {
	f := &fakeAccess{table: Table{Exists: true, Columns: map[string]Column{"a": actualColumn("int")}, Indexes: map[string]Index{}}}
	_, err := Run(context.Background(), []string{"ALTER TABLE t MODIFY COLUMN a bigint"}, f, false)
	if err == nil || !strings.Contains(err.Error(), "结构不一致") || len(f.execs) != 1 {
		t.Fatalf("仅凭 Exec=nil 判成功：%v", err)
	}
	f.apply = func(_ string, table *Table) { table.Columns["a"] = actualColumn("bigint") }
	if _, err = Run(context.Background(), []string{"ALTER TABLE t MODIFY COLUMN a bigint"}, f, false); err != nil {
		t.Fatal(err)
	}
}

func TestIndexComparisonAndQuotedCommaDefault(t *testing.T) {
	op := Parse("ALTER TABLE t ADD COLUMN name varchar(20) NOT NULL DEFAULT 'a,b' COMMENT '说明', ADD UNIQUE INDEX uk_name (name(10))")
	if !op.Supported || len(op.Clauses) != 2 {
		t.Fatalf("子句解析失败：%+v", op)
	}
	col := op.Clauses[0].Checks[0].Column
	if col == nil || col.Default == nil || *col.Default != "a,b" || col.Comment != "说明" || col.Nullable {
		t.Fatalf("字段语义丢失：%+v", col)
	}
	check := op.Clauses[1].Checks[0]
	actual := Table{Exists: true, Indexes: map[string]Index{"uk_name": {Unique: false, Columns: []string{"name(10)"}, Method: "BTREE"}}}
	if Compare(check, actual) == "" {
		t.Fatal("同名非唯一索引被误认为唯一索引")
	}
}

func TestCreateChecksColumnsAndIndexAndHistoricalVerifyOnly(t *testing.T) {
	sql := "CREATE TABLE t(id bigint NOT NULL, name varchar(20) DEFAULT 'a', PRIMARY KEY(id), KEY idx_name(name)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4"
	op := Parse(sql)
	if !op.Supported || len(op.Clauses) == 0 {
		t.Fatalf("建表无法核验：%+v", op)
	}
	f := &fakeAccess{table: Table{Exists: true, Engine: "InnoDB", Charset: "utf8mb4", Columns: map[string]Column{"id": {Type: "bigint", Nullable: false}, "name": {Type: "varchar(20)", Nullable: true, Default: stringPointer("a")}}, Indexes: map[string]Index{"primary": {Unique: true, Columns: []string{"id"}, Method: "BTREE"}, "idx_name": {Columns: []string{"name"}, Method: "BTREE"}}}}
	if _, err := Run(context.Background(), []string{sql, "INSERT INTO t VALUES(1,'a')"}, f, true); err != nil || len(f.execs) != 0 {
		t.Fatalf("历史复核重复执行数据：%v %v", err, f.execs)
	}
	delete(f.table.Columns, "name")
	if _, err := Run(context.Background(), []string{sql}, f, true); err == nil {
		t.Fatal("旧成功历史掩盖了当前结构缺失")
	}
}

func TestMetadataFailureDoesNotWriteAndUnsupportedIsUnverified(t *testing.T) {
	f := &fakeAccess{readErr: errors.New("模拟没有元数据权限")}
	_, err := Run(context.Background(), []string{"ALTER TABLE t ADD COLUMN a int"}, f, false)
	var verification *VerificationError
	if !errors.As(err, &verification) || !verification.Unverified || len(f.execs) != 0 {
		t.Fatalf("检查失败仍执行：%v", err)
	}
	f.readErr = nil
	_, err = Run(context.Background(), []string{"ALTER TABLE t ENGINE=InnoDB"}, f, false)
	if !errors.As(err, &verification) || !verification.Unverified {
		t.Fatalf("未知语法标为通过：%v", err)
	}
}

func TestFinalDefinitionOverridesIntermediateAndDefaultsAreExact(t *testing.T) {
	f := &fakeAccess{table: Table{Exists: true, Columns: map[string]Column{"a": actualColumn("varchar(100)")}, Indexes: map[string]Index{}}}
	if _, err := Run(context.Background(), []string{"ALTER TABLE t ADD COLUMN a varchar(20)", "ALTER TABLE t MODIFY COLUMN a varchar(100)"}, f, true); err != nil {
		t.Fatal(err)
	}
	if defaultEqual(stringPointer("A"), stringPointer("a"), "varchar(20)") {
		t.Fatal("字符串默认值大小写被忽略")
	}
	if !defaultEqual(stringPointer("0"), stringPointer("0.00"), "decimal(10,2)") {
		t.Fatal("数值默认规范化失败")
	}
}
func stringPointer(value string) *string { return &value }

func TestDuplicateRaceRequiresActualMatchingStructure(t *testing.T) {
	f := &fakeAccess{table: Table{Exists: true, Columns: map[string]Column{}, Indexes: map[string]Index{}}, execErr: &mysql.MySQLError{Number: 1060, Message: "模拟并发新增同名字段"}}
	f.apply = func(_ string, table *Table) { table.Columns["a"] = actualColumn("varchar(20)") }
	if _, err := Run(context.Background(), []string{"ALTER TABLE t ADD COLUMN a varchar(20)"}, f, false); err != nil {
		t.Fatalf("实际结构已符合却只按报错失败：%v", err)
	}
	f.table.Columns = map[string]Column{}
	f.apply = func(_ string, table *Table) { table.Columns["a"] = actualColumn("varchar(10)") }
	if _, err := Run(context.Background(), []string{"ALTER TABLE t ADD COLUMN a varchar(20)"}, f, false); err == nil {
		t.Fatal("重复字段但类型不同被当作成功")
	}
}

func TestInconsistentStructureBlocksLaterDataWrites(t *testing.T) {
	f := &fakeAccess{table: Table{Exists: true, Columns: map[string]Column{"a": actualColumn("varchar(10)")}, Indexes: map[string]Index{}}}
	_, err := Run(context.Background(), []string{"ALTER TABLE t ADD COLUMN a varchar(20)", "INSERT INTO t VALUES('data')"}, f, false)
	if err == nil || len(f.execs) != 0 {
		t.Fatalf("结构不一致后仍写入数据：%v %v", err, f.execs)
	}
}

func TestTypeNormalizationPreservesBusinessValuesAndZerofillWidth(t *testing.T) {
	if normalizeType("enum('A  B')") == normalizeType("enum('A B')") {
		t.Fatal("枚举内部空格丢失")
	}
	if normalizeType("int(8) zerofill") == normalizeType("int(11) zerofill") {
		t.Fatal("零填充宽度被忽略")
	}
	if normalizeType("decimal(10)") != normalizeType("decimal(10,0)") {
		t.Fatal("缺省小数位误报")
	}
	op := Parse("CREATE TABLE t(id bigint NOT NULL AUTO_INCREMENT PRIMARY KEY)")
	if !op.Supported {
		t.Fatalf("普通内联主键不应退出自动核验：%+v", op)
	}
	if Parse("CREATE TABLE t(id int) ROW_FORMAT=COMPRESSED").Supported {
		t.Fatal("未知表选项被静默忽略")
	}
}
