package aiservice

import (
	"release-desk/internal/storage/workspace"
)

// Service 编排版本扫描和 AI 自主验证，复用共享工作区的互斥与持久化边界。
type Service struct{ *workspace.Store }
