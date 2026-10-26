package workspace

import (
	"release-desk/internal/tasks"
	"testing"
)

// TestNeutralOutcomeDoesNotHideIncompleteResults 验证中立观察不被汇总成全通过。
// 参数 t：测试上下文；只组合内存快照，不修改用户历史。
func TestNeutralOutcomeDoesNotHideIncompleteResults(t *testing.T) {
	for _, state := range []string{"通过", "证据不足", "失败", "已停止"} {
		want := map[string]string{"通过": "未发现异常", "证据不足": "证据不足", "失败": "有失败", "已停止": "已停止"}[state]
		for _, order := range [][]tasks.Row{{{State: "未发现异常"}, {State: state}}, {{State: state}, {State: "未发现异常"}}} {
			// 完成顺序不改变结果，失败和未处理项优先提示。
			if got := taskOutcome(order, false); got != want {
				t.Fatalf("%s 汇总为 %s，期望 %s", state, got, want)
			}
		}
	}
}
