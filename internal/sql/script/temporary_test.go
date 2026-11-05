package sqlscript

import (
	"strings"
	"testing"
)

func TestTemporaryTableScriptsPreserveSourceAndScope(t *testing.T) {
	for _, source := range []string{
		"-- 中文注释\nCREATE /* 临时快照 */ TEMPORARY TABLE `tmp_snapshot` AS SELECT r.id, JSON_SET(r.config, '$.window', @window) AS config FROM rules r WHERE r.enabled=1",
		"create temporary table if not exists tmp_snapshot (id bigint NOT NULL, config json DEFAULT NULL, PRIMARY KEY(id)) ENGINE=InnoDB COMMENT='temporary; select'",
		"CREATE TEMPORARY TABLE tmp_snapshot SELECT r.id FROM rules r",
		"CREATE TEMPORARY TABLE tmp_snapshot (id bigint) AS SELECT r.id FROM rules r",
		"-- 说明含括号 (不属于字段)\nCREATE TEMPORARY TABLE tmp_snapshot /* (说明) */ AS /* (查询) */ SELECT r.id FROM rules r",
		"CREATE TEMPORARY TABLE tmp_snapshot -- 首部行注释\nSELECT r.id FROM rules r",
	} {
		parts, err := SplitSQL(source + "; UPDATE rules r JOIN tmp_snapshot b ON b.id=r.id SET r.enabled=1;")
		if err != nil || len(parts) != 2 || parts[0] != strings.TrimSpace(source) {
			t.Fatalf("临时表脚本未保留原文和顺序：%v %v", parts, err)
		}
		if phase := InferSQLPhase(map[string]string{"sql": source}); phase != "初始化数据" {
			t.Fatalf("临时表初始化错误地排入建表阶段：%s", phase)
		}
	}
}

func TestTemporaryErrorIncludesStatementNumber(t *testing.T) {
	_, err := SplitSQL("-- 第一条\nSET @window=30; -- 第二条\nCREATE TEMPORARY TABLE tmp_snapshot AS SELECT FROM rules;")
	if err == nil || !strings.Contains(err.Error(), "第 2 条语句校验失败") {
		t.Fatalf("缺少脚本内错误定位：%v", err)
	}
}

func TestTemporaryTablesRejectInvalidOrCrossDatabaseSQL(t *testing.T) {
	for _, source := range []string{
		"CREATE TEMPORARY TABLE other.tmp_snapshot AS SELECT id FROM rules",
		"CREATE TEMPORARY TABLE tmp_snapshot AS SELECT r.id FROM other.rules r",
		"CREATE TEMPORARY TABLE tmp_snapshot AS SELECT r.id FROM rules r WHERE EXISTS (SELECT 1 FROM other.audit a WHERE a.id=r.id)",
		"CREATE TEMPORARY TABLE tmp_snapshot AS SELECT other.rules.id FROM rules",
		"CREATE TEMPORARY TABLE tmp_snapshot AS SELECT FROM rules",
		"CREATE TEMPORARY TABLE tmp_snapshot AS SELECT 1 INTO OUTFILE '/tmp/result'",
		"CREATE TEMPORARY TABLE tmp_snapshot",
		"CREATE TEMPORARY TABLE tmp_snapshot (id invalid_type)",
		"CREATE TEMPORARY TABLE tmp_snapshot LIKE other.rules",
	} {
		if _, err := SplitSQL(source); err == nil {
			t.Errorf("无效或越界语句被放行：%s", source)
		}
	}
}
