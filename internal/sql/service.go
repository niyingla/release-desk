package sqlservice

import (
	"sync"

	"release-desk/internal/storage/workspace"
)

// Service 管理测试及 UAT 独立连接、预览及执行；令牌仅保存在本窗口实例内。
type Service struct {
	*workspace.Store
	PlanMu     sync.Mutex
	Plans      map[string]Plan
	QueuePlans map[string]SQLQueuePreview
}
