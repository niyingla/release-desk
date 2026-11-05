package gitrepo

import (
	"context"
	"sort"
	"strings"

	"release-desk/internal/versioning"
)

// ReleaseBranches 列举代码库已有的发布分支，作为手动新建版本的候选。
// 无业务入参；只读取当前读取器绑定的代码仓库，不访问远端或切换工作区。
// 返回：仅 release- 前缀的去重版本，沿用版本名称倒序；读取失败返回错误。
func (g *Git) ReleaseBranches() ([]GitVersion, error) {
	// 同时查看本地分支和已缓存的远端跟踪分支，尚未拉取的分支不会凭空出现。
	text, err := g.runVersionGit(context.Background(), "for-each-ref", "--format=%(refname)", "refs/heads/", "refs/remotes/")
	if err != nil {
		return nil, err
	}
	// 同名本地与远端分支只显示一个版本；不借用 SQL 目录或脚本数量判断候选。
	found := map[string]bool{}
	list := []GitVersion{}
	for _, ref := range strings.Fields(text) {
		// 先去掉引用命名空间和远端名，再判断实际分支前缀，避免 feature/release-* 混入。
		branch := strings.TrimPrefix(ref, "refs/heads/")
		if strings.HasPrefix(ref, "refs/remotes/") {
			_, branch, _ = strings.Cut(strings.TrimPrefix(ref, "refs/remotes/"), "/")
		}
		if !strings.HasPrefix(branch, "release-") {
			// 只依据真实分支前缀筛选，feature、hotfix、测试分支都不属于本列表。
			continue
		}
		// 软件版本名称继续使用前缀后的原文，选择候选不触发创建、扫描或导入。
		name := strings.TrimPrefix(branch, "release-")
		if name == "" || found[name] {
			// 空名称无法创建记录，重复跟踪引用也不能让同一版本出现多次。
			continue
		}
		found[name] = true
		// 保存完整分支名作为来源说明；展示名称仍使用现有版本号格式。
		list = append(list, GitVersion{Name: name, Folder: branch})
	}
	// 复用现有数字版本比较，5.10 位于 5.9 前，保持用户原有倒序体验。
	sort.Slice(list, func(i, j int) bool { return versioning.CompareVersion(list[i].Name, list[j].Name) > 0 })
	return list, nil
}
