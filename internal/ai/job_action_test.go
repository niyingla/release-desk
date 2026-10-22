package aiservice

import (
	"testing"

	"release-desk/internal/storage/workspace"
)

// TestVersionScanAcceptsJobClosure 验证 AI 扫描输出的关闭动作能进入发布清单。
// 参数 t：Go 测试上下文；只处理内存扫描结果，不启动 Codex 或调度系统。
func TestVersionScanAcceptsJobClosure(t *testing.T) {
	// 扫描记录包含来源；关闭沿用 action 字段，不创建平行状态字段。
	result := ScanResult{Summary: "目标版本关闭旧同步任务", Items: []ScanItem{{ID: "job-legacy", Kind: "job", Action: "关闭", Fields: []ScanField{{Name: "handler", Value: "legacySync"}}, Evidence: []string{"qa-sha JobHandler#legacySync"}}}}
	version, err := PrepareScanVersion(workspace.Version{Name: "5.7.3"}, result)
	if err != nil {
		t.Fatal(err)
	}
	if len(version.Items) != 1 || version.Items[0].Action != "关闭" || version.Items[0].Online != "待上线" {
		t.Fatalf("AI 关闭操作未正确保留：%+v", version.Items)
	}
}
