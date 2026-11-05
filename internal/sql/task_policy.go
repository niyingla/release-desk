package sqlservice

import "errors"

// CheckSQLAvailability 统一检查预览、单条执行与批量执行的任务冲突。
// 无业务入参；调用方必须持有 Store.Mu，检查与运行标记写入保持原子性。
// 返回：扫描可并行时返回 nil；退出、已有 SQL 或 AI 验证占用时返回明确错误。
func (s *Service) CheckSQLAvailability() error {
	// 退出后不再发现目标或启动写入，避免产生无人接收的执行结果。
	if s.Closing {
		return errors.New("软件正在退出，未启动 SQL")
	}
	// 单条和批量共用 SQL 通道，脚本切换的间隙也不能插入另一批。
	if s.SQLActive() {
		return errors.New("已有 SQL 执行任务，请等待完成")
	}
	// 扫描只读取代码并合并候选，执行器持有固定计划，二者互不占用。
	// AI 验证仍遵循原有互斥规则，不将所有 AI 任务一并放开。
	if s.AIBusy && (s.Scan == nil || !s.Scan.Running) {
		return errors.New("请等待当前 AI 验证任务完成后再执行 SQL")
	}
	return nil
}
