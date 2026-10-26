package aiservice

import (
	"strconv"
	"strings"

	"release-desk/internal/storage/workspace"
)

// VerificationPermissionArguments 为本次验收构造只读文件与日志网络权限。
// 参数 config：已经固定的非敏感配置，仅提取允许访问的精确日志主机。
// 返回：独立 CLI 参数；配置错误时拒绝启动，不降级为完全访问或改变全局设置。
func VerificationPermissionArguments(config workspace.Settings) ([]string, error) {
	domains := workspace.LogDomains(config)
	// 先验证再引用，配置文本不能变成额外 CLI 参数或 TOML 片段。
	if err := workspace.ValidateLogDomains(domains); err != nil {
		return nil, err
	}
	rules := make([]string, 0, len(domains))
	for _, domain := range domains {
		// 整张域名表用 TOML 内联表传递，避免带点域名被当成嵌套配置路径。
		rules = append(rules, strconv.Quote(strings.ToLower(domain))+`="allow"`)
	}
	// 不传旧 --sandbox 开关，它会覆盖命名权限中的受限网络设置。
	return []string{"-c", `default_permissions="release_desk_logs_readonly"`, "-c", `permissions.release_desk_logs_readonly.extends=":read-only"`, "-c", `features.network_proxy=true`, "-c", `permissions.release_desk_logs_readonly.network.enabled=true`, "-c", `permissions.release_desk_logs_readonly.network.domains={` + strings.Join(rules, ",") + `}`}, nil
}
