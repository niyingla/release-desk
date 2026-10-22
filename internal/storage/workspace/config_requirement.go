package workspace

import (
	"errors"
	"slices"
)

// ConfigRequirements 是界面、文件协议与 AI 共同使用的配置要求选项。
var ConfigRequirements = []string{"待确认", "必须配置", "可选配置"}

// ValidateConfigRequirement 校验配置要求，旧记录按待确认处理。
// 参数 data：配置事项的字符串字段；缺省或空 required 不表示可选。
// 返回：合法时补全 required 并返回 nil；未知取值返回错误，阻止覆盖版本文件。
func ValidateConfigRequirement(data map[string]string) error {
	// 不能根据配置内容非空或“代码可能有默认值”猜测部署要求。
	if data["required"] == "" {
		data["required"] = ConfigRequirements[0]
	}
	// 使用有限选项，避免手工文件与 AI 输出出现多种相反解释。
	if !slices.Contains(ConfigRequirements, data["required"]) {
		return errors.New("配置要求应为待确认、必须配置或可选配置")
	}
	// 是否可选只描述部署要求，不改变用户维护的线上状态。
	return nil
}
