package app

import (
	"context"
	"testing"

	aiservice "release-desk/internal/ai"
	"release-desk/internal/storage/workspace"
	"release-desk/internal/tasks"
)

func TestUnifiedScanStopHistorySurvivesRestart(t *testing.T) {
	s, err := workspace.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	v := sampleVersion()
	if err = s.Save(v); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	ai := &aiservice.Service{Store: s}
	if err = ai.StartVersionScan(v.Version.Name, func(ctx context.Context, _ workspace.VersionFile, _ workspace.Settings) (aiservice.ScanResult, error) {
		close(entered)
		<-ctx.Done()
		return aiservice.ScanResult{}, ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	<-entered
	s.Mu.Lock()
	id := s.Scan.ID
	s.Mu.Unlock()
	if err = s.StopTask("old-task"); err == nil {
		t.Fatal("旧任务能够停止当前扫描")
	}
	if err = s.StopTask(id); err != nil {
		t.Fatal(err)
	}
	waitForScan(t, s)
	record, ok := s.TaskHistory.Get(id)
	if !ok || record.Running || record.Status != "已停止" {
		t.Fatalf("扫描终态未保存：%+v", record)
	}
	restarted, err := workspace.OpenStore(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	record, ok = restarted.TaskHistory.Get(id)
	if !ok || record.Status != "已停止" {
		t.Fatal("重启丢失任务结果")
	}
	if err = restarted.StopTask(id); err == nil {
		t.Fatal("历史任务仍可停止")
	}
}

func TestSingleVerificationCancellationPreservesPreviousConclusion(t *testing.T) {
	s, err := workspace.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	v := sampleVersion()
	v.Items = []workspace.Item{{ID: "verify1", Kind: "verify", Service: "service", Data: map[string]string{"title": "变更", "change": "变更", "keywords": "关键词", "expected": "预期"}, VerifyState: "通过"}}
	if err = s.Save(v); err != nil {
		t.Fatal(err)
	}
	_, item, _, err := s.Find(v.Version.Name, "verify1")
	if err != nil {
		t.Fatal(err)
	}
	s.SingleVerify = &workspace.VerificationTask{ID: "single-verification", Version: v.Version.Name, ItemID: item.ID, Running: true, CancelRequested: true}
	s.AIBusy = true
	ai := &aiservice.Service{Store: s}
	err = ai.FinishVerification(context.Background(), aiservice.VerifyRequest{Version: v.Version.Name, ID: item.ID}, item, workspace.VerifyResult{Status: "通过", Summary: "迟到结果"})
	if err == nil {
		t.Fatal("停止后仍接受迟到证据")
	}
	ai.CompleteSingleVerify(workspace.VerifyResult{Status: "已停止"})
	got := s.Versions[v.Version.Name].Items[0]
	if got.VerifyState != "通过" || len(got.VerifyRuns) != 0 || s.AIBusy {
		t.Fatal("取消覆盖旧结论或遗留占用")
	}
	record, _ := s.TaskHistory.Get("single-verification")
	if record.Status != "已停止" {
		t.Fatalf("单项停止没有历史：%+v", record)
	}
	copy := record
	copy.ID = "interrupted"
	copy.Running = true
	copy.Status = "运行中"
	copy.Rows = []tasks.Row{{Title: "未完成", State: "验证中"}}
	if err = s.TaskHistory.Save(copy); err != nil {
		t.Fatal(err)
	}
}
