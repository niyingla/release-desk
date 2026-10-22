package workspace

import "testing"

// TestVerificationHashTracksGoalAndSource 防止标题或代码来源变化后沿用旧验收结论。
// 参数 t：Go 测试上下文；只比较业务快照摘要，不读写文件。
func TestVerificationHashTracksGoalAndSource(t *testing.T) {
	item := Item{Kind: "verify", Action: "修改", Data: map[string]string{"title": "防重复排班", "source_evidence": "sha-a TaskService#create"}}
	before := VerificationHash(item)
	// 现在 AI 会读取标题与来源，自主分析所用输入变化也必须使旧结论失效。
	for _, key := range []string{"title", "source_evidence"} {
		changed := Clone(item)
		changed.Data[key] = "另一个修复目标或来源"
		if VerificationHash(changed) == before {
			t.Errorf("修改 %s 后旧结论未失效", key)
		}
	}
	// 结果回填不属于新的验证输入，不能使本轮自己的结论失效。
	item.Data["result"] = "本轮结论"
	if VerificationHash(item) != before {
		t.Fatal("结果正文影响了输入摘要")
	}
}
