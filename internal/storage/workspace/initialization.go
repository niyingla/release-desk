package workspace

import (
	"errors"
	"strings"
)

// DefaultInitializationBaseURLs 集中提供用户确认的初始化接口环境地址，可由 settings.json 覆盖。
// 无业务入参；每次返回独立映射，业务流程不散落具体域名。
// 返回：测试和线上环境的三端入口；服务商路径前缀属于地址的一部分。
func DefaultInitializationBaseURLs() map[string]map[string]string {
	// 这里只提供用户给出的初始路由，不保存 Token 或自动连接线上。
	// 服务商的 spapi 是网关路径前缀，不能在域名归一时丢弃。
	return map[string]map[string]string{
		"test":       {"租户端": "https://qyapitest.qiyucloud.com.cn", "服务商端": "https://qyapitest.qiyucloud.com.cn/spapi", "平台端": "https://qypfapitest.qiyucloud.com.cn"},
		"production": {"租户端": "https://qyapi.qiyucloud.com.cn", "服务商端": "https://qyapi.qiyucloud.com.cn/spapi", "平台端": "https://qypfapi.qiyucloud.com.cn"},
	}
}

// NormalizeInitialization 补全初始化调用的参数分类，不将参数模板当作实际执行值。
// 参数 data：事项字段映射，可包含尚待填写的路径和 JSON 参数模板。
// 返回：分类合法返回 nil；未知分类返回错误，不猜测为无参。
func NormalizeInitialization(data map[string]string) error {
	// 空分类兼容外部填写文件，根据是否已有参数模板补齐展示。
	if data["parameter_mode"] == "" {
		// 现有模板带参数时分类为必填，避免被空分类误解释为无参。
		data["parameter_mode"] = "无参"
		if strings.TrimSpace(data["params"]+data["body"]) != "" {
			data["parameter_mode"] = "必填参数"
		}
	}
	// 分类只控制手动调用时的参数检查，不在保存时触发任何网络操作。
	if data["parameter_mode"] != "无参" && data["parameter_mode"] != "必填参数" {
		return errors.New("初始化接口参数要求应为无参或必填参数")
	}
	return nil
}
