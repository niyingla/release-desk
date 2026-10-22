package releaseservice

import (
	"errors"
	"os"
	"path/filepath"

	"release-desk/internal/identity"
	"release-desk/internal/storage/fileio"
	"release-desk/internal/storage/workspace"
)

// SaveVersion 保存版本基本信息并保留原事项。
// 参数 body：界面传入的 UTF-8 JSON 请求；已通过本机能力令牌鉴权，仍需进行业务字段校验。
// 返回：成功返回 nil 结果与 nil 错误；请求校验、记录定位或后续职责处理失败时返回错误，未返回业务成功凭证。
func (a *Service) SaveVersion(body []byte) (any, error) {
	// 版本名称同时决定本地 JSON 文件名。
	// 同名保存更新版本基本信息，不新建重复版本。
	// 旧的事项列表仍由原版本持有。
	// 版本发布状态不自动更新事项上线状态。

	req, e := workspace.Decode[workspace.Version](body)

	if e != nil {
		return nil, e
	}
	s := a.Store
	s.Mu.Lock()

	defer s.Mu.Unlock()
	// 同名版本只更新基本信息，记录内容保留。
	v, ok := s.Versions[req.Name]

	if !ok {
		v = workspace.VersionFile{Format: "release-desk-version", Schema: 1, Items: []workspace.Item{}}
	}
	// 归档只能通过独立操作变更，普通表单省略字段不能隐式恢复版本。
	req.Archived = v.Version.Archived
	v.Version = req
	return nil, s.Save(v)
}

// DeleteVersion 归档后删除用户选中的版本。
// 参数 body：界面传入的 UTF-8 JSON 请求；已通过本机能力令牌鉴权，仍需进行业务字段校验。
// 返回：成功返回 nil 结果与 nil 错误；请求校验、记录定位或后续职责处理失败时返回错误，未返回业务成功凭证。
func (a *Service) DeleteVersion(body []byte) (any, error) {
	// 删除是用户在界面明确触发的操作。
	// 执行或验证运行期间不允许删除所属版本。
	// 删除前检查版本确实存在，避免归档错误对象。
	// 先把完整版本归档到自动备份目录。
	// 备份失败时不删除原文件。
	// 执行审计单独保存，不随版本删除。
	// 文件删除成功后才清除内存索引。
	// 外部已经删除的文件按已删除处理。

	req, e := workspace.Decode[workspace.Version](body)

	if e != nil {
		return nil, e
	}
	s := a.Store

	s.Mu.Lock()
	defer s.Mu.Unlock()

	if e = a.Store.EnsureIdle(); e != nil {
		return nil, e
	}
	v, ok := s.Versions[req.Name]

	if !ok {
		return nil, errors.New("版本不存在")
	}
	// 删除前归档，执行审计保留在独立历史文件中。
	if e = fileio.WriteJSON(filepath.Join(s.Dir, "自动备份", req.Name+"-删除-"+identity.NewID()+".json"), v); e != nil {
		return nil, e
	}

	path := filepath.Join(s.Dir, "版本文件", req.Name+".json")
	if e = os.Remove(path); e != nil && !os.IsNotExist(e) {
		return nil, e
	}

	delete(s.Versions, req.Name)
	delete(s.Hashes, path)

	s.Revision++
	return nil, nil
}

// SaveItem 新增或更新一个版本事项。
// 参数 body：界面传入的 UTF-8 JSON 请求；已通过本机能力令牌鉴权，仍需进行业务字段校验。
// 返回：成功返回 nil 结果与 nil 错误；请求校验、记录定位或后续职责处理失败时返回错误，未返回业务成功凭证。
func (a *Service) SaveItem(body []byte) (any, error) {
	// 事项始终归属于用户当前选择的版本。
	// 修改期间不能存在运行中的批量操作。
	// 克隆原版本，校验失败不能污染内存记录。
	// 同一稳定 ID 不允许改变业务类型。
	// 同名但不同 ID 的事项仍可独立记录。
	// 保存业务内容不改变实际 SQL 执行审计。
	// 线上标记来自已有人工状态，忽略前端伪造值。
	// 人工完成状态可以在编辑表单中明确修改。
	// Codex 历史由后台结果维护，不信任表单提交。
	// 验证条件变化会使原结论失效。
	// 旧的验证历史仍保留，便于追溯。
	// 新增事项生成不可预测的唯一 ID。
	// 新增项默认未上线、未验收。
	// 记录时间用于定位最近编辑，不充当上线证据。

	req, e := workspace.Decode[workspace.ItemRequest](body)

	if e != nil {
		return nil, e
	}
	s := a.Store

	s.Mu.Lock()

	defer s.Mu.Unlock()
	if e = a.Store.EnsureIdle(); e != nil {
		return nil, e
	}

	v, ok := s.Versions[req.Version]

	if !ok {
		return nil, errors.New("请先选择版本")
	}
	v = workspace.Clone(v)

	r := req.Item
	// 内容摘要只供界面匹配历史，不作为用户维护字段写回版本文件。
	delete(r.Data, "content_digest")

	r.Updated = workspace.Stamp()
	// 手动编辑只修改业务字段，执行审计与 Codex 结果不信任前端传入。

	for i, old := range v.Items {
		if old.ID == r.ID {
			if old.Kind != r.Kind {
				return nil, errors.New("不能更改已有记录类型")
			}
			r.VerifyRuns = old.VerifyRuns
			r.Online = old.Online
			r.OnlineAt = old.OnlineAt
			r.VerifyState = old.VerifyState
			if workspace.VerificationHash(old) != workspace.VerificationHash(r) {
				r.VerifyState = "待验证"
			}
			v.Items[i] = r
			return nil, s.Save(v)
		}
	}
	if r.ID == "" {
		r.ID = identity.NewID()
	}

	r.Online = "待上线"

	r.OnlineAt = ""
	r.VerifyState = "待验证"

	r.VerifyRuns = nil

	v.Items = append(v.Items, r)
	return nil, s.Save(v)
}

// DeleteItem 删除明确选中的单项记录。
// 参数 body：界面传入的 UTF-8 JSON 请求；已通过本机能力令牌鉴权，仍需进行业务字段校验。
// 返回：成功返回 nil 结果与 nil 错误；请求校验、记录定位或后续职责处理失败时返回错误，未返回业务成功凭证。
func (a *Service) DeleteItem(body []byte) (any, error) {
	// 只根据版本与 ID 定位一项，不能跨版本删除。
	// 运行中的任务完成前不允许删除。
	// 找到索引后操作独立版本副本。
	// 删除前的文件快照由统一保存入口归档。
	// 执行历史不在此处删除，仍可通过完整备份追溯。
	// 界面记录删除不会回滚已执行的 SQL。

	req, e := workspace.Decode[workspace.ItemRequest](body)

	if e != nil {
		return nil, e
	}
	s := a.Store

	s.Mu.Lock()
	defer s.Mu.Unlock()

	if e = a.Store.EnsureIdle(); e != nil {
		return nil, e
	}
	v, _, index, e := s.Find(req.Version, req.ID)

	if e != nil {
		return nil, e
	}
	v = workspace.Clone(v)

	v.Items = append(v.Items[:index], v.Items[index+1:]...)
	return nil, s.Save(v)
}

// ToggleOnline 切换配置或定时任务的人工上线标记。
// 参数 body：界面传入的 UTF-8 JSON 请求；已通过本机能力令牌鉴权，仍需进行业务字段校验。
// 返回：成功返回 nil 结果与 nil 错误；请求校验、记录定位或后续职责处理失败时返回错误，未返回业务成功凭证。
func (a *Service) ToggleOnline(body []byte) (any, error) {
	// 此状态只属于定时任务和配置发布。
	// 它表示人工确认，不能推导 SQL 执行结果。
	// 必须同时核对版本和记录 ID。
	// 切换前克隆版本，不直接改动共享对象。
	// 标记已上线时记录人工操作时间。
	// 取消上线标记时清除相应时间。
	// 切换不会连接 Nacos 或 XXL-Job。
	// 未通过界面确认的记录仍保持待上线。
	// 保存使用原子替换，避免状态丢失。

	req, e := workspace.Decode[workspace.ItemRequest](body)

	if e != nil {
		return nil, e
	}
	s := a.Store

	s.Mu.Lock()

	defer s.Mu.Unlock()
	v, r, index, e := s.Find(req.Version, req.ID)

	if e != nil {
		return nil, e
	}

	if r.Kind != "job" && r.Kind != "config" {
		return nil, errors.New("只有配置和定时任务维护线上状态")
	}
	v = workspace.Clone(v)

	if r.Online == "已上线" {
		r.Online = "待上线"
		r.OnlineAt = ""
	} else {
		r.Online = "已上线"
		r.OnlineAt = workspace.Stamp()
	}

	v.Items[index] = r
	return nil, s.Save(v)
}
