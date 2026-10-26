package app

import (
	"context"
	"testing"

	aiservice "release-desk/internal/ai"
	"release-desk/internal/storage/workspace"
)

// TestManualVerificationCannotEnterQueue 验证单项及混合批次均不能绕过人工分类。
// 参数 t：测试上下文；使用临时版本及哨兵执行器，不访问生产或调用模型。
func TestManualVerificationCannotEnterQueue(t *testing.T) {
	store, req := queueVerifyFixture(t)
	v := workspace.Clone(store.Versions[req.Version])
	v.Items[0].Data["verification_mode"] = "手动验证"
	v.Items[0].Data["manual_reason"] = "没有可证明页面最终结果的日志"
	// 旧待验证状态在保存时规范化为人工待验，但不伪造人工完成结论。
	if err := store.Save(v); err != nil {
		t.Fatal(err)
	}
	service := &aiservice.Service{Store: store}
	run := func(context.Context, aiservice.VerifyRequest, workspace.Item, workspace.Settings) (workspace.VerifyResult, error) {
		t.Error("人工项触发了取证执行器")
		return workspace.VerifyResult{}, nil
	}
	if err := service.StartVerifyQueue(req, run); err == nil {
		t.Fatal("混合批次没有整体拒绝")
	}
	if _, err := service.EnqueueVerification(aiservice.VerifyRequest{Version: req.Version, ID: req.IDs[0], Start: req.Start, End: req.End}, run); err == nil {
		t.Fatal("单项绕过人工分类")
	}
	// 拒绝不能占用并发或留下半个队列，其他自动项稍后仍可执行。
	if store.AIBusy || store.VerifyQueue != nil {
		t.Fatal("无效请求污染任务状态")
	}
}
