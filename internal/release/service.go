package releaseservice

import (
	"release-desk/internal/storage/workspace"
)

// Service 维护版本事项和人工发布状态，不承担数据库执行或 AI 查询。
type Service struct{ *workspace.Store }
