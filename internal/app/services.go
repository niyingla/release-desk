package app

import (
	"context"
	"sync"

	aiservice "release-desk/internal/ai"
	fileservice "release-desk/internal/files"
	gitservice "release-desk/internal/git"
	gitrepo "release-desk/internal/git/repository"
	releaseservice "release-desk/internal/release"
	settingsservice "release-desk/internal/settings"
	sqlservice "release-desk/internal/sql"
	"release-desk/internal/storage/workspace"
	"release-desk/internal/taskcenter"
	updateservice "release-desk/internal/update"
)

// App 仅组装本机接口及共享工作区，不直接实现 SQL、Git 或 AI 业务。
type App struct {
	Store       *workspace.Store
	Git         *gitrepo.Git
	Token, Host string
	serviceOnce sync.Once
	components  *services
}

type services struct {
	Release  *releaseservice.Service
	Git      *gitservice.Service
	SQL      *sqlservice.Service
	AI       *aiservice.Service
	Settings *settingsservice.Service
	Files    *fileservice.Service
	Tasks    *taskcenter.Service
	Updates  *updateservice.Service
}

// services 延迟建立本窗口的业务组件，确保所有接口使用同一组预览与任务状态。
// 无业务入参；依赖只来自启动时注入的 Store 和 Git。
// 返回：本窗口共享的服务集合；不在创建组件时联网或执行任务。
func (a *App) services() *services {
	// Once 保证并发首请求只初始化一次，SQL 预览不能分散到不同实例。
	a.serviceOnce.Do(func() {
		// 纯端点登记测试没有工作区；真实窗口必须绑定可取消的退出上下文。
		updateContext := context.Background()
		if a.Store != nil && a.Store.TaskContext != nil {
			updateContext = a.Store.TaskContext
		}
		// 各业务服务接收同一工作区，避免运行标记与人工状态出现两个来源。
		a.components = &services{
			Release: &releaseservice.Service{Store: a.Store},
			Git:     &gitservice.Service{Store: a.Store, Git: a.Git},
			// SQL 服务独占预览集合，令牌不会混入版本文件或 AI 协议。
			SQL:      &sqlservice.Service{Store: a.Store, Plans: map[string]sqlservice.Plan{}},
			AI:       &aiservice.Service{Store: a.Store},
			Settings: &settingsservice.Service{Store: a.Store, Git: a.Git},
			// 文件服务只响应用户操作，不在初始化阶段打开目录或导出数据。
			Files: &fileservice.Service{Store: a.Store},
			Tasks: &taskcenter.Service{Store: a.Store},
			// 更新拥有独立状态与网络超时，不参与 SQL 或 AI 的占用标记。
			Updates: updateservice.New(updateContext),
		}
		// 组件只共享工作区锁和注入依赖，业务实现不反向依赖 app。
	})
	// 统一从此处取得实例，避免重复 HTTP 调用丢失一次性预览。
	return a.components
}
