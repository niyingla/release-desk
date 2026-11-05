package sqlservice

import (
	"testing"

	sqlscript "release-desk/internal/sql/script"
	"release-desk/internal/storage/workspace"
)

func TestTemporaryInitializationFollowsRequiredPersistentDDL(t *testing.T) {
	items := []workspace.Item{
		{ID: "initialize", Kind: "sql", Data: map[string]string{"sql": "CREATE TEMPORARY TABLE tmp_snapshot AS SELECT id,enabled FROM rules; UPDATE rules SET enabled=1", "order": "1"}},
		{ID: "alter", Kind: "sql", Data: map[string]string{"sql": "ALTER TABLE rules ADD enabled int", "order": "9"}},
		{ID: "create", Kind: "sql", Data: map[string]string{"sql": "CREATE TABLE rules(id bigint)", "order": "10"}},
	}
	preview, err := BuildSQLQueuePreview("sample", items, func(id string) (Plan, error) {
		for _, item := range items {
			if item.ID == id {
				_, err := sqlscript.SplitSQL(item.Data["sql"])
				return Plan{ItemID: id, Environment: "test"}, err
			}
		}
		t.Fatal("未知事项")
		return Plan{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Items) != 3 || preview.Items[0].Plan.ItemID != "create" || preview.Items[1].Plan.ItemID != "alter" || preview.Items[2].Plan.ItemID != "initialize" {
		t.Fatalf("初始化早于前置DDL：%+v", preview.Items)
	}
}
