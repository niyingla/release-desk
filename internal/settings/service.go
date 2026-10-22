package settingsservice

import (
	gitrepo "release-desk/internal/git/repository"
	"release-desk/internal/storage/workspace"
)

// Service 保存连接以外的本机设置和界面偏好，更新时沿用工作区忙碌约束。
type Service struct {
	*workspace.Store
	Git *gitrepo.Git
}
