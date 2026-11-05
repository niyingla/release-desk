package sqlservice

import (
	"strings"
	"testing"

	"release-desk/internal/storage/workspace"
)

// TestScanAllowsSQLPreflightButKeepsPlanValidation 验证扫描放行预览和单条入口，仍拒绝过期正文。
// 参数 t：Go 测试上下文；使用隔离配置并在联网前故意触发校验错误。
func TestScanAllowsSQLPreflightButKeepsPlanValidation(t *testing.T) {
	s, err := workspace.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	v := workspace.VersionFile{Format: "release-desk-version", Schema: 1, Version: workspace.Version{Name: "qa"}, Items: []workspace.Item{{ID: "sql", Kind: "sql", Data: map[string]string{"name": "qa", "side": "服务商端", "sql": "INSERT INTO demo VALUES(1);"}}}}
	if err = s.Save(v); err != nil {
		t.Fatal(err)
	}
	db := &Service{Store: s}
	s.AIBusy, s.Scan = true, &workspace.VersionScanState{Running: true}
	// 缺少连接应到达配置校验；旧实现会在这里误报 SQL 忙碌。
	if _, err = db.Plan("qa", "sql", false, "test", false); err == nil || !strings.Contains(err.Error(), "配置") {
		t.Fatalf("扫描阻断 SQL 预览：%v", err)
	}
	cfg := DBConfig{Environment: "test", Host: "qa.invalid", Port: 3306, User: "qa"}
	if err = db.SaveDB(cfg); err != nil {
		t.Fatal(err)
	}
	// 放行只解除任务互斥，旧预览仍须被拒绝，不能偷偷执行刷新后的新 SQL。
	if err = db.Execute(Plan{Environment: "test", Version: "qa", ItemID: "sql", Server: ServerID(cfg), SQL: "INSERT INTO demo VALUES(2);", Digest: "expired", Side: "服务商端"}); err == nil || !strings.Contains(err.Error(), "SQL 内容或所属端") {
		t.Fatalf("扫描期间未保留正文校验：%v", err)
	}
	// 验证任务及退出期间的阻塞规则保持不变，不能把所有 AI 占用都放开。
	s.Scan.Running = false
	if _, err = db.Plan("qa", "sql", false, "test", false); err == nil || !strings.Contains(err.Error(), "任务") {
		t.Fatalf("AI 验证期间仍可进入 SQL 预览：%v", err)
	}
}
