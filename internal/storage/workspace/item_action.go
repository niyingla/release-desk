package workspace

// ItemActionOptions 返回事项类型可维护的操作选项，供校验与界面共用。
// 参数 kind：已经识别的事项类型；job 支持保留定义并停止调度的关闭操作。
// 返回：独立的选项切片；历史删除操作保留，其他类型不接受关闭语义。
func ItemActionOptions(kind string) []string {
	// 关闭仅停用调度，删除则移除定义；两个操作不能互相替代。
	if kind == "job" {
		return []string{"新增", "修改", "关闭", "删除"}
	}
	// 其他业务类型继续沿用原协议，避免无意扩展配置、SQL 等操作范围。
	return []string{"新增", "修改", "删除"}
}
