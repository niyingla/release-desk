package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExternalJobFrequencyMigrates 验证升级读取旧文件和 AI 写入采用同一迁移逻辑。
// 参数 t：Go 测试上下文；旧文件写入临时目录，原内容不涉及业务数据。
func TestExternalJobFrequencyMigrates(t *testing.T) {
	dir := t.TempDir()
	versionDir := filepath.Join(dir, "版本文件")
	if err := os.MkdirAll(versionDir, 0700); err != nil {
		t.Fatal(err)
	}
	v := VersionFile{Format: "release-desk-version", Schema: 1, Version: Version{Name: "legacy-cron"}, Items: []Item{{ID: "job", Kind: "job", Data: map[string]string{"handler": "reportJob", "frequency": "0 0 10 * * ? (每天10点)", "description": "原描述"}}}}
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(versionDir, "legacy-cron.json")
	if err = os.WriteFile(file, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenStore(dir); err != nil {
		t.Fatal(err)
	}
	// 检查实际规范化文件，不能只检查已分离的内存快照。
	encoded, err = os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(encoded, &v); err != nil {
		t.Fatal(err)
	}
	if v.Items[0].Data["frequency"] != "0 0 10 * * ?" || !strings.Contains(v.Items[0].Data["description"], "每天10点") {
		t.Fatalf("旧文件迁移不完整：%+v", v.Items[0].Data)
	}
}

// TestJobFrequencySeparatesLegacyNotes 验证旧频率中的说明被保留，表达式独立保存。
// 参数 t：Go 测试上下文；只校验内存数据，不访问调度器。
func TestJobFrequencySeparatesLegacyNotes(t *testing.T) {
	for _, test := range []struct{ raw, cron, note string }{
		{"0 0 10 * * ?（源码注明每天10:00；Admin实际配置待核对）", "0 0 10 * * ?", "（源码注明每天10:00；Admin实际配置待核对）"},
		{" 0  0/5 * * * ? (每5分钟) ", "0 0/5 * * * ?", "(每5分钟)"},
		{"0 0 3 ? JAN MON#2 2027", "0 0 3 ? JAN MON#2 2027", ""},
		{"0 0 0 L * ?", "0 0 0 L * ?", ""},
		{"0 0 * * *", "0 0 * * *", ""},
		{"0 0 10 * * ? 每天10点", "0 0 10 * * ?", "每天10点"},
		{"每天10点，实际Cron待确认", "", "每天10点，实际Cron待确认"},
		{"0 0 10 * * ? 2027 2028", "", "0 0 10 * * ? 2027 2028"},
		{"", "", ""},
	} {
		item := Item{Kind: "job", Data: map[string]string{"handler": "reportJob", "frequency": test.raw, "description": "原有业务说明"}}
		if err := NormalizeItem(&item); err != nil {
			t.Fatal(err)
		}
		// 未知文本也不能猜成 Cron，保留到描述以便人工核对。
		if item.Data["frequency"] != test.cron || !strings.Contains(item.Data["description"], "原有业务说明") || !strings.Contains(item.Data["description"], test.note) {
			t.Errorf("原文 %q 分离错误：%+v", test.raw, item.Data)
		}
		previous := item.Data["description"]
		if err := NormalizeItem(&item); err != nil || item.Data["description"] != previous {
			t.Fatalf("重复规范化不能追加重复备注：%v / %+v", err, item.Data)
		}
	}
}

// TestJobFrequencyPersistsAfterRestart 验证保存与重新加载均采用纯表达式协议。
// 参数 t：Go 测试上下文；数据仅写入隔离临时目录。
func TestJobFrequencyPersistsAfterRestart(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	v := VersionFile{Format: "release-desk-version", Schema: 1, Version: Version{Name: "qa-cron"}, Items: []Item{{ID: "job", Kind: "job", Data: map[string]string{"handler": "reportJob", "frequency": "0 0 10 * * ?（每天10点）", "description": "报告"}}}}
	if err = store.Save(v); err != nil {
		t.Fatal(err)
	}
	restarted, err := OpenStore(store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	got := restarted.Versions["qa-cron"].Items[0].Data
	if got["frequency"] != "0 0 10 * * ?" || !strings.Contains(got["description"], "每天10点") {
		t.Fatalf("重启后数据不完整：%+v", got)
	}
}
