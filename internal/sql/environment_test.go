package sqlservice

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"release-desk/internal/identity"
	"release-desk/internal/storage/workspace"
)

// TestEnvironmentConnectionsAreIndependent 验证独立加密文件、密码保留和旧测试入口。
// 参数 t：Go 测试上下文；使用虚构连接参数，不建立任何网络连接。
func TestEnvironmentConnectionsAreIndependent(t *testing.T) {
	store, err := workspace.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{Store: store}
	testConfig := DBConfig{Environment: "test", Host: "fake-test.invalid", Port: 3306, User: "qa-test", Password: "fake-test-secret"}
	if err = service.SaveDB(testConfig); err != nil {
		t.Fatal(err)
	}
	// 缺失 UAT 必须报错，不能回退到已配置测试。
	if _, err = service.LoadDB("uat"); err == nil {
		t.Fatal("UAT 借用了测试连接")
	}
	uatConfig := DBConfig{Environment: "uat", Host: "fake-uat.invalid", Port: 3306, User: "qa-uat", Password: "fake-uat-secret"}
	if err = service.SaveDB(uatConfig); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []DBConfig{testConfig, uatConfig} {
		actual, err := service.LoadDB(expected.Environment)
		if err != nil || actual.Host != expected.Host || actual.Password != expected.Password {
			t.Fatalf("%s 连接读取不正确", expected.Environment)
		}
		file, _ := service.ConnectionPath(expected.Environment)
		encoded, err := os.ReadFile(file)
		if err != nil || strings.Contains(string(encoded), expected.Password) {
			t.Fatal("凭据未加密")
		}
		// 摘要不包含密码，界面留空密码时只保留当前环境凭据。
		request, _ := json.Marshal(map[string]string{"environment": expected.Environment})
		view, err := service.DBSettings(request)
		data, _ := json.Marshal(view)
		if err != nil || strings.Contains(string(data), "password") {
			t.Fatal("连接摘要泄漏密码字段")
		}
		expected.Password = ""
		request, _ = json.Marshal(expected)
		if _, err = service.DBSave(request); err != nil {
			t.Fatal(err)
		}
	}
	legacy, err := service.LoadDB("")
	if err != nil || legacy.Environment != "test" {
		t.Fatal("旧测试入口不兼容")
	}
	gotUAT, _ := service.LoadDB("uat")
	if gotUAT.Password != uatConfig.Password {
		t.Fatal("UAT 留空密码错误复用了测试凭据")
	}
	if _, err = service.ConnectionPath("../../other"); err == nil {
		t.Fatal("未知环境被用于文件路径")
	}
	if err = service.SaveDB(DBConfig{Environment: "prod"}); err == nil {
		t.Fatal("接受了非授权执行环境")
	}
}

// TestEnvironmentHistoryNeverCrossesTargets 验证历史隔离以及旧无环境字段仅属于测试。
// 参数 t：Go 测试上下文；所有结果为内存模拟数据。
func TestEnvironmentHistoryNeverCrossesTargets(t *testing.T) {
	plan := Plan{Environment: "uat", Version: "qa", ItemID: "sql", Digest: "digest", Server: "same.invalid:3306"}
	history := []workspace.Execution{
		{Version: plan.Version, ItemID: plan.ItemID, Digest: plan.Digest, Server: plan.Server, Database: "sp", State: "成功"},
		{Environment: "test", Version: plan.Version, ItemID: plan.ItemID, Digest: plan.Digest, Server: plan.Server, Database: "sp1", State: "成功"},
		{Environment: "uat", Version: plan.Version, ItemID: plan.ItemID, Digest: plan.Digest, Server: plan.Server, Database: "sp1", State: "失败"},
		// 只读通过不能覆盖真正执行记录，尤其不能据此跳过初始化数据。
		{Environment: "uat", VerificationOnly: true, Version: plan.Version, ItemID: plan.ItemID, Digest: plan.Digest, Server: plan.Server, Database: "sp1", State: "成功"},
	}
	got := latestExecutions(plan, history)
	if len(got) != 1 || got["sp1"].State != "失败" {
		t.Fatalf("UAT 读取了其他环境历史：%+v", got)
	}
	plan.Environment = "test"
	got = latestExecutions(plan, history)
	if len(got) != 2 || got["sp"].State != "成功" || got["sp1"].State != "成功" {
		t.Fatalf("旧测试历史被丢弃：%+v", got)
	}
}

// TestEnvironmentPreviewRejectsChangedConnection 验证环境、账号和凭据都绑定于后端预览。
// 参数 t：Go 测试上下文；只做预览校验，不执行 SQL。
func TestEnvironmentPreviewRejectsChangedConnection(t *testing.T) {
	store, err := workspace.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	item := workspace.Item{ID: "sql", Kind: "sql", Data: map[string]string{"name": "qa", "side": "服务商端", "sql": "INSERT INTO demo(id) VALUES(1);"}}
	v := workspace.VersionFile{Format: "release-desk-version", Schema: 1, Version: workspace.Version{Name: "qa"}, Items: []workspace.Item{item}}
	if err = store.Save(v); err != nil {
		t.Fatal(err)
	}
	service := &Service{Store: store}
	cfg := DBConfig{Environment: "uat", Host: "fake.invalid", Port: 3306, User: "qa", Password: "fake-password", ScopeRules: workspace.DefaultSettings().ScopeRules}
	plan := Plan{Environment: "uat", Version: "qa", ItemID: "sql", Digest: identity.Digest(item.Data["sql"]), SQL: item.Data["sql"], Side: "服务商端", Databases: []string{"sp"}, Server: ServerID(cfg), connectionDigest: connectionDigest(cfg)}
	if _, err = service.ValidateSQLPlan(plan, cfg); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*DBConfig){func(c *DBConfig) { c.Environment = "test" }, func(c *DBConfig) { c.User = "other" }, func(c *DBConfig) { c.Password = "changed" }} {
		changed := cfg
		change(&changed)
		if _, err = service.ValidateSQLPlan(plan, changed); err == nil {
			t.Fatal("变更连接后仍接受旧预览")
		}
	}
	encoded, _ := json.Marshal(plan)
	if strings.Contains(string(encoded), "fake-password") || strings.Contains(string(encoded), plan.connectionDigest) {
		t.Fatal("预览泄漏私有连接信息")
	}
	// 合法 UAT 可以创建惰性连接池，真正访问只在执行器的受控入口发生。
	db, err := ConnectDB(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err = ConnectDB(DBConfig{Environment: "prod"}, ""); err == nil {
		t.Fatal("允许了生产连接")
	}
}
