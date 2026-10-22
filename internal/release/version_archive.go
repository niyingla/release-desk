package releaseservice

import (
	"errors"
	"log/slog"

	"release-desk/internal/storage/workspace"
)

type ArchiveVersionRequest struct {
	Name string `json:"name"`
	// 使用指针区分明确恢复与请求漏传字段，避免无意改变导航状态。
	Archived *bool `json:"archived"`
}

// ArchiveVersion 归档或恢复一个已有版本，不删除版本事项与执行审计。
// 参数 body：包含精确版本名 name 和明确布尔值 archived；true 归档，false 恢复。
// 返回：落盘成功或状态未变化返回 nil；参数缺失、版本不存在、任务运行或保存失败时返回错误。
func (a *Service) ArchiveVersion(body []byte) (any, error) {
	// 归档与恢复是同一个幂等操作，重复请求不能反向切换状态。
	req, err := workspace.Decode[ArchiveVersionRequest](body)
	if err != nil {
		return nil, err
	}
	// 漏传目标状态不能被解释为恢复，操作意图必须明确。
	if req.Archived == nil {
		return nil, errors.New("请明确归档或恢复状态")
	}
	s := a.Store
	s.Mu.Lock()
	defer s.Mu.Unlock()
	// 正在执行的版本继续保持稳定，等 SQL 或验证任务收尾后再调整导航。
	if err = a.Store.EnsureIdle(); err != nil {
		return nil, err
	}
	version, exists := s.Versions[req.Name]
	if !exists {
		return nil, errors.New("版本不存在")
	}
	// 相同状态不制造重复备份，也不额外增加刷新版本号。
	if version.Version.Archived == *req.Archived {
		return nil, nil
	}
	// 独立快照只改变归档标记；原事项、发布状态和审计不参与变更。
	// save 使用统一的备份与原子写入，失败时仍保留旧内存状态。
	// 不移动版本文件，AI 文件监听仍按原路径读取该版本的后续更新。
	// 恢复沿用原版本和事项 ID，避免把历史结果变成找不到原记录的孤立审计。
	version = workspace.Clone(version)
	version.Version.Archived = *req.Archived
	if err = s.Save(version); err != nil {
		return nil, err
	}
	// 日志只记录版本和状态，不输出配置、SQL 或验证结果正文。
	slog.Info("版本归档状态更新", "version", req.Name, "archived", *req.Archived, "items", len(version.Items))
	return nil, nil
}
