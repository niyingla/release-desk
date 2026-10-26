package workspace

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"release-desk/internal/identity"
	"release-desk/internal/policy"
	"release-desk/internal/storage/fileio"
)

// ScanFiles 轮询并增量合并本地 AI 版本文件，保留执行状态与历史。
// 无业务入参；使用接收对象或本机启动上下文，不隐式触发外部写操作。
// 不返回业务值；运行结果通过受控状态、记录或日志反馈，失败不会伪装为成功。
func (s *Store) ScanFiles() {
	// 每轮只处理扩展名为 JSON 的版本文件。
	// 任务运行期间先跳过，下一轮仍会检查。

	s.Mu.Lock()
	defer s.Mu.Unlock()
	// 执行或验证固定使用启动时的版本快照，外部 AI 变更等任务结束后再合并。
	// 避免执行中的 SQL 被文件监听器替换，导致界面与实际操作不一致。

	if s.SQLActive() || s.AIBusy {
		return
	}
	files, _ := filepath.Glob(filepath.Join(s.Dir, "版本文件", "*.json"))
	for _, path := range files {
		if err := s.ImportExternalFile(path); err != nil {
			s.Notice = "AI 文件未导入：" + filepath.Base(path) + "：" + err.Error()
		}
	}
}

// ReadVersionCandidate 读取与校验发生变化的候选版本文件。
// 参数 path：本方法要读取、写入或打开的受控本机路径；不得由业务正文拼接任意系统路径。
// 返回：依次返回版本候选、原文摘要、是否变化和错误；未变化时不触发导入。
func (s *Store) ReadVersionCandidate(path string) (VersionFile, string, bool, error) {
	// 读取前检查文件大小，防止误放大文件。
	// 文件改时间但内容不变时不重复导入。
	// 完整解析成功之后再检查协议。
	// 文件名与版本名必须一致。
	// 返回未变化时不分配新版本记录。

	var version VersionFile

	info, err := os.Stat(path)
	if err != nil {
		return version, "", false, err
	}
	// 大小上限防止误放入大文件后阻塞本地工作台，文件不会被删除。
	// 摘要用于识别内容变更，单纯修改文件时间不会触发重复导入。
	if info.Size() > policy.MaxVersionFileBytes {
		return version, "", false, errors.New("版本文件超过大小上限")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return version, "", false, err
	}

	hash := identity.Digest(string(data))
	if hash == s.Hashes[path] {
		return version, hash, false, nil
	}

	// 文件名绑定版本名称，防止两个不同文件轮流覆盖同一版本。
	// JSON 尚未写完整时返回错误，下一轮扫描仍会继续尝试。
	if err = json.Unmarshal(data, &version); err != nil {
		return version, "", false, errors.New("JSON 格式错误")
	}

	if err = ValidateVersion(&version); err != nil {
		return version, "", false, err
	}
	if filepath.Base(path) != version.Version.Name+".json" {
		return version, "", false, errors.New("文件名必须是版本名称加 .json")
	}
	return version, hash, true, nil
}

// ImportExternalFile 备份并合并单个外部版本文件。
// 参数 path：本方法要读取、写入或打开的受控本机路径；不得由业务正文拼接任意系统路径。
// 返回：成功或未变化返回 nil；校验、备份、合并或写回失败时返回错误。
func (s *Store) ImportExternalFile(path string) error {
	// 输入需通过协议与字段校验。
	// 旧内存快照是覆盖前备份的可信来源。
	// 人工状态先合并再写回文件。
	// 备份失败时拒绝接受新内容。
	// 写回失败不更新内存版本。

	version, _, changed, err := s.ReadVersionCandidate(path)

	if err != nil || !changed {
		return err
	}
	if previous, exists := s.Versions[version.Version.Name]; exists {
		// 外部文件可能已经覆盖磁盘，因此备份必须来自旧内存快照。
		// 合并失败时不写回文件，便于 AI 读取错误后修正原始输入。
		version, err = MergeExternalVersion(previous, version)
		if err != nil {
			return err
		}
		backup := filepath.Join(s.Dir, "自动备份", version.Version.Name+"-AI-"+identity.NewID()+".json")
		if err = fileio.WriteJSON(backup, previous); err != nil {
			return err
		}
	}

	// 先落盘、后更新内存；写回人工状态后，AI 下次读取也能看到完整结果。
	// Hashes 使用规范化文件摘要，监听器不会把自己的写回当作下一次外部修改。

	if err = fileio.WriteJSON(path, version); err != nil {
		return err
	}
	canonical, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	s.Versions[version.Version.Name] = version
	s.Hashes[path] = identity.Digest(string(canonical))

	s.Notice = "已读取版本文件：" + version.Version.Name
	s.Revision++
	return nil
}

// MergeExternalVersion 保护人工状态并增量合并 AI 内容。
// 参数 previous：写入前的可信版本快照，用于合并和自动备份。
// 参数 incoming：完成基本格式校验的外部版本候选，不直接覆盖人工状态。
// 返回：返回独立合并快照；同一 ID 改变业务类型时返回错误并保留旧对象。
func MergeExternalVersion(previous, incoming VersionFile) (VersionFile, error) {
	// 按稳定 ID 找到同一业务项。
	// 不会根据相似名称合并不同记录。
	// 业务类型改变会使整份候选失败。
	// 人工上线时间不接受 AI 重置。
	// 待办状态和验证历史由旧记录保留。
	// 条件变化时旧证据不再表示当前验证通过。

	result := Clone(incoming)
	// 归档由用户手动维护，AI 更新或旧模板省略该字段时保持原有状态。
	result.Version.Archived = previous.Version.Archived

	remaining := map[string]Item{}

	for _, item := range previous.Items {
		remaining[item.ID] = item
	}

	for index, item := range result.Items {
		old, exists := remaining[item.ID]
		if !exists {
			continue
		}
		if old.Kind != item.Kind {
			return incoming, errors.New("不能改变已有记录的类型：" + item.ID)
		}

		// AI 负责事实内容，人工发布状态、待办状态和验收历史不接受外部覆盖。
		// 因缺少旧项而省略的记录也不会被删除，删除只能使用工作台明确操作。
		item.Online = old.Online
		item.OnlineAt = old.OnlineAt
		item.Status = old.Status
		item.VerifyRuns = old.VerifyRuns
		item.VerifyState = PendingVerificationState(item)
		if VerificationHash(old) == VerificationHash(item) {
			item.VerifyState = old.VerifyState
		}
		result.Items[index] = item
		delete(remaining, item.ID)
	}
	// 保留原先的顺序，避免遍历 map 造成界面记录每次随机重排。
	// 新增项跟随文件顺序，已有但被省略的项继续放在末尾。

	for _, item := range previous.Items {
		if _, exists := remaining[item.ID]; exists {
			result.Items = append(result.Items, item)
		}
	}
	return result, nil
}
