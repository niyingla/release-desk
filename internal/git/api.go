package gitservice

import (
	"errors"
	"path/filepath"
	"strconv"

	gitrepo "release-desk/internal/git/repository"
	"release-desk/internal/identity"
	"release-desk/internal/storage/workspace"
)

// GitVersions 读取 Git 版本候选与来源信息。
// 无业务入参；使用接收对象或本机启动上下文，不隐式触发外部写操作。
// 返回：返回倒序版本候选、当前分支名与来源路径；Git 读取失败时返回错误。
func (a *Service) GitVersions(_ []byte) (any, error) {
	// 只读取当前选定 Git 引用，不切换工作区。
	// 返回分支信息用于用户识别来源。
	// 仓库路径来自用户设置而非界面任意文件路径。

	list, e := a.Git.Versions()
	if e != nil {
		return nil, e
	}

	branch, _ := a.Git.Branch()
	// 路径只取配置快照，避免用户修改设置时与读取结果发生数据竞争。
	path := a.Git.Directory()
	return map[string]any{"versions": list, "branch": branch, "path": path}, nil
}

// GitSync 同步当前分支的远端对象。
// 无业务入参；使用接收对象或本机启动上下文，不隐式触发外部写操作。
// 返回：成功返回 nil 结果与 nil 错误；请求校验、记录定位或后续职责处理失败时返回错误，未返回业务成功凭证。
func (a *Service) GitSync(_ []byte) (any, error) {
	// 同步只更新远端跟踪引用。
	// 本地未提交 SQL 和当前分支保持原样。

	if e := a.Git.Sync(); e != nil {
		return nil, e
	}
	return a.GitVersions(nil)
}

// GitScripts 查询指定版本和作者涉及的 SQL 提交。
// 参数 body：界面传入的 UTF-8 JSON 请求；已通过本机能力令牌鉴权，仍需进行业务字段校验。
// 返回：返回指定作者与完整版本对应的 SQL 提交元信息；不返回或执行脚本正文。
func (a *Service) GitScripts(body []byte) (any, error) {
	// 版本过滤按完整数字版本匹配。
	// 作者范围由本机设置控制。

	req, e := workspace.Decode[workspace.Version](body)

	if e != nil {
		return nil, e
	}
	return a.Git.Scripts(req.Name)
}

// GitImport 将所选 Git SQL 导入当前版本。
// 参数 body：界面传入的 UTF-8 JSON 请求；已通过本机能力令牌鉴权，仍需进行业务字段校验。
// 返回：返回已选文件数与实际新增数；任一源文件无效时不开始合并。
func (a *Service) GitImport(body []byte) (any, error) {
	// 用户选择的文件只作为候选。
	// 每个候选都要由后端作者提交清单再次确认。
	// 读取 Git 对象，不依赖工作区文件当前是否存在。
	// 先读完全部文件，再开始修改版本记录。
	// 任何源文件读取失败都取消本批导入。
	// 文件路径形成稳定 ID，重复导入不会增加重复行。
	// 源提交 SHA 与作者随记录保存，方便追溯。
	// SQL 正文完整保留换行和中文。
	// 导入只记录文本，不连接数据库。
	// 拿到记录锁后再次检查后台任务状态。
	// 版本必须已经由用户手动加入工作台。
	// 合并时使用版本副本，校验失败保持原记录。
	// 相同来源提交无需重复覆盖。
	// 来源提交改变才更新该文件的 SQL。
	// 手工设置的执行顺序需要保留。
	// 回滚说明可能是用户后补，不得被导入清空。
	// 新增项给予默认顺序，用户仍可调整。
	// 导入数量区分已选文件和实际新增记录。

	req, e := workspace.Decode[struct {
		Version string              `json:"version"`
		Scripts []gitrepo.GitScript `json:"scripts"`
	}](body)

	if e != nil {
		return nil, e
	}
	// 先读取并校验全部源文件，任一失败都不导入，避免只写入半批记录。

	records := []workspace.Item{}

	for _, script := range req.Scripts {
		sql, e := a.Git.Content(script)
		if e != nil {
			return nil, e
		}
		id := "git-" + identity.Digest(script.Path)[:24]
		name := filepath.Base(script.Path)
		records = append(records, workspace.Item{ID: id, Kind: "sql", Action: "新增", Status: "待处理", Data: map[string]string{"name": name, "side": script.Side, "sql": sql, "order": "1", "source_path": script.Path, "source_commit": script.Commit, "source_author": script.Author, "source_digest": identity.Digest(sql), "description": "导入 " + script.Author + " 的 Git 提交，提交日期 " + script.Date}})
	}

	s := a.Store

	s.Mu.Lock()

	defer s.Mu.Unlock()

	if e = a.Store.EnsureIdle(); e != nil {
		return nil, e
	}

	v, ok := s.Versions[req.Version]

	if !ok {
		return nil, errors.New("版本不存在")
	}

	v = workspace.Clone(v)

	count := 0

	for _, r := range records {
		found := false
		for i, old := range v.Items {
			if old.ID == r.ID {
				found = true
				if old.Data["source_commit"] == r.Data["source_commit"] {
					break
				}
				r.Status = old.Status
				r.Data["order"] = old.Data["order"]
				r.Data["rollback"] = old.Data["rollback"]
				v.Items[i] = r
				break
			}
		}
		if !found {
			r.Data["order"] = strconv.Itoa(len(v.Items) + 1)
			v.Items = append(v.Items, r)
			count++
		}
	}

	if e = s.Save(v); e != nil {
		return nil, e
	}

	return map[string]int{"added": count, "selected": len(records)}, nil
}
