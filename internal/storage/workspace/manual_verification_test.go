package workspace

import "testing"

// TestManualModeInvalidatesOldConclusionButPreservesHistory 验证重新扫描改变方式时不借用旧结论。
// 参数 t：测试上下文；只合并内存版本，不触碰真实发版文件。
func TestManualModeInvalidatesOldConclusionButPreservesHistory(t *testing.T) {
	previous := VersionFile{Format: "release-desk-version", Schema: 1, Version: Version{Name: "qa-manual"}, Items: []Item{{ID: "verify", Kind: "verify", VerifyState: "通过", VerifyRuns: []VerifyResult{{Status: "通过", Summary: "原有结论"}}, Data: map[string]string{"title": "展示逻辑"}}}}
	incoming := Clone(previous)
	incoming.Items[0].Data["verification_mode"] = "手动验证"
	incoming.Items[0].Data["manual_reason"] = "关键展示结果未记录在日志"
	// 新方式影响本轮验收范围，合并后保留旧历史但必须重新人工核验。
	got, err := MergeExternalVersion(previous, incoming)
	if err != nil || got.Items[0].VerifyState != "需手动验证" || len(got.Items[0].VerifyRuns) != 1 {
		t.Fatalf("人工方式或历史不正确：%+v %v", got.Items[0], err)
	}
}
