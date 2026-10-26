package gitservice

import (
	"errors"

	gitrepo "release-desk/internal/git/repository"
	"release-desk/internal/identity"
	"release-desk/internal/storage/workspace"
)

type SQLRefresh struct {
	Updated   int      `json:"updated"`
	Unchanged int      `json:"unchanged"`
	Conflicts []string `json:"conflicts"`
}

type SQLUpdate struct{ ID, Before, Content, Commit, Author string }

// GitRefresh 同步远端并刷新当前版本已经绑定的 Git SQL，不自动加入新文件。
// 参数 body：包含精确版本名 name 的本机请求；刷新仅针对该版本已导入记录。
// 返回：更新数、未变更数和冲突文件名；运行任务或 Git 错误时拒绝刷新。
func (a *Service) GitRefresh(body []byte) (any, error) {
	// 当前版本是刷新边界，其他版本的脚本不受本按钮影响。
	// 预览和执行历史不在此处删除，内容改变后由摘要区分新旧结果。
	// 用户可以继续查看工作台，网络读取不会长时间占用记录锁。
	// 所有读取结束后还要核对记录未在期间被手工修改。
	// 执行中的记录保持固定，不能在用户等待结果时偷偷替换其内容。
	// 只同步已关联文件，新增路径仍需要用户在导入窗口明确选择。
	req, err := workspace.Decode[workspace.Version](body)
	if err != nil {
		return nil, err
	}
	a.Store.Mu.Lock()
	err = a.Store.EnsureIdle()
	version, exists := a.Store.Versions[req.Name]
	version = workspace.Clone(version)
	a.Store.Mu.Unlock()
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errors.New("版本不存在")
	}

	// 网络读取不占记录锁，界面仍可查看其他事项。
	// fetch 保留工作区，候选内容来自配置作者在当前分支的最近提交。
	if err = a.Git.Sync(); err != nil {
		return nil, err
	}
	updates, result, err := a.CollectSQLUpdates(version)
	if err != nil {
		return nil, err
	}
	return a.ApplySQLUpdates(req.Name, updates, result)
}

// CollectSQLUpdates 比较来源提交，准备不会覆盖人工修改的 SQL 更新集合。
// 参数 version：刷新开始时固定的版本快照，只有 source_commit 已存在的记录参与。
// 返回：待应用的更新、统计和错误；手工内容与来源摘要不一致时列入冲突。
func (a *Service) CollectSQLUpdates(version workspace.VersionFile) ([]SQLUpdate, SQLRefresh, error) {
	// 只有明确绑定了 Git 来源的 SQL 才参加自动内容更新。
	// 纯手工录入的 SQL 继续由用户维护，不能按相似文件名猜测来源。
	// 每个来源路径使用作者最近一次可见提交，保留原始提交定位信息。
	// 相同提交只记为未变更，不制造重复备份或新的待执行内容。
	// 软件内被编辑的内容优先保护，不用远端提交静默覆盖。
	// 作者候选中消失的路径列入冲突，而不是删除记录。
	// 新路径仍由用户手动选择导入，防止自动扩大本版本执行范围。
	// 待应用更新保留刷新前摘要，提交时用于第二次并发校验。
	result := SQLRefresh{Conflicts: []string{}}
	candidates, err := a.Git.Scripts(version.Version.Name)
	if err != nil {
		return nil, result, err
	}
	byPath := map[string]gitrepo.GitScript{}
	for _, record := range candidates {
		byPath[record.Path] = record
	}
	updates := []SQLUpdate{}

	// source_digest 是导入时的 SQL 原文摘要，用户在软件内编辑后不会被更新。
	// 无摘要的旧记录不能猜测为未编辑，必须让用户手动选择是否重新导入。
	for _, item := range version.Items {
		if item.Kind != "sql" || item.Data["source_commit"] == "" {
			continue
		}
		candidate, exists := byPath[item.Data["source_path"]]
		if !exists {
			result.Conflicts = append(result.Conflicts, item.Data["name"]+"（来源已不在候选清单）")
			continue
		}
		if candidate.Commit == item.Data["source_commit"] {
			result.Unchanged++
			continue
		}
		before := identity.Digest(item.Data["sql"])
		if item.Data["source_digest"] == "" || before != item.Data["source_digest"] {
			result.Conflicts = append(result.Conflicts, item.Data["name"]+"（软件内已编辑）")
			continue
		}

		// 读取整份新提交内容，不按行拼接，避免产生不存在于任何提交的脚本。
		// 摘要变更会让旧执行结果只留在历史，不再表示新内容已经执行。
		content, err := a.Git.Content(candidate)
		if err != nil {
			return nil, result, err
		}
		updates = append(updates, SQLUpdate{ID: item.ID, Before: before, Content: content, Commit: candidate.Commit, Author: candidate.Author})
	}
	return updates, result, nil
}

// ApplySQLUpdates 再次核对当前内容后保存刷新结果，保留排序和回滚说明，SQL 类型按新正文重算。
// 参数 name：需要刷新 SQL 来源的版本名。
// 参数 updates：经过来源摘要比较的候选，不包含未核对的工作区正文。
// 参数 result：前序刷新统计与冲突清单，保存后补充最终数量。
// 返回：最终统计；保存失败或刷新期间开始执行时返回错误，不替换旧快照。
func (a *Service) ApplySQLUpdates(name string, updates []SQLUpdate, result SQLRefresh) (any, error) {
	// 只改 SQL 原文与来源信息，不改变所属端和用户排序；类型由新正文重新识别。
	// 回滚说明与人工待办状态都由原记录保留。
	// 磁盘写入使用完整版本快照并自动归档旧文件。
	// 有冲突的单项跳过，其他安全更新仍可保存。
	// 新内容的 SHA 与旧审计不同，不会沿用旧内容的成功状态。
	// 保存失败时不把更新数量作为成功结果返回给界面。
	// 完整快照只保存一次，避免每个脚本分别落盘造成半批次记录。
	// 刷新不会连接测试库或主动调用 Codex。
	a.Store.Mu.Lock()
	defer a.Store.Mu.Unlock()
	// 用户可能在网络读取期间开始操作，再检查一次才能保证正在执行的 SQL 不变。
	// 每项保存前再比旧摘要，防止覆盖这段时间内的人工编辑。
	if err := a.Store.EnsureIdle(); err != nil {
		return nil, err
	}
	version, exists := a.Store.Versions[name]
	if !exists {
		return nil, errors.New("版本已被删除")
	}
	version = workspace.Clone(version)
	for _, update := range updates {
		for index, item := range version.Items {
			if item.ID != update.ID {
				continue
			}
			if identity.Digest(item.Data["sql"]) != update.Before {
				result.Conflicts = append(result.Conflicts, item.Data["name"]+"（刷新期间被编辑）")
				break
			}
			item.Data["sql"] = update.Content
			item.Data["source_commit"] = update.Commit
			item.Data["source_author"] = update.Author
			item.Data["source_digest"] = identity.Digest(update.Content)
			version.Items[index] = item
			result.Updated++
			break
		}
	}
	// 没有更新时不制造额外备份，刷新不能改变人工发布或验收状态。
	// 新内容的执行结果由内容摘要自然隔离，旧审计永久保留。
	if result.Updated > 0 {
		if err := a.Store.Save(version); err != nil {
			return nil, err
		}
	}
	return result, nil
}
