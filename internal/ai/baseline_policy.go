package aiservice

import (
	"strings"

	"release-desk/web"
)

// applyReleaseBaselinePolicy 将发布基线规则注入扫描、验证及填写说明，避免三套规则漂移。
// 参数 source：带基线策略占位符的内嵌提示词或协议说明，不是业务字段文本。
// 返回：已展开完整规则的文本；资源读取失败时返回错误，调用方不能忽略规则继续生成。
func applyReleaseBaselinePolicy(source string) (string, error) {
	// 基线规则独立维护，新增业务和线上修复采用相同的发布事实边界。
	policy, err := web.Assets.ReadFile("prompts/release-baseline-policy.txt")
	if err != nil {
		return "", err
	}
	// 只替换内嵌模板，不把用户备注或历史提交文本解释为策略。
	return strings.ReplaceAll(source, "__RELEASE_BASELINE_POLICY__", string(policy)), nil
}
