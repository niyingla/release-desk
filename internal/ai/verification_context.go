package aiservice

import (
	"strings"

	"release-desk/internal/storage/workspace"
)

// VerificationCondition 记录本轮自主验证的目标与参考线索，便于回看历史。
// 参数 req：已校验的版本及北京时间窗口，不表示任何固定验收方法。
// 参数 item：启动时固定的事项快照；服务、步骤、检索词和预期结果均可省略。
// 返回：完整可读的条件快照；不会将缺少线索解释为跳过或失败。
func VerificationCondition(req VerifyRequest, item workspace.Item) string {
	// 历史首先记录要解决什么问题，而不是只记录预先猜测的日志词。
	lines := []string{req.Start + " ~ " + req.End, "版本：" + req.Version, "目标：" + item.Data["title"], "判断方式：先集合版本代码与生产日志，再由 AI 综合判断；预填条件可调整"}
	for _, field := range [][2]string{{"修改", item.Data["change"]}, {"服务线索", item.Service}, {"参考步骤", item.Data["steps"]}, {"参考检索词", item.Data["keywords"]}, {"参考预期", item.Data["expected"]}, {"代码来源", item.Data["source_evidence"]}} {
		// 保留用户提供的信息，但不把空值变成必须补齐的前置条件。
		if strings.TrimSpace(field[1]) != "" {
			lines = append(lines, field[0]+"："+field[1])
		}
	}
	// 任务历史使用独立文本快照，后续修改事项不会改写本轮验证依据。
	return strings.Join(lines, "\n")
}
