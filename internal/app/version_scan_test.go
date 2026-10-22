package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	aiservice "release-desk/internal/ai"
	sqlservice "release-desk/internal/sql"
	"release-desk/internal/storage/workspace"
)

func scanFixture() aiservice.ScanResult {
	return aiservice.ScanResult{Summary: "5.7.3，huohuo；基线 abc，HEAD def", Warnings: []string{"频率需要人工确认"}, Items: []aiservice.ScanItem{{ID: "job1", Kind: "job", Action: "修改", Service: "job-service", Fields: []aiservice.ScanField{{Name: "handler", Value: "syncJob"}, {Name: "description", Value: "扫描后的真实说明"}}, Evidence: []string{"def job/SyncJob.java#syncJob"}}}}
}

func waitForScan(t *testing.T, s *workspace.Store) workspace.VersionScanState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.Mu.Lock()
		if s.Scan != nil && !s.Scan.Running {
			result := workspace.Clone(*s.Scan)
			s.Mu.Unlock()
			return result
		}
		s.Mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("模拟扫描未完成")
	return workspace.VersionScanState{}
}

func TestVersionScanPreservesManualStateAndHistory(t *testing.T) {
	s, err := workspace.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	v := sampleVersion()
	v.Version.Archived = true
	v.Version.Status = "已发布"
	v.Items[0].Online = "已上线"
	v.Items[0].Status = "已完成"
	if err = s.Save(v); err != nil {
		t.Fatal(err)
	}
	s.History = []workspace.Execution{{ID: "audit1", Version: v.Version.Name, State: "成功"}}
	if err = s.PersistHistory(); err != nil {
		t.Fatal(err)
	}
	if err = (&aiservice.Service{Store: s}).StartVersionScan(v.Version.Name, func(_ context.Context, snapshot workspace.VersionFile, config workspace.Settings) (aiservice.ScanResult, error) {
		if config.GitAuthor != "huohuo" || !snapshot.Version.Archived {
			return aiservice.ScanResult{}, errors.New("输入范围错误")
		}
		return scanFixture(), nil
	}); err != nil {
		t.Fatal(err)
	}
	result := waitForScan(t, s)
	if result.Error != "" || result.Counts["job"] != 1 {
		t.Fatalf("扫描失败：%+v", result)
	}
	s.Mu.Lock()
	current := workspace.Clone(s.Versions[v.Version.Name])
	busy := s.AIBusy
	s.Mu.Unlock()
	if busy || !current.Version.Archived || current.Version.Status != "已发布" || current.Items[0].Online != "已上线" || current.Items[0].Status != "已完成" || current.Items[0].Data["description"] != "扫描后的真实说明" {
		t.Fatalf("状态或内容丢失：%+v", current)
	}
	restarted, err := workspace.OpenStore(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(restarted.History) != 1 || len(restarted.Versions[v.Version.Name].Items) != 1 {
		t.Fatal("持久化或审计丢失")
	}
}

func TestVersionScanFailureKeepsFileAndCanRetry(t *testing.T) {
	for _, cause := range []string{"runner", "evidence", "field", "external"} {
		t.Run(cause, func(t *testing.T) {
			s, err := workspace.OpenStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			v := sampleVersion()
			if err = s.Save(v); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(s.Dir, "版本文件", v.Version.Name+".json")
			before, _ := os.ReadFile(path)
			if err = (&aiservice.Service{Store: s}).StartVersionScan(v.Version.Name, func(_ context.Context, _ workspace.VersionFile, _ workspace.Settings) (aiservice.ScanResult, error) {
				r := scanFixture()
				switch cause {
				case "runner":
					return r, errors.New("模拟 CLI 超时")
				case "evidence":
					r.Items[0].Evidence = []string{" "}
				case "field":
					r.Items[0].Fields = append(r.Items[0].Fields, aiservice.ScanField{Name: "online", Value: "已上线"})
				case "external":
					if err := os.WriteFile(path, append(before, '\n'), 0600); err != nil {
						return r, err
					}
				}
				return r, nil
			}); err != nil {
				t.Fatal(err)
			}
			result := waitForScan(t, s)
			if result.Error == "" {
				t.Fatal("无效候选未拒绝")
			}
			after, _ := os.ReadFile(path)
			expected := string(before)
			if cause == "external" {
				expected += "\n"
			}
			if string(after) != expected {
				t.Fatal("失败扫描覆盖了原文件")
			}
			s.Mu.Lock()
			busy := s.AIBusy
			s.Mu.Unlock()
			if busy {
				t.Fatal("失败未释放扫描占用")
			}
			if cause == "external" {
				s.ScanFiles()
			}
			if err = (&aiservice.Service{Store: s}).StartVersionScan(v.Version.Name, func(_ context.Context, _ workspace.VersionFile, _ workspace.Settings) (aiservice.ScanResult, error) {
				return scanFixture(), nil
			}); err != nil {
				t.Fatal(err)
			}
			if retry := waitForScan(t, s); retry.Error != "" {
				t.Fatalf("重试失败：%+v", retry)
			}
		})
	}
}

func TestVersionScanRejectsBusyAndUnknownSQL(t *testing.T) {
	s, err := workspace.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Save(sampleVersion()); err != nil {
		t.Fatal(err)
	}
	s.Batch = &workspace.Batch{Running: true}
	if err = (&aiservice.Service{Store: s}).StartVersionScan("5.7.3", nil); err == nil {
		t.Fatal("与 SQL 并发启动")
	}
	s.Batch = nil
	s.AIBusy = true
	if err = (&aiservice.Service{Store: s}).StartVersionScan("5.7.3", nil); err == nil {
		t.Fatal("与 Codex 并发启动")
	}
	// 临时 Git 目录无需数据库连接，无来源路径的 SQL 必须被拒绝。
	candidate := sampleVersion()
	candidate.Items = []workspace.Item{{ID: "s", Kind: "sql", Data: map[string]string{"name": "fake", "source_path": "missing.sql"}}}
	config := workspace.DefaultSettings()
	config.SQLRepository = t.TempDir()
	if err = aiservice.ResolveScanSQL(&candidate, config); err == nil {
		t.Fatal("接受了模型编造的 SQL")
	}
}

func TestSQLConfirmationUsesOnlyCheckbox(t *testing.T) {
	a := &sqlservice.Service{Plans: map[string]sqlservice.Plan{"p": {Created: time.Now()}}}
	_, err := a.DBExecute([]byte(`{"token":"p","confirm":false}`))
	if err == nil || !strings.Contains(err.Error(), "勾选") {
		t.Fatal("未勾选确认时没有拒绝")
	}
	// 已勾选应通过布尔字段解码并进入已有预览令牌校验，不再接受文字确认。
	_, err = a.DBExecute([]byte(`{"token":"expired","confirm":true}`))
	if err == nil || !strings.Contains(err.Error(), "过期") {
		t.Fatalf("勾选请求未按布尔协议解析：%v", err)
	}
	_, err = a.DBExecute([]byte(`{"token":"expired","confirm":"执行"}`))
	if err == nil {
		t.Fatal("旧的文字确认协议仍被接受")
	}
}

func TestVersionScanSchemaAndPrompt(t *testing.T) {
	data, err := assets.ReadFile("prompts/version-scan-schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err = json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	prompt, err := aiservice.VersionScanPrompt(sampleVersion(), workspace.DefaultSettings())
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"huohuo", "5.7.3", "只返回", "禁止数据库", "release-desk-version-scan"} {
		if !strings.Contains(prompt, text) {
			t.Errorf("提示词缺少 %s", text)
		}
	}
}
