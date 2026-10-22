package fileservice

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"release-desk/internal/platform/desktop"
	"release-desk/internal/storage/fileio"
	"release-desk/internal/storage/workspace"
)

// OpenFolder 打开应用允许的版本或备份目录。
// 参数 body：界面传入的 UTF-8 JSON 请求；已通过本机能力令牌鉴权，仍需进行业务字段校验。
// 返回：成功返回 nil 结果与 nil 错误；请求校验、记录定位或后续职责处理失败时返回错误，未返回业务成功凭证。
func (a *Service) OpenFolder(body []byte) (any, error) {
	// 前端提交的是目录类型，不是任意本机路径。
	// 目录白名单限定在本应用的数据范围。
	// 缺少目录时创建空目录，便于首次使用。
	// 打开版本目录不会自动生成新业务记录。
	// 打开备份目录不会自动触发恢复。

	req, e := workspace.Decode[struct {
		Kind string `json:"kind"`
	}](body)
	if e != nil {
		return nil, e
	}

	folders := map[string]string{"data": a.Store.Dir, "versions": filepath.Join(a.Store.Dir, "版本文件"), "backup": filepath.Join(a.Store.Dir, "自动备份"), "templates": filepath.Join(a.Store.Dir, "AI模板")}

	path, ok := folders[req.Kind]
	if !ok {
		return nil, errors.New("目录类型无效")
	}

	if e = os.MkdirAll(path, 0700); e != nil {
		return nil, fmt.Errorf("无法创建目录：%w", e)
	}
	// 返回实际目录给界面反馈；系统拒绝打开时继续向上传递错误。
	return path, desktop.OpenPath(path)
}

// Backup 导出版本与执行历史的完整备份。
// 无业务入参；使用接收对象或本机启动上下文，不隐式触发外部写操作。
// 返回：返回完整备份文件绝对路径；序列化或原子写入失败时返回错误。
func (a *Service) Backup(_ []byte) (any, error) {
	// 先克隆记录与历史，再释放锁进行文件写入。
	// 备份包含版本信息与逐库执行审计。
	// 数据库密码保存在独立凭据文件，不进入备份。
	// 文件名使用时间戳，避免覆盖上一份手工备份。
	// 写入成功才打开目录，防止误报导出成功。

	s := a.Store

	s.Mu.Lock()
	snapshot := workspace.Clone(map[string]any{"format": "release-desk-backup", "schema": 1, "versions": workspace.SortedVersions(s.Versions), "history": s.History})

	s.Mu.Unlock()
	folder := filepath.Join(s.Dir, "备份导出")

	path := filepath.Join(folder, "ReleaseDesk-"+time.Now().Format("20060102-150405")+".json")
	if e := fileio.WriteJSON(path, snapshot); e != nil {
		return nil, e
	}

	desktop.OpenPath(folder)
	return path, nil
}

// Restore 校验并合并恢复本应用备份。
// 参数 body：界面传入的 UTF-8 JSON 请求；已通过本机能力令牌鉴权，仍需进行业务字段校验。
// 返回：成功返回 nil 结果与 nil 错误；请求校验、记录定位或后续职责处理失败时返回错误，未返回业务成功凭证。
func (a *Service) Restore(body []byte) (any, error) {
	// 只接受本应用备份协议。
	// 恢复前校验整批版本的基本结构。
	// 后台运行期间禁止恢复，避免替换任务依赖的记录。
	// 恢复采用合并模式，不删除备份中未包含的其他版本。
	// 同名版本以备份中的完整快照为准。
	// 每份当前版本在覆盖前均自动归档。
	// 执行历史按稳定审计 ID 去重。
	// 重复恢复同一份文件不应重复统计执行次数。
	// 备份里仍为执行中的结果视为未确认。
	// 未确认状态不能当作成功，也不能自动重试。
	// 恢复记录不执行任何 SQL。
	// 恢复结果由本地原子文件持久化。

	req, e := workspace.Decode[struct {
		Format   string                  `json:"format"`
		Schema   int                     `json:"schema"`
		Versions []workspace.VersionFile `json:"versions"`
		History  []workspace.Execution   `json:"history"`
	}](body)

	if e != nil || req.Format != "release-desk-backup" || req.Schema != 1 {
		return nil, errors.New("不是有效备份")
	}

	for i := range req.Versions {
		if e = workspace.ValidateVersion(&req.Versions[i]); e != nil {
			return nil, e
		}
	}

	s := a.Store

	s.Mu.Lock()

	defer s.Mu.Unlock()

	if e = a.Store.EnsureIdle(); e != nil {
		return nil, e
	}
	// 恢复采用合并模式，同名版本整份恢复，其他版本与现有审计保留。

	for _, v := range req.Versions {
		if e = s.Save(v); e != nil {
			return nil, e
		}
	}

	seen := map[string]bool{}

	for _, r := range s.History {
		seen[r.ID] = true
	}

	for _, r := range req.History {
		if !seen[r.ID] {
			if r.State == "执行中" {
				r.State = "已中断"
			}
			s.History = append(s.History, r)
			seen[r.ID] = true
		}
	}
	return nil, s.PersistHistory()
}
