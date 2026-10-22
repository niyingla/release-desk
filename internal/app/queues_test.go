package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	aiservice "release-desk/internal/ai"
	"release-desk/internal/identity"
	sqlservice "release-desk/internal/sql"
	"release-desk/internal/storage/workspace"
)

func queueSQLFixture(t *testing.T) (*workspace.Store, sqlservice.DBConfig, sqlservice.SQLQueuePreview) {
	t.Helper()
	s, err := workspace.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	v := sampleVersion()
	v.Items = nil
	for i, sql := range []string{"INSERT INTO demo VALUES(1);", "ALTER TABLE demo ADD enabled INT;", "CREATE TABLE demo(id INT);"} {
		v.Items = append(v.Items, workspace.Item{ID: fmt.Sprint("sql", i), Kind: "sql", Data: map[string]string{"name": fmt.Sprint("脚本", i), "sql": sql, "side": "服务商端", "order": "1", "source_path": "误导目录/新增表.sql"}})
	}
	if err = s.Save(v); err != nil {
		t.Fatal(err)
	}
	cfg := sqlservice.DBConfig{Environment: "test", Host: "fake", Port: 3306, ScopeRules: workspace.DefaultSettings().ScopeRules}
	preview, err := sqlservice.BuildSQLQueuePreview(v.Version.Name, workspace.Clone(v.Items), func(id string) (sqlservice.Plan, error) {
		_, item, _, err := s.Find(v.Version.Name, id)
		if err != nil {
			return sqlservice.Plan{}, err
		}
		return sqlservice.Plan{Version: v.Version.Name, ItemID: id, SQL: item.Data["sql"], Digest: identity.Digest(item.Data["sql"]), Side: item.Data["side"], Server: sqlservice.ServerID(cfg), Databases: []string{"sp", "sp1"}, Skipped: []string{}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, cfg, preview
}

func waitSQLQueue(t *testing.T, s *workspace.Store) workspace.SQLQueue {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.Mu.Lock()
		if s.SQLQueue != nil && !s.SQLQueue.Running {
			q := workspace.Clone(*s.SQLQueue)
			s.Mu.Unlock()
			return q
		}
		s.Mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("SQL 模拟队列未完成")
	return workspace.SQLQueue{}
}

func TestSQLQueueUsesStatementOrderAndContinuesAfterFailure(t *testing.T) {
	s, cfg, preview := queueSQLFixture(t)
	visited := []string{}
	err := (&sqlservice.Service{Store: s}).StartSQLQueue(preview, cfg, func(_ context.Context, _ sqlservice.DBConfig, name string, parts []string) (string, error) {
		kind := strings.Fields(parts[0])[0]
		visited = append(visited, kind+":"+name)
		if kind == "ALTER" && name == "sp" {
			return "", errors.New("模拟业务错误")
		}
		return "完成", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	q := waitSQLQueue(t, s)
	if strings.Join(visited, ",") != "CREATE:sp,CREATE:sp1,ALTER:sp,ALTER:sp1,INSERT:sp,INSERT:sp1" {
		t.Fatalf("执行顺序或失败继续错误：%v", visited)
	}
	if q.Rows[1].State != "有失败" || q.Rows[2].State != "成功" || len(s.History) != 6 || len(q.Rows[0].Executions) != 2 {
		t.Fatalf("逐脚本逐库结果不完整：%+v", q)
	}
	restarted, err := workspace.OpenStore(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(restarted.History) != 6 {
		t.Fatal("批量执行审计未持久化")
	}
}

func TestSQLQueueRejectsEntireBatchOnChangedSource(t *testing.T) {
	s, cfg, preview := queueSQLFixture(t)
	preview.Items[1].Plan.Digest = "changed"
	called := false
	err := (&sqlservice.Service{Store: s}).StartSQLQueue(preview, cfg, func(_ context.Context, _ sqlservice.DBConfig, _ string, _ []string) (string, error) {
		called = true
		return "", nil
	})
	if err == nil || called || len(s.History) != 0 || s.SQLQueue != nil {
		t.Fatal("来源变化后仍开始了部分脚本")
	}
}

func TestSQLQueueStopDoesNotExecuteLaterScripts(t *testing.T) {
	s, cfg, preview := queueSQLFixture(t)
	started := make(chan struct{})
	err := (&sqlservice.Service{Store: s}).StartSQLQueue(preview, cfg, func(ctx context.Context, _ sqlservice.DBConfig, _ string, _ []string) (string, error) {
		close(started)
		<-ctx.Done()
		return "", &sqlservice.DatabaseFailure{Err: ctx.Err(), Uncertain: true}
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	s.Mu.Lock()
	id := s.SQLQueue.ID
	s.Mu.Unlock()
	a := &App{Store: s}
	if _, err = a.services().SQL.CancelSQLBatch([]byte(`{"id":"` + id + `"}`)); err != nil {
		t.Fatal(err)
	}
	q := waitSQLQueue(t, s)
	if len(s.History) != 1 || s.History[0].State != "已中断" || q.Rows[1].State != "未执行" || q.Rows[2].State != "未执行" {
		t.Fatalf("取消后仍执行后续目标或错误记录成功：%+v", q)
	}
}

func TestSQLPreviewIncludesSkippedItemsAndRejectsWrongSelection(t *testing.T) {
	s, _, preview := queueSQLFixture(t)
	_, err := s.SelectedItems(workspace.SelectionRequest{Version: preview.Version, IDs: []string{"sql0", "sql0"}}, "sql")
	if err == nil {
		t.Fatal("重复 SQL 选择没有拒绝")
	}
	_, err = s.SelectedItems(workspace.SelectionRequest{Version: preview.Version, IDs: []string{"sql0"}}, "verify")
	if err == nil {
		t.Fatal("SQL 被当成日志验证事项")
	}
	_, err = s.SelectedItems(workspace.SelectionRequest{Version: preview.Version}, "sql")
	if err == nil {
		t.Fatal("空批次没有拒绝")
	}
	items := workspace.Clone(s.Versions[preview.Version].Items)
	p, err := sqlservice.BuildSQLQueuePreview(preview.Version, items, func(id string) (sqlservice.Plan, error) {
		return sqlservice.Plan{ItemID: id, Skipped: []string{"sp"}}, sqlservice.ErrNoSQLTargets
	})
	if err != nil || len(p.Items) != 3 {
		t.Fatal("已成功脚本阻断了整批预览")
	}
}

func queueVerifyFixture(t *testing.T) (*workspace.Store, aiservice.VerifyQueueRequest) {
	t.Helper()
	s, err := workspace.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	v := sampleVersion()
	v.Items = nil
	ids := []string{"v1", "v2", "v3", "v4"}
	for _, id := range ids {
		v.Items = append(v.Items, workspace.Item{ID: id, Kind: "verify", Service: "qa-service", Data: map[string]string{"title": id, "change": "修改", "steps": "观察日志", "keywords": "真实检索词", "expected": "真实预期"}})
	}
	v.Items[2].Data["expected"] = ""
	// 仅保留修复描述，缺少全部预填线索仍应交由 AI 选择核验路径。
	v.Items[2].Service = ""
	v.Items[2].Data["keywords"] = ""
	v.Items[2].Data["steps"] = ""
	if err = s.Save(v); err != nil {
		t.Fatal(err)
	}
	zone := time.FixedZone("Asia/Shanghai", 8*60*60)
	end := time.Now().In(zone).Add(-time.Minute)
	return s, aiservice.VerifyQueueRequest{Version: v.Version.Name, IDs: ids, Start: end.Add(-time.Hour).Format("2006-01-02T15:04"), End: end.Format("2006-01-02T15:04")}
}

func waitVerifyQueue(t *testing.T, s *workspace.Store) workspace.VerifyQueue {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.Mu.Lock()
		if s.VerifyQueue != nil && !s.VerifyQueue.Running {
			q := workspace.Clone(*s.VerifyQueue)
			s.Mu.Unlock()
			return q
		}
		s.Mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("验收模拟队列未完成")
	return workspace.VerifyQueue{}
}

func TestVerifyQueueContinuesAndSavesEachIndependentResult(t *testing.T) {
	s, req := queueVerifyFixture(t)
	called := []string{}
	var calledMu sync.Mutex
	err := (&aiservice.Service{Store: s}).StartVerifyQueue(req, func(_ context.Context, r aiservice.VerifyRequest, _ workspace.Item, _ workspace.Settings) (workspace.VerifyResult, error) {
		calledMu.Lock()
		called = append(called, r.ID)
		calledMu.Unlock()
		if r.ID == "v2" {
			return workspace.VerifyResult{}, errors.New("模拟 CLI 失败")
		}
		return workspace.VerifyResult{Status: "通过", Summary: "有真实日志证据", Environment: "生产", Evidence: []string{"模拟来源"}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	q := waitVerifyQueue(t, s)
	// 并发完成不约束调用先后，仍应恰好处理每个稳定 ID 一次。
	sort.Strings(called)
	if strings.Join(called, ",") != "v1,v2,v3,v4" || q.Rows[0].State != "通过" || q.Rows[1].State != "证据不足" || q.Rows[2].State != "通过" || q.Rows[3].State != "通过" {
		t.Fatalf("验收未按项失败继续：%+v", q)
	}
	restarted, err := workspace.OpenStore(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	items := restarted.Versions[req.Version].Items
	if len(items[0].VerifyRuns) != 1 || len(items[1].VerifyRuns) != 1 || len(items[2].VerifyRuns) != 1 || len(items[3].VerifyRuns) != 1 || s.AIBusy {
		t.Fatal("验收历史丢失或未释放占用")
	}
}

func TestVerifyQueueCancelPreservesCompletedResults(t *testing.T) {
	s, req := queueVerifyFixture(t)
	started := make(chan struct{})
	err := (&aiservice.Service{Store: s}).StartVerifyQueue(req, func(ctx context.Context, r aiservice.VerifyRequest, _ workspace.Item, _ workspace.Settings) (workspace.VerifyResult, error) {
		if r.ID == "v2" {
			close(started)
			<-ctx.Done()
			return workspace.VerifyResult{}, ctx.Err()
		}
		return workspace.VerifyResult{Status: "通过", Summary: "前序结果", Environment: "生产", Evidence: []string{"来源"}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	// 首项保存完成后再停止，断言保护已完成结果，不假定其他工作者的完成顺序。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s.Mu.Lock()
		done := s.VerifyQueue.Rows[0].State == "通过"
		s.Mu.Unlock()
		if done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.Mu.Lock()
	id := s.VerifyQueue.ID
	s.Mu.Unlock()
	a := &App{Store: s}
	if _, err = a.services().AI.CancelVerifyBatch([]byte(`{"id":"` + id + `"}`)); err != nil {
		t.Fatal(err)
	}
	q := waitVerifyQueue(t, s)
	if q.Rows[0].State != "通过" || q.Rows[1].State != "已停止" || len(s.Versions[req.Version].Items[0].VerifyRuns) != 1 {
		t.Fatal("取消后覆盖了前序结果或继续调用后续项")
	}
}

func TestSQLQueueRetainsExclusiveAccessBetweenScripts(t *testing.T) {
	s, _, _ := queueSQLFixture(t)
	s.SQLQueue = &workspace.SQLQueue{Running: true}
	s.Batch = &workspace.Batch{Running: false}
	a := &App{Store: s}
	if a.Store.EnsureIdle() == nil {
		t.Fatal("脚本切换间隙释放了 SQL 队列占用")
	}
	if err := (&aiservice.Service{Store: s}).StartVersionScan("5.7.3", nil); err == nil {
		t.Fatal("脚本切换时启动了扫描")
	}
}
