package tasks

import (
	"path/filepath"
	"testing"
)

func TestPersistRecoverAndIndependentRows(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tasks")
	repo, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	record := Record{ID: "test-task", Kind: "sql", Running: true, Status: "运行中", Rows: []Row{{ID: "script", State: "执行中", Children: []Row{{Title: "newlink", State: "成功"}, {Title: "newlink1", State: "执行中"}, {Title: "newlink2", State: "等待中"}}}}}
	if err = repo.Save(record); err != nil {
		t.Fatal(err)
	}
	original, _ := repo.Get(record.ID)
	original.Rows[0].Children[0].State = "修改副本"
	got, _ := repo.Get(record.ID)
	if got.Rows[0].Children[0].State != "成功" {
		t.Fatal("历史共享了可变结果")
	}
	recovered, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, _ = recovered.Get(record.ID)
	if got.Running || got.Status != "已中断" || got.Rows[0].Children[0].State != "成功" || got.Rows[0].Children[1].State != "已中断" || got.Rows[0].Children[2].State != "未开始" {
		t.Fatalf("恢复错误：%+v", got)
	}
	if err = recovered.Save(record); err == nil {
		t.Fatal("迟到进度重新激活了已中断任务")
	}
	if err = repo.Save(Record{ID: "../escape"}); err == nil {
		t.Fatal("接受路径注入")
	}
}
