package workspace

import (
	"errors"
	"regexp"
	"slices"
	"strings"

	"release-desk/internal/policy"
)

var logDomainName = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9.-]*[a-zA-Z0-9])?$`)

// LogDomains 返回本轮固定的日志网络主机，不读取任何访问密钥。
// 参数 config：本机非敏感设置快照，旧设置没有域名时使用集中初始值。
// 返回：独立主机列表，仅用于只读验证进程的网络限制。
func LogDomains(config Settings) []string {
	// 默认值集中维护，避免业务执行入口固定到某个火山区域。
	if len(config.VerificationLogDomains) == 0 {
		return []string{policy.DefaultVerificationLogDomain}
	}
	// 不返回底层切片，界面设置不能影响正在取证的权限快照。
	return slices.Clone(config.VerificationLogDomains)
}

// ValidateLogDomains 拒绝含协议、端口、账户、路径或通配符的网络范围。
// 参数 domains：用户配置的精确主机列表，空值兼容旧设置。
// 返回：格式或规模错误；合法配置不会隐式扩大到全部互联网。
func ValidateLogDomains(domains []string) error {
	if len(domains) > policy.MaxVerificationLogDomains {
		// 显式设置仍保留有界范围，不能生成无限规模的允许表。
		return errors.New("日志查询域名数量过多")
	}
	for _, domain := range domains {
		// 仅接受精确 ASCII 主机，显式限制长度且不允许空标签。
		if len(domain) > 253 || !logDomainName.MatchString(domain) || strings.Contains(domain, "..") {
			// 不将包含账户或通配符的输入宽松修正成可访问网络。
			return errors.New("日志查询域名应为精确主机名，不含协议、端口、路径或通配符")
		}
	}
	return nil
}
