package schema

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestTemporaryInitializationExecutesInOrderWithoutPermanentMetadata(t *testing.T) {
	statements := []string{"SET @window=30", "CREATE TEMPORARY TABLE tmp_snapshot (id bigint, config json)", "INSERT INTO tmp_snapshot SELECT id,config FROM rules", "CREATE TEMPORARY TABLE tmp_selected AS SELECT b.id FROM tmp_snapshot b", "UPDATE rules r JOIN tmp_selected b ON b.id=r.id SET r.enabled=1"}
	access := &fakeAccess{readErr: errors.New("临时表不应查询普通元数据")}
	detail, err := Run(context.Background(), statements, access, false)
	if err != nil || !slices.Equal(access.execs, statements) {
		t.Fatalf("临时表阻断后续初始化或原文变动：%v %v", err, access.execs)
	}
	if !strings.Contains(detail, "临时表") || strings.Contains(detail, "结构核验通过") {
		t.Fatalf("执行摘要混淆临时表与持久结构核验：%s", detail)
	}
	access.execs = nil
	if _, err = Run(context.Background(), statements, access, true); err == nil || len(access.execs) != 0 {
		t.Fatalf("历史复核重放临时表初始化：%v %v", err, access.execs)
	}
}

func TestTemporaryFailureStopsDataAndPermanentMismatchStillBlocks(t *testing.T) {
	access := &fakeAccess{execErr: errors.New("临时表创建失败")}
	if _, err := Run(context.Background(), []string{"CREATE TEMPORARY TABLE tmp_snapshot AS SELECT id FROM rules", "UPDATE rules SET enabled=1"}, access, false); err == nil || len(access.execs) != 1 {
		t.Fatalf("临时表失败后仍写入：%v %v", err, access.execs)
	}
	access = &fakeAccess{table: Table{Exists: true, Columns: map[string]Column{"enabled": actualColumn("varchar(10)")}}}
	if _, err := Run(context.Background(), []string{"ALTER TABLE rules ADD enabled int", "CREATE TEMPORARY TABLE tmp_snapshot AS SELECT id FROM rules", "UPDATE rules SET enabled=1"}, access, false); err == nil || len(access.execs) != 0 {
		t.Fatalf("临时表绕过了持久字段不一致：%v %v", err, access.execs)
	}
}
