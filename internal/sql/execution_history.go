package sqlservice

import "release-desk/internal/storage/workspace"

// latestExecutions 选取同环境、服务器与脚本内容的每库最近结果。
// 参数 plan：本次已确认环境的预览快照，环境缺失仅兼容旧测试计划。
// 参数 history：按写入先后排列的正式执行历史，不修改其内容。
// 返回：库名到最近结果的独立映射；其他环境的成功不能用于跳过本次执行。
func latestExecutions(plan Plan, history []workspace.Execution) map[string]workspace.Execution {
	// 空历史环境来自仅支持测试的旧版本，升级后仍只属于 test。
	// 即使主机与库名完全相同，也先检查环境，防止跨环境复用成功或失败。
	latest := map[string]workspace.Execution{}
	for _, record := range history {
		// 显式结构核验只证明当前结构；不能据此跳过或触发真正的数据写入。
		if record.VerificationOnly {
			continue
		}
		if workspace.EffectiveSQLEnvironment(record.Environment) != workspace.EffectiveSQLEnvironment(plan.Environment) {
			continue
		}
		if record.Version == plan.Version && record.ItemID == plan.ItemID && record.Digest == plan.Digest && record.Server == plan.Server {
			latest[record.Database] = record
		}
	}
	// 后写记录覆盖同库旧记录，重试使用真实最近结果而非任一次成功。
	return latest
}
