// Package tasks 保存统一任务历史；不持有数据库连接、SQL 原文或 Codex 登录凭据。
package tasks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"release-desk/internal/storage/fileio"
)

type Row struct {
	ID        string          `json:"id"`
	Title     string          `json:"title"`
	State     string          `json:"state"`
	Detail    string          `json:"detail"`
	Condition string          `json:"condition,omitempty"`
	Evidence  []string        `json:"evidence,omitempty"`
	Children  []Row           `json:"children,omitempty"`
	Progress  []ProgressEntry `json:"progress,omitempty"`
}

// ProgressEntry 是可公开的核验行动与发现摘要，不保存内部推理或原始工具响应。
type ProgressEntry struct {
	At      string `json:"at"`
	Summary string `json:"summary"`
}
type Record struct {
	StopRequested bool   `json:"stop_requested,omitempty"`
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	Version       string `json:"version"`
	Title         string `json:"title"`
	Status        string `json:"status"`
	Running       bool   `json:"running"`
	Progress      string `json:"progress"`
	Started       string `json:"started"`
	Finished      string `json:"finished"`
	Rows          []Row  `json:"rows,omitempty"`
}
type Repository struct {
	mu      sync.Mutex
	dir     string
	records map[string]Record
}

var validID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

// Open 恢复历史并将异常退出留下的运行状态标成中断。
// 参数 dir：本应用专用任务历史目录，不读取其他业务或系统目录。
// 返回：可用仓库；文件损坏或恢复落盘失败时保留原文件并返回错误。
func Open(dir string) (*Repository, error) {
	// 损坏的任务文件必须保留给用户排查，不能自动覆盖为空历史。
	// 文件名必须与记录 ID 相同，避免恢复时把两个任务混为一轮。
	// 未完成任务在新进程中不再拥有执行器，因此必须终止旧的运行标记。
	// 已成功的子项不受父任务中断影响，已有证据继续保留。
	// 恢复后的写盘失败会阻止正常启动，避免后续误把旧状态当作可靠结果。
	// 临时文件仅是未完成的替换过程，不作为正式历史读取。
	// 目录创建范围固定在应用数据目录，不扫描用户其他 JSON。
	// 历史与版本文件独立，删除或归档版本不会删除执行结果。
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	repo := &Repository{dir: dir, records: map[string]Record{}}
	for _, entry := range entries {
		// 仅恢复本协议 JSON，忽略原子写入残留的临时文件。
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		payload, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var record Record
		if err = json.Unmarshal(payload, &record); err != nil || !validID.MatchString(record.ID) || entry.Name() != record.ID+".json" {
			return nil, fmt.Errorf("任务历史损坏，已保留：%s", entry.Name())
		}
		if record.Running {
			// 上次进程消失不等于执行失败，更不能自动重跑潜在已提交的 SQL。
			record.Running = false
			record.Status = "已中断"
			record.Progress = "上次退出时未完成，请核对实际结果"
			record.Finished = now()
			record.Rows = recoverRows(record.Rows)
			if err = fileio.WriteJSON(filepath.Join(dir, entry.Name()), record); err != nil {
				return nil, err
			}
		}
		repo.records[record.ID] = record
	}
	// 返回前已可靠保存恢复状态，界面不会把旧任务显示成仍在运行。
	return repo, nil
}

// Save 原子保存一轮任务快照，保持首次开始时间与停止请求。
// 参数 record：已去除连接信息的进度或结果；ID 只能是安全的单文件标识。
// 返回：落盘成功返回 nil；校验或写盘失败不替换内存中的可靠历史。
func (r *Repository) Save(record Record) error {
	// 开始时间属于本轮身份，后续进度不允许重置它来掩盖运行时长。
	// 停止请求优先于普通进度，取消过程中不能重新显示为正常执行。
	// 任务终态不可逆，迟到的协程响应不能重新占用工作区。
	// 最终结束时间只在有明确终态时产生，不在停止请求时提前填写。
	// 序列化失败或磁盘错误不能更新可靠缓存，界面会保留上一份历史。
	// 同一任务的进度串行写入，避免两个回调互相覆盖文件。
	// ID 同时用于文件名，不能允许路径或其他用户指定目录。
	if !validID.MatchString(record.ID) {
		return fmt.Errorf("任务标识无效")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	previous, exists := r.records[record.ID]
	if exists {
		// 完成后的历史不可被迟到的运行进度重新激活。
		if !previous.Running && record.Running {
			return fmt.Errorf("任务已结束")
		}
		record.Started = previous.Started
		// 停止意图跨进度更新保留，最终仍可回看哪些结果来自人工停止。
		record.StopRequested = record.StopRequested || previous.StopRequested || previous.Status == "正在停止"
		if previous.Status == "正在停止" && record.Running {
			record.Status = "正在停止"
		}
		if !record.Running && record.StopRequested && record.Status != "有失败" && record.Status != "已中断" {
			record.Status = "已停止"
		}
	}
	if record.Started == "" {
		record.Started = now()
	}
	if !record.Running && record.Finished == "" {
		record.Finished = now()
	}
	// 先写盘再替换缓存；任务中心不展示未落盘的“已完成”凭证。
	if err := fileio.WriteJSON(filepath.Join(r.dir, record.ID+".json"), record); err != nil {
		return err
	}
	r.records[record.ID] = copyRecord(record)
	return nil
}

// List 返回按启动时间倒序的轻量目录。
// 无业务入参；摘要不携带生产日志证据或逐库明细。
// 返回：独立记录列表；完整结果按 ID 调用 Get。
func (r *Repository) List() []Record {
	// 目录只承担导航，具体证据和逐库状态在查看详情时读取。
	// 倒序依据真实开始时间，同版本多轮重试也会保留独立记录。
	// 返回列表是独立容器，筛选和排序不能改动历史缓存。
	// 列表与明细分离，主界面轮询不会反复传输全部日志证据。
	r.mu.Lock()
	defer r.mu.Unlock()
	result := []Record{}
	for _, record := range r.records {
		record.Rows = nil
		result = append(result, record)
	}
	// 同一时间的记录使用随机 ID 稳定排序，避免页面每次刷新跳动。
	sort.Slice(result, func(i, j int) bool {
		left, _ := time.Parse(time.RFC3339Nano, result[i].Started)
		right, _ := time.Parse(time.RFC3339Nano, result[j].Started)
		// 纳秒文本长度可不同，排序按真实时间而非 RFC3339 字符串字典序。
		if left.Equal(right) {
			return result[i].ID > result[j].ID
		}
		return left.After(right)
	})
	return result
}

// Get 读取一轮任务的完整结果副本。
// 参数 id：任务目录中的精确 ID，不跨版本按标题模糊寻找。
// 返回：记录及存在标记；副本可供 HTTP 编码而不与进度更新竞争。
func (r *Repository) Get(id string) (Record, bool) {
	// 结果复制与查找在同一锁内，避免返回一半新一半旧的行。
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.records[id]
	// 深拷贝包含子行和证据，调用方不能修改仓库缓存。
	return copyRecord(record), ok
}

// recoverRows 区分未开始与结果未知，并递归保留已完成的子项。
// 参数 rows：上次进程退出前持久化的行，不再由其他任务共享。
// 返回：恢复后的独立状态树；已有成功、失败和证据原文不变。
func recoverRows(rows []Row) []Row {
	// 执行中表示可能已经产生外部效果，需要保留为结果未知。
	// 等待中的子项未被调用，应与真正执行失败明确区分。
	// 只修改非终态，历史成功不能因为父任务中断而被清空。
	for i := range rows {
		switch rows[i].State {
		case "等待中":
			rows[i].State = "未开始"
		case "执行中", "验证中":
			rows[i].State = "已中断"
		}
		// SQL 脚本下的每个目标库也必须恢复，不能仅修改最外层标题。
		rows[i].Children = recoverRows(rows[i].Children)
	}
	return rows
}

// copyRecord 复制可序列化任务数据，隔离行和证据切片。
// 参数 record：本应用已经构造的任务协议，不包含任意对象指针。
// 返回：无共享可变行的副本，序列化边界由固定结构保证。
func copyRecord(record Record) Record {
	// 固定结构只包含 JSON 基本类型，复制不执行用户代码。
	payload, _ := json.Marshal(record)
	var result Record
	// 复制过程不涉及文件或网络，错误不能来自外部文本。
	json.Unmarshal(payload, &result)
	return result
}

// now 生成与时区无关的任务时间戳。
// 无业务入参；使用系统时钟，不推断线上发布时间。
// 返回：UTC RFC3339Nano 时间，界面按用户本地时区展示。
func now() string {
	// 统一 UTC 便于跨电脑备份和稳定排序，保留纳秒以区分连续操作。
	return time.Now().UTC().Format(time.RFC3339Nano)
}
