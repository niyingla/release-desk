package sqlservice

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"release-desk/internal/identity"
	"release-desk/internal/storage/workspace"
)

// TestRetiredSQLPlansStopBeforeConnection 验证执行、重试及核验都在读取连接前拒绝退役脚本。
// 参数 t：隔离存储，不配置数据库，因此测试不会连接真实或虚构服务器。
func TestRetiredSQLPlansStopBeforeConnection(t *testing.T) {
	store, err := workspace.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.CancelTasks()
	service := &Service{Store: store}
	for _, item := range []workspace.Item{
		{ID: "sql", Kind: "sql", Data: map[string]string{"name": "历史初始化【已退役，勿执行】", "sql": "INSERT INTO demo(id) VALUES(1);", "side": "服务商端"}},
		{ID: "sql", Kind: "sql", Data: map[string]string{"name": "历史初始化", "lifecycle": "已退役", "sql": "INSERT INTO demo(id) VALUES(1);", "side": "服务商端"}},
	} {
		version := workspace.VersionFile{Format: "release-desk-version", Schema: 1, Version: workspace.Version{Name: "qa-retired"}, Items: []workspace.Item{item}}
		if err = store.Save(version); err != nil {
			t.Fatal(err)
		}
		for _, environment := range []string{"test", "uat"} {
			for _, mode := range [][2]bool{{false, false}, {true, false}, {false, true}} {
				if _, err = service.Plan("qa-retired", "sql", mode[0], environment, mode[1]); err == nil || !strings.Contains(err.Error(), "已退役") {
					t.Errorf("退役 SQL 未在连接前被拒绝：环境=%s，模式=%v，错误=%v", environment, mode, err)
				}
			}
		}
	}
}

// TestRetiredSQLBatchPreviewRejectsAllBeforeDiscovery 验证混合选择不会先发现正常脚本的目标。
// 参数 t：目标规划器仅计数，不访问数据库或生成执行任务。
func TestRetiredSQLBatchPreviewRejectsAllBeforeDiscovery(t *testing.T) {
	items := []workspace.Item{
		{ID: "active", Kind: "sql", Data: map[string]string{"name": "建表", "sql": "CREATE TABLE demo(id bigint);", "order": "1"}},
		{ID: "retired", Kind: "sql", Data: map[string]string{"name": "旧初始化【已退役，勿执行】", "sql": "INSERT INTO demo(id) VALUES(1);", "order": "2"}},
	}
	calls := 0
	_, err := BuildSQLQueuePreview("qa-retired", items, func(id string) (Plan, error) {
		calls++
		return Plan{Environment: "test", ItemID: id}, nil
	})
	if err == nil || !strings.Contains(err.Error(), "已退役") || calls != 0 {
		t.Fatalf("混合预览未整体拒绝：调用次数=%d，错误=%v", calls, err)
	}
}

// TestSQLRetirementInvalidatesExistingQueue 验证正文未变但后来退役时，旧预览不能启动任何脚本。
// 参数 t：逐库函数为内存替身，仅观察真实队列是否错误进入执行。
func TestSQLRetirementInvalidatesExistingQueue(t *testing.T) {
	store, err := workspace.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.CancelTasks()
	service := &Service{Store: store}
	config := DBConfig{Environment: "test", Host: "qa.invalid", Port: 3306, User: "qa", ScopeRules: workspace.DefaultSettings().ScopeRules}
	version := workspace.VersionFile{Format: "release-desk-version", Schema: 1, Version: workspace.Version{Name: "qa-retired"}}
	preview := SQLQueuePreview{Environment: "test", Version: version.Version.Name}
	for _, id := range []string{"first", "second"} {
		item := workspace.Item{ID: id, Kind: "sql", Data: map[string]string{"name": id, "sql": "INSERT INTO demo(id) VALUES(1);", "side": "服务商端"}}
		version.Items = append(version.Items, item)
		preview.Items = append(preview.Items, SQLPlannedItem{Plan: Plan{Environment: "test", Version: version.Version.Name, ItemID: id, SQL: item.Data["sql"], Digest: identity.Digest(item.Data["sql"]), Side: "服务商端", Server: ServerID(config), Databases: []string{"sp"}}})
	}
	if err = store.Save(version); err != nil {
		t.Fatal(err)
	}
	if _, err = service.ValidateSQLPlan(preview.Items[1].Plan, config); err != nil {
		t.Fatal("正常预览不应被拒绝", err)
	}
	version.Items[1].Data["lifecycle"] = "已退役"
	if err = store.Save(version); err != nil {
		t.Fatal(err)
	}
	if _, err = service.ValidateSQLPlan(preview.Items[1].Plan, config); err == nil || !strings.Contains(err.Error(), "已退役") {
		t.Error("退役后仍接受旧单条预览", err)
	}
	var calls atomic.Int32
	err = service.StartSQLQueue(preview, config, func(context.Context, DBConfig, string, []string) (string, error) {
		calls.Add(1)
		return "模拟结果", nil
	})
	// 旧实现会错误启动协程；等待其收尾后再报告，避免临时目录与后台落盘竞争。
	if err == nil {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			store.Mu.Lock()
			running := store.SQLActive()
			store.Mu.Unlock()
			if !running {
				break
			}
			time.Sleep(time.Millisecond)
		}
	}
	if err == nil || !strings.Contains(err.Error(), "已退役") || calls.Load() != 0 {
		t.Fatalf("退役后仍可启动旧批次：调用次数=%d，错误=%v", calls.Load(), err)
	}
}
