package workspace

// VerificationData 复制本轮 AI 判断需要的业务输入，排除历史结论和派生展示值。
// 参数 item：所选变更事项；标题、来源、修复描述与参考条件均属于验证输入。
// 返回：独立的字符串字段副本；调用方修改副本不会改变已有记录。
func VerificationData(item Item) map[string]string {
	// 使用相同输入定义生成提示词与摘要，避免 AI 读取了未受过期保护的来源。
	data := make(map[string]string, len(item.Data))
	for key, value := range item.Data {
		// 旧结论不能当作本轮证据；派生摘要也不是待验证业务本身。
		if key == "result" || key == "content_digest" {
			continue
		}
		data[key] = value
	}
	// 调用方可以附加上下文，但不能修改运行中的原事项。
	return data
}
