package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	gitservice "release-desk/internal/git"
	"release-desk/internal/identity"
	sqlservice "release-desk/internal/sql"
	sqlscript "release-desk/internal/sql/script"
	"release-desk/internal/storage/fileio"
	"release-desk/internal/storage/workspace"
	"release-desk/internal/versioning"
)

func TestReleaseExample(t *testing.T) {
	// 示例只验证文件协议和 SQL 可解析性，不连接数据库、不执行脚本。
	content, err := os.ReadFile(filepath.Join("..", "..", "examples", "template.json"))
	if err != nil {
		t.Fatal(err)
	}
	var version workspace.VersionFile
	if err = json.Unmarshal(content, &version); err != nil {
		t.Fatal(err)
	}
	if err = workspace.ValidateVersion(&version); err != nil {
		t.Fatal(err)
	}
	for _, item := range version.Items {
		if item.Kind == "sql" {
			if _, err = sqlscript.SplitSQL(item.Data["sql"]); err != nil {
				t.Errorf("示例 SQL 无法预览执行：%s：%v", item.Data["name"], err)
			}
		}
	}
}

func TestBatchContinuesAfterOneDatabaseFails(t *testing.T) {
	store, err := workspace.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	plan := sqlservice.Plan{Version: "5.7.3", ItemID: "sql1", Digest: identity.Digest("SELECT 1;"), Server: "test:3306", Databases: []string{"sp", "sp1", "sp2"}}
	batch := sqlservice.NewBatch(plan)
	store.Batch = batch
	visited := []string{}
	(&sqlservice.Service{Store: store}).RunBatch(batch, sqlservice.DBConfig{}, []string{"SELECT 1;"}, func(_ sqlservice.DBConfig, name string, _ []string) (string, error) {
		visited = append(visited, name)
		if name == "sp1" {
			return "", errors.New("模拟业务 SQL 失败")
		}
		return "完成", nil
	})
	if len(visited) != 3 || batch.Rows[0].State != "成功" || batch.Rows[1].State != "失败" || batch.Rows[2].State != "成功" || batch.Running {
		t.Fatalf("批次未按预期继续：%+v", batch)
	}
	if len(store.History) != 3 {
		t.Fatal("逐库审计不完整")
	}
	state, _ := sqlservice.ExecutionOutcome("", &sqlservice.DatabaseFailure{Err: errors.New("模拟提交断线"), Uncertain: true}, time.Second)
	if state != "已中断" {
		t.Fatal("不确定提交不能自动作为普通失败重试")
	}
}

func sampleVersion() workspace.VersionFile {
	return workspace.VersionFile{Format: "release-desk-version", Schema: 1, Version: workspace.Version{Name: "5.7.3", Status: "计划中"}, Items: []workspace.Item{{ID: "job1", Kind: "job", Status: "待处理", Action: "新增", Data: map[string]string{"handler": "syncJob", "frequency": "0 0 * * * ?"}}}}
}

func TestStorePersistenceAndAIProtectsManualState(t *testing.T) {
	dir := t.TempDir()
	s, e := workspace.OpenStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	v := sampleVersion()
	v.Items[0].Online = "已上线"
	v.Items[0].Status = "已完成"
	s.Mu.Lock()
	e = s.Save(v)
	s.Mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	external := sampleVersion()
	external.Items[0].Data["description"] = "AI 更新说明"
	path := filepath.Join(dir, "版本文件", "5.7.3.json")
	if e = fileio.WriteJSON(path, external); e != nil {
		t.Fatal(e)
	}
	s.ScanFiles()
	r := s.Versions["5.7.3"].Items[0]
	if r.Online != "已上线" || r.Status != "已完成" || r.Data["description"] != "AI 更新说明" {
		t.Fatalf("状态或 AI 内容异常：%+v", r)
	}
	restarted, e := workspace.OpenStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	if restarted.Versions["5.7.3"].Items[0].Online != "已上线" {
		t.Fatal("重启后状态未保留")
	}
	if e = os.WriteFile(path, []byte("{invalid"), 0600); e != nil {
		t.Fatal(e)
	}
	s.ScanFiles()
	if len(s.Versions["5.7.3"].Items) != 1 {
		t.Fatal("无效文件损坏已有记录")
	}
}

func TestDatabaseScope(t *testing.T) {
	names := []string{"newlink", "newlink197", "newlink_share1", "newlink_pf", "newlink_view", "newlink_points", "sp", "sp1", "sp_view"}
	for side, count := range map[string]int{"租户端": 3, "服务商端": 2, "平台端": 1} {
		if got := sqlservice.MatchDB(names, side); len(got) != count {
			t.Fatalf("%s: %v", side, got)
		}
	}
}

func TestAIScanEntryRegistered(t *testing.T) {
	// 新增版本的 AI 扫描必须有独立接口，不能误用生产日志验证入口。
	if (&App{}).handlers()["ai/scan"] == nil {
		t.Fatal("版本扫描接口未注册")
	}
}

func TestVersionArchiveCanBeRestoredAndSurvivesAIUpdates(t *testing.T) {
	// 归档只改变导航可见性，不能丢失事项、执行审计或被 AI 更新撤销。
	dir := t.TempDir()
	store, err := workspace.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Save(sampleVersion()); err != nil {
		t.Fatal(err)
	}
	store.History = []workspace.Execution{{ID: "archive-history", Version: "5.7.3", State: "成功"}}
	if err = store.PersistHistory(); err != nil {
		t.Fatal(err)
	}
	app := &App{Store: store}
	handler := app.handlers()["version/archive"]
	if handler == nil {
		t.Fatal("归档入口未注册")
	}
	change := func(archived bool) {
		body, _ := json.Marshal(map[string]any{"name": "5.7.3", "archived": archived})
		if _, err = handler(body); err != nil {
			t.Fatal(err)
		}
	}
	isArchived := func(s *workspace.Store) bool {
		data, _ := json.Marshal(s.Versions["5.7.3"])
		var persisted struct {
			Version struct {
				Archived bool `json:"archived"`
			} `json:"version"`
		}
		if err := json.Unmarshal(data, &persisted); err != nil {
			t.Fatal(err)
		}
		return persisted.Version.Archived
	}
	change(true)
	if !isArchived(store) {
		t.Fatal("归档状态未保存")
	}
	// 普通版本设置和未携带归档字段的 AI 文件都不能隐式恢复版本。
	if _, err = app.services().Release.SaveVersion([]byte(`{"name":"5.7.3","status":"已发布"}`)); err != nil {
		t.Fatal(err)
	}
	if !isArchived(store) {
		t.Fatal("编辑版本设置撤销了归档")
	}
	if err = fileio.WriteJSON(filepath.Join(dir, "版本文件", "5.7.3.json"), sampleVersion()); err != nil {
		t.Fatal(err)
	}
	store.ScanFiles()
	if !isArchived(store) {
		t.Fatal("AI 更新撤销了归档")
	}
	restarted, err := workspace.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !isArchived(restarted) || len(restarted.Versions["5.7.3"].Items) != 1 || len(restarted.History) != 1 {
		t.Fatal("重启后归档、事项或审计丢失")
	}
	// 恢复使用同一条记录，不重新创建版本或复制事项。
	change(false)
	if isArchived(store) || len(store.Versions["5.7.3"].Items) != 1 || len(store.History) != 1 {
		t.Fatal("恢复版本破坏了原始记录")
	}
}

func TestSQLValidation(t *testing.T) {
	for _, sql := range []string{"USE newlink; SELECT 1;", "UPDATE newlink197.t SET a=1;", "DROP DATABASE newlink;", "PREPARE s FROM @x;", "/*!50000 DROP TABLE t */;", "SET GLOBAL max_connections=100;"} {
		if _, e := sqlscript.SplitSQL(sql); e == nil {
			t.Errorf("危险语句未拒绝：%s", sql)
		}
	}
	parts, e := sqlscript.SplitSQL("ALTER TABLE t ADD a INT COMMENT '分号;测试';\nUPDATE t SET a=1;")
	if e != nil || len(parts) != 2 {
		t.Fatalf("有效 SQL 拆分失败：%v %v", parts, e)
	}
}

func TestVersionSort(t *testing.T) {
	if versioning.CompareVersion("5.7.10", "5.7.9") <= 0 || versioning.CompareVersion("5.7.3", "5.7.2") <= 0 {
		t.Fatal("版本未按数字排序")
	}
}

func TestSQLRefreshKeepsHistoryAndProtectsManualEdits(t *testing.T) {
	// 刷新测试只比较本地快照与摘要，不 fetch、不访问数据库。
	store, err := workspace.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	version := sampleVersion()
	oldSQL := "UPDATE demo SET enabled=1;"
	version.Items = []workspace.Item{{ID: "sql-refresh", Kind: "sql", Data: map[string]string{"name": "demo", "side": "租户端", "phase": "修改表", "order": "3", "sql": oldSQL, "source_digest": identity.Digest(oldSQL), "rollback": "人工回滚说明"}}}
	if err = store.Save(version); err != nil {
		t.Fatal(err)
	}
	store.History = append(store.History, workspace.Execution{ID: "old-audit", Version: "5.7.3", ItemID: "sql-refresh", Digest: identity.Digest(oldSQL), State: "成功"})
	app := App{Store: store}
	updates := []gitservice.SQLUpdate{{ID: "sql-refresh", Before: identity.Digest(oldSQL), Content: "UPDATE demo SET enabled=2;", Commit: "new-commit", Author: "huohuo"}}
	if _, err = app.services().Git.ApplySQLUpdates("5.7.3", updates, gitservice.SQLRefresh{Conflicts: []string{}}); err != nil {
		t.Fatal(err)
	}
	item := store.Versions["5.7.3"].Items[0]
	if item.Data["phase"] != "初始化数据" || item.Data["order"] != "3" || item.Data["rollback"] != "人工回滚说明" || identity.Digest(item.Data["sql"]) == store.History[0].Digest {
		t.Fatal("刷新破坏用户元信息或沿用旧执行结果")
	}
	// 刷新期间内容又被编辑时，旧摘要不匹配的候选必须跳过。
	updates[0].Content = "UPDATE demo SET enabled=3;"
	result, err := app.services().Git.ApplySQLUpdates("5.7.3", updates, gitservice.SQLRefresh{Conflicts: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if result.(gitservice.SQLRefresh).Updated != 0 || len(result.(gitservice.SQLRefresh).Conflicts) != 1 {
		t.Fatal("未保护刷新期间的人工修改")
	}
}

func TestSingleInstancePerDataDirectory(t *testing.T) {
	// 同一数据目录只能持有一个执行入口，另一个目录的测试实例不受影响。
	dir := t.TempDir()
	first, err := lockInstance(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(first)
	second, err := lockInstance(dir)
	if second != 0 {
		defer windows.CloseHandle(second)
	}
	if err == nil {
		t.Fatal("同一数据目录未阻止重复实例")
	}
}

func TestSQLPhaseInference(t *testing.T) {
	for sql, expected := range map[string]string{"CREATE TABLE t(id INT);": "创建表", "ALTER TABLE t ADD a INT;": "修改表", "INSERT INTO t VALUES(1);": "初始化数据"} {
		if got := sqlscript.InferSQLPhase(map[string]string{"sql": sql}); got != expected {
			t.Fatalf("%s: %s", sql, got)
		}
	}
	// 目录名和手工标签不能覆盖 SQL 真正的类型。
	if got := sqlscript.InferSQLPhase(map[string]string{"sql": "UPDATE t SET a=1;", "source_path": "新增表/伪装.sql", "phase": "创建表"}); got != "初始化数据" {
		t.Fatal("类型错误依赖目录名称")
	}
	if got := sqlscript.InferSQLPhase(map[string]string{"sql": "-- CREATE TABLE fake\nSELECT 'CREATE TABLE fake';"}); got != "初始化数据" {
		t.Fatal("注释或字符串误导了类型识别")
	}
	if got := sqlscript.InferSQLPhase(map[string]string{"sql": "ALTER TABLE t ADD a INT; CREATE TABLE b(id INT);"}); got != "创建表" {
		t.Fatal("混合脚本未取创建表优先级")
	}
}
