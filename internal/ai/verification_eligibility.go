package aiservice

import (
	"fmt"

	"release-desk/internal/storage/workspace"
)

// RequireAutomaticVerification 在创建或追加队列前拒绝人工事项。
// 参数 items：本次用户明确选择的独立记录快照，尚未调用模型或写入队列。
// 返回：存在人工项时拒绝整组并指出事项，避免部分收费执行后才发现不可验证。
func RequireAutomaticVerification(items []workspace.Item) error {
	for _, item := range items {
		// 后端检查与前端禁用配套，旧页面或直接接口请求也不能绕过。
		if workspace.NeedsManualVerification(item) {
			return fmt.Errorf("%s 需手动验证，不能加入自动验证队列", item.Data["title"])
		}
	}
	// 旧记录没有模式字段时仍按原逻辑允许，不能按失败次数推断人工分类。
	return nil
}
