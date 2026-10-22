package fileservice

import (
	"release-desk/internal/storage/workspace"
)

// Service 管理用户主动触发的目录打开、复制、备份恢复及清单导出。
type Service struct{ *workspace.Store }
