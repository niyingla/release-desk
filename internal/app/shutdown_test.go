package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	aiservice "release-desk/internal/ai"
	"release-desk/internal/platform/process"
	sqlservice "release-desk/internal/sql"
	"release-desk/internal/storage/workspace"
)

func TestClosingCancelsScanWithoutWritingVersion(t *testing.T) {
	s, err := workspace.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	v := sampleVersion()
	if err = s.Save(v); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	if err = (&aiservice.Service{Store: s}).StartVersionScan(v.Version.Name, func(_ context.Context, _ workspace.VersionFile, _ workspace.Settings) (aiservice.ScanResult, error) {
		close(started)
		<-s.TaskContext.Done()
		return scanFixture(), nil
	}); err != nil {
		t.Fatal(err)
	}
	<-started
	s.StopWindowTasks()
	s.Mu.Lock()
	result := workspace.Clone(*s.Scan)
	current := workspace.Clone(s.Versions[v.Version.Name])
	s.Mu.Unlock()
	// 主动退出采用已终止标记，不把用户取消伪装成扫描失败。
	if result.Running || !result.Cancelled || current.Items[0].Data["description"] != "" {
		t.Fatal("退出未取消扫描或取消后仍写入了版本")
	}
	if err = (&aiservice.Service{Store: s}).StartVersionScan(v.Version.Name, nil); err == nil {
		t.Fatal("退出后仍接受新扫描")
	}
}

func TestClosingCancelsCurrentDatabaseAndStopsRemainingTargets(t *testing.T) {
	s, err := workspace.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	batch := sqlservice.NewBatch(sqlservice.Plan{Version: "qa", ItemID: "sql", Databases: []string{"sp1", "sp2"}})
	s.Batch = batch
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		(&sqlservice.Service{Store: s}).RunBatch(batch, sqlservice.DBConfig{}, []string{"SELECT 1;"}, func(_ sqlservice.DBConfig, _ string, _ []string) (string, error) {
			close(started)
			<-s.TaskContext.Done()
			return "", &sqlservice.DatabaseFailure{Err: context.Canceled, Uncertain: true}
		})
	}()
	<-started
	s.StopWindowTasks()
	<-done
	if len(s.History) != 1 || s.History[0].State != "已中断" || batch.Running {
		t.Fatal("退出后目标仍继续执行或审计未保留中断语义")
	}
	restarted, err := workspace.OpenStore(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(restarted.History) != 1 || restarted.History[0].State != "已中断" {
		t.Fatal("中断审计未持久化")
	}
}

func TestManagedCommandCancellationStopsProcessTree(t *testing.T) {
	// 使用测试自身作为进程树，不启动真实 Codex，也不修改用户其他进程。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pidPath := filepath.Join(t.TempDir(), "child.pid")
	cmd := process.Command(ctx, os.Args[0], "-test.run=^TestManagedCommandHelper$")
	cmd.Env = append(os.Environ(), "RELEASE_DESK_TEST_HELPER=parent", "RELEASE_DESK_TEST_PID="+pidPath)
	done := make(chan error, 1)
	go func() { done <- process.RunManagedCommand(cmd) }()
	var childPID int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(pidPath); err == nil {
			childPID, _ = strconv.Atoi(string(data))
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID == 0 {
		cancel()
		t.Fatal("测试子进程没有启动")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("取消未停止命令")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("命令取消后仍未返回")
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(childPID))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(process)
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		var exit uint32
		if err = windows.GetExitCodeProcess(process, &exit); err != nil {
			t.Fatal(err)
		}
		if exit != 259 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("CLI 子进程在退出后仍然存活")
}

func TestManagedCommandHelper(t *testing.T) {
	mode := os.Getenv("RELEASE_DESK_TEST_HELPER")
	if mode == "" {
		return
	}
	if mode == "parent" {
		child := process.Command(context.Background(), os.Args[0], "-test.run=^TestManagedCommandHelper$")
		child.Env = append(os.Environ(), "RELEASE_DESK_TEST_HELPER=child")
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		if err := os.WriteFile(os.Getenv("RELEASE_DESK_TEST_PID"), []byte(strconv.Itoa(child.Process.Pid)), 0600); err != nil {
			os.Exit(3)
		}
	}
	time.Sleep(time.Minute)
	os.Exit(0)
}
