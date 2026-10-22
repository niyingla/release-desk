package gitservice

import (
	gitrepo "release-desk/internal/git/repository"
	"release-desk/internal/storage/workspace"
)

// Service 将可信 Git 候选及作者原文增量导入工作区。
type Service struct {
	*workspace.Store
	Git *gitrepo.Git
}
