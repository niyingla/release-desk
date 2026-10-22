package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	aiservice "release-desk/internal/ai"
	"release-desk/internal/storage/workspace"
)

// TestScanCancellationDiscardsLateSuccess 确保取消后丢弃迟到结果，后台退出前保持任务占用。
func TestScanCancellationDiscardsLateSuccess(t *testing.T) {
	s, err := workspace.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	v := sampleVersion()
	if err = s.Save(v); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Dir, "版本文件", v.Version.Name+".json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	// 模拟忽略取消的扫描器，验证即使它迟到返回成功也不能覆盖已有记录。
	if err = (&aiservice.Service{Store: s}).StartVersionScan(v.Version.Name, func(_ context.Context, _ workspace.VersionFile, _ workspace.Settings) (aiservice.ScanResult, error) {
		close(started)
		<-release
		return scanFixture(), nil
	}); err != nil {
		t.Fatal(err)
	}
	<-started
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
		waitForScan(t, s)
	}()
	a := &App{Store: s}
	cancel := a.handlers()["ai/scan/cancel"]
	if cancel == nil {
		t.Fatal("缺少独立终止扫描入口")
	}
	s.Mu.Lock()
	data, _ := json.Marshal(s.Scan)
	s.Mu.Unlock()
	var state map[string]any
	if err = json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if state["id"] == nil || state["id"] == "" || state["phase"] == "" {
		t.Fatal("扫描状态缺少稳定任务ID或阶段")
	}
	body, _ := json.Marshal(map[string]any{"id": state["id"]})
	if _, err = cancel(body); err != nil {
		t.Fatal(err)
	}
	s.Mu.Lock()
	busy := s.AIBusy
	s.Mu.Unlock()
	if !busy {
		t.Fatal("后台未退出就释放了扫描占用")
	}
	if err = (&aiservice.Service{Store: s}).StartVersionScan(v.Version.Name, nil); err == nil {
		t.Fatal("终止收尾期间启动了新任务")
	}
	close(release)
	finished := waitForScan(t, s)
	encoded, _ := json.Marshal(finished)
	if err = json.Unmarshal(encoded, &state); err != nil {
		t.Fatal(err)
	}
	if state["cancelled"] != true || finished.Error != "" || len(finished.Counts) != 0 {
		t.Fatalf("人工终止被误报成功或失败：%s", encoded)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("人工终止仍写回了迟到的扫描结果")
	}
	if s.TaskContext.Err() != nil {
		t.Fatal("终止扫描取消了整个软件的任务上下文")
	}
	if err = (&aiservice.Service{Store: s}).StartVersionScan(v.Version.Name, func(_ context.Context, _ workspace.VersionFile, _ workspace.Settings) (aiservice.ScanResult, error) {
		return scanFixture(), nil
	}); err != nil {
		t.Fatal(err)
	}
	if retried := waitForScan(t, s); retried.Error != "" || retried.Counts["job"] != 1 {
		t.Fatalf("终止后不能正常重试：%+v", retried)
	}
}

// TestScanCancellationRejectsStaleTask 确保旧弹窗不能终止另一轮扫描或其他 AI 任务。
func TestScanCancellationRejectsStaleTask(t *testing.T) {
	s, err := workspace.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &App{Store: s}
	cancel := a.handlers()["ai/scan/cancel"]
	if cancel == nil {
		t.Fatal("缺少独立终止扫描入口")
	}
	s.AIBusy = true
	if _, err = cancel([]byte(`{"id":"old-scan"}`)); err == nil {
		t.Fatal("没有扫描时取消了其他AI任务")
	}
	if !s.AIBusy || s.TaskContext.Err() != nil {
		t.Fatal("取消不存在的扫描影响其他任务")
	}
}

// TestScanCancellationReachesRunner 确保终止真正传递到后台扫描器，而非只改变界面状态。
func TestScanCancellationReachesRunner(t *testing.T) {
	s, err := workspace.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Save(sampleVersion()); err != nil {
		t.Fatal(err)
	}
	started, canceled := make(chan struct{}), make(chan struct{})
	if err = (&aiservice.Service{Store: s}).StartVersionScan("5.7.3", func(ctx context.Context, _ workspace.VersionFile, _ workspace.Settings) (aiservice.ScanResult, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		return aiservice.ScanResult{}, ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	<-started
	defer s.CancelTasks()
	a := &App{Store: s}
	stop := a.handlers()["ai/scan/cancel"]
	if _, err = stop([]byte(`{"id":"stale-scan"}`)); err == nil {
		t.Fatal("旧扫描ID取消了当前任务")
	}
	select {
	case <-canceled:
		t.Fatal("旧扫描ID取消了当前runner")
	default:
	}
	s.Mu.Lock()
	id := s.Scan.ID
	s.Mu.Unlock()
	body, _ := json.Marshal(map[string]string{"id": id})
	if _, err = stop(body); err != nil {
		t.Fatal(err)
	}
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("终止没有传递到后台runner")
	}
	if result := waitForScan(t, s); !result.Cancelled {
		t.Fatalf("取消未进入终态：%+v", result)
	}
	if s.TaskContext.Err() != nil {
		t.Fatal("单次取消破坏了全局上下文")
	}
}
