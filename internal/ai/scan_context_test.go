package aiservice

import (
	"encoding/json"
	"strings"
	"testing"

	"release-desk/internal/storage/workspace"
)

// TestRepeatedScanDoesNotPromotePreviousResults 验证重复扫描仅保留业务线索，不把旧验收结论传给模型。
// 参数 t：Go 测试上下文；使用同一未发布版本的两次扫描快照，不调用模型或远端。
func TestRepeatedScanDoesNotPromotePreviousResults(t *testing.T) {
	v := workspace.VersionFile{Format: "release-desk-version", Schema: 1, Version: workspace.Version{Name: "5.7.0", Status: "进行中"}, Items: []workspace.Item{{
		ID: "new-goal", Kind: "verify", VerifyState: "通过", VerifyRuns: []workspace.VerifyResult{{Status: "通过", Summary: "上次扫描的开发快照结论"}},
		Data: map[string]string{"title": "新需求", "change": "本版本新增业务", "source_commit": "first-development-sha", "result": "旧验证结论"},
	}}}
	prompt, err := VersionScanPrompt(v, workspace.DefaultSettings())
	if err != nil {
		t.Fatal(err)
	}
	start := strings.LastIndex(prompt, "\n{")
	if start < 0 {
		t.Fatal("缺少扫描上下文")
	}
	var context struct {
		Version           workspace.VersionFile `json:"version"`
		ExistingItemsRole string                `json:"existing_items_role"`
		ComparisonBasis   string                `json:"comparison_basis"`
	}
	if err = json.Unmarshal([]byte(prompt[start:]), &context); err != nil {
		t.Fatal(err)
	}
	got := context.Version.Items[0]
	if got.ID != "new-goal" || got.Data["source_commit"] != "first-development-sha" || got.Data["change"] != "本版本新增业务" {
		t.Fatal("重复扫描丢失了稳定 ID 或开发来源线索")
	}
	if len(got.VerifyRuns) != 0 || got.VerifyState != "" || got.Data["result"] != "" {
		t.Fatal("上次扫描的验收结论进入了本轮代码分析")
	}
	if context.ExistingItemsRole != "scan_history" || context.ComparisonBasis != "published_release" {
		t.Fatal("历史扫描与发布基线没有区分用途")
	}
	if v.Items[0].VerifyState != "通过" || len(v.Items[0].VerifyRuns) != 1 || v.Items[0].Data["result"] != "旧验证结论" {
		t.Fatal("构建提示词修改了用户保存的历史记录")
	}
}
