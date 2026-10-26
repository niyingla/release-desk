package aiservice

import (
	"testing"

	"release-desk/internal/storage/workspace"
)

// TestScanMarksUnobservableChangeForManualVerification 验证扫描结果可声明日志无法证明的关键结果。
// 参数 t：测试上下文；仅转换内存候选，不查询日志或调用模型。
func TestScanMarksUnobservableChangeForManualVerification(t *testing.T) {
	result := ScanResult{Summary: "关键结果没有日志观测点", Items: []ScanItem{{ID: "manual-ui", Kind: "verify", Action: "修改", Fields: []ScanField{{Name: "title", Value: "页面展示结果"}, {Name: "verification_mode", Value: "手动验证"}, {Name: "manual_reason", Value: "日志只记录入口，未记录展示结果"}, {Name: "steps", Value: "人工核对页面及实际返回结果"}}, Evidence: []string{"qa-before -> qa-after ViewService#detail"}}}}
	// 模型只声明可观察性，待验收状态由软件规范化生成。
	version, err := PrepareScanVersion(workspace.Version{Name: "qa-scan"}, result)
	if err != nil {
		t.Fatal(err)
	}
	if version.Items[0].VerifyState != "需手动验证" {
		t.Fatalf("无日志可观察性的事项仍被当作自动验证：%s", version.Items[0].VerifyState)
	}
}
