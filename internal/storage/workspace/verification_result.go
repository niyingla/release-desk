package workspace

import "strings"

// VerifyResult 保存 AI 的本轮结论与证据；Analysis 可省略以兼容旧版历史。
type VerifyResult struct {
	Status            string                `json:"status"`
	Confirmation      string                `json:"confirmation,omitempty"`
	Blocker           string                `json:"blocker,omitempty"`
	LogQuerySucceeded bool                  `json:"log_query_succeeded,omitempty"`
	Summary           string                `json:"summary"`
	Environment       string                `json:"environment"`
	Window            string                `json:"window"`
	Evidence          []string              `json:"evidence"`
	NextSteps         string                `json:"next_steps"`
	At                string                `json:"at"`
	Analysis          *VerificationAnalysis `json:"analysis,omitempty"`
}

// VerificationAnalysis 分开记录目标、代码判断、日志发现和综合理由，不限定 AI 的核验方法。
type VerificationAnalysis struct {
	Goal      string `json:"goal"`
	Code      string `json:"code"`
	Logs      string `json:"logs"`
	Reasoning string `json:"reasoning"`
}

// Text 将分项分析转换为可复制、可回看的文本。
// 无业务入参；字段来自已脱敏的本轮模型输出，不读取或查询额外证据。
// 返回：只包含有内容的分析段落；旧记录缺少分析时返回空文本。
func (a *VerificationAnalysis) Text() string {
	// 兼容升级前的历史，不为缺少字段的旧结论编造推理过程。
	if a == nil {
		return ""
	}
	parts := []string{}
	for _, field := range [][2]string{{"验证目标", a.Goal}, {"代码判断", a.Code}, {"日志发现", a.Logs}, {"综合理由", a.Reasoning}} {
		// 这些标签用于回看依据，不能用标签内容或固定词判断是否通过。
		if strings.TrimSpace(field[1]) != "" {
			parts = append(parts, field[0]+"："+field[1])
		}
	}
	// 换行原样保留，前端展示时统一转义，不解释证据中的 HTML。
	return strings.Join(parts, "\n")
}
