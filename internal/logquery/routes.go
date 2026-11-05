package logquery

import (
	"encoding/json"
	"errors"

	"release-desk/web"
)

// ResourceOption 是可读的项目或主题标识，不含日志正文和连接凭据。
type ResourceOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// EnvironmentRoutes 保存用户确认可供同事沿用的团队默认环境映射。
type EnvironmentRoutes struct {
	ID           string           `json:"id"`
	Name         string           `json:"name"`
	DefaultTopic string           `json:"default_topic"`
	Topics       []ResourceOption `json:"topics"`
}

// TeamRoutes 将环境差异集中在独立资源中，实际查询仍核对当前连接下的资源归属。
type TeamRoutes struct {
	Endpoint     string                       `json:"endpoint"`
	Region       string                       `json:"region"`
	VerifiedAt   string                       `json:"verified_at"`
	Environments map[string]EnvironmentRoutes `json:"environments"`
}

// ReadTeamRoutes 读取随软件分发、可由设置覆盖的团队路由元数据。
// 无业务入参；资源仅包含已核对的项目和 Topic，不包含 AK、SK 或日志内容。
// 返回：团队默认映射；资源缺失或损坏时明确报错，不从其他环境猜测。
func ReadTeamRoutes() (TeamRoutes, error) {
	// 这里分发的是团队资源定位信息，当前用户仍须自行配置只读授权。
	var routes TeamRoutes
	// 路由不散落在业务方法中，用户换项目时只修改设置中的对应映射。
	raw, err := web.Assets.ReadFile("prompts/log-routes-default.json")
	if err != nil || json.Unmarshal(raw, &routes) != nil {
		return routes, errors.New("团队日志路由资源不可用")
	}
	// 资源只作为定位线索，不能替代云端鉴权或证明项目已可访问。
	return routes, nil
}
