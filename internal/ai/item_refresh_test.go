package aiservice

import (
	"context"
	"testing"
	"time"

	"release-desk/internal/storage/workspace"
)

// TestItemCodeRefreshOnlyMergesSelectedItem 验证单项更新范围由后端约束，不允许模型新增或修改其他事项。
// 参数 t：Go 测试上下文；直接注入结构化候选，不调用 Codex 或远端 Git。
func TestItemCodeRefreshOnlyMergesSelectedItem(t *testing.T) {
	for _, wrongScope := range []bool{false, true} {
		t.Run(map[bool]string{false: "合法更新", true: "越界更新"}[wrongScope], func(t *testing.T) {
			s, err := workspace.OpenStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.StopWindowTasks)
			v := workspace.VersionFile{Format: "release-desk-version", Schema: 1, Version: workspace.Version{Name: "5.7.3", Notes: "人工备注"}, Items: []workspace.Item{
				{ID: "verify-one", Kind: "verify", Data: map[string]string{"title": "当前目标", "change": "旧实现"}, VerifyState: "通过", VerifyRuns: []workspace.VerifyResult{{Status: "通过"}}},
				{ID: "verify-two", Kind: "verify", Data: map[string]string{"title": "其他目标", "change": "保持原样"}},
			}}
			if err = s.Save(v); err != nil {
				t.Fatal(err)
			}
			err = (&Service{Store: s}).StartItemCodeRefresh(v.Version.Name, "verify-one", func(_ context.Context, snapshot workspace.VersionFile, _ workspace.Settings) (ScanResult, error) {
				if len(snapshot.Items) != 1 || snapshot.Items[0].ID != "verify-one" {
					t.Error("单项更新传入了其他事项")
				}
				item := ScanItem{ID: "verify-one", Kind: "verify", Fields: []ScanField{{Name: "title", Value: "当前目标"}, {Name: "change", Value: "最新实现"}, {Name: "verification_mode", Value: "手动验证"}, {Name: "manual_reason", Value: "结果没有运行日志"}}, Evidence: []string{"new-sha src/Goal.go#Run"}}
				if wrongScope {
					item.ID = "verify-two"
				}
				return ScanResult{Summary: "重新核对当前事项", Items: []ScanItem{item}}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				s.Mu.Lock()
				finished, scanErr := !s.Scan.Running, s.Scan.Error
				current := workspace.Clone(s.Versions[v.Version.Name])
				s.Mu.Unlock()
				if finished {
					if wrongScope != (scanErr != "") {
						t.Fatalf("范围校验错误：%s", scanErr)
					}
					byID := map[string]workspace.Item{}
					for _, item := range current.Items {
						byID[item.ID] = item
					}
					if len(current.Items) != 2 || byID["verify-two"].Data["change"] != "保持原样" || current.Version.Notes != "人工备注" {
						t.Fatal("单项更新越界覆盖内容")
					}
					one := byID["verify-one"]
					if len(one.VerifyRuns) != 1 {
						t.Fatal("旧验证历史丢失")
					}
					if !wrongScope && (one.Data["change"] != "最新实现" || one.VerifyState != "需手动验证") {
						t.Fatalf("新条件没有生效：%+v", one)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("单项更新未结束")
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}
