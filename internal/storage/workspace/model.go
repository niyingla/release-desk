package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"release-desk/internal/identity"
	"release-desk/internal/storage/fileio"
	"release-desk/internal/tasks"
	"release-desk/internal/versioning"
)

var Kinds = []string{"job", "config", "sql", "topic", "verify"}

var KindLabels = map[string]string{"job": "定时任务", "config": "配置文件", "sql": "SQL 变更", "topic": "Topic", "verify": "变更与验证"}

var Fields = map[string][][2]string{
	"job":    {{"handler", "Handler 名称"}, {"frequency", "频率 / Cron"}, {"description", "描述"}},
	"config": {{"filename", "文件名称"}, {"prefix", "配置前缀"}, {"content", "配置内容"}, {"required", "是否必须配置"}, {"description", "描述"}},
	"sql":    {{"name", "脚本名称"}, {"phase", "SQL 类型（自动识别）"}, {"side", "所属端"}, {"database", "数据库说明"}, {"order", "同类型内顺序"}, {"sql", "SQL 内容"}, {"rollback", "回滚 SQL / 说明"}, {"description", "描述"}},
	"topic":  {{"topic", "Topic 名称"}, {"partitions", "分区数"}, {"group", "消费组"}, {"description", "变更描述"}},
	"verify": {{"title", "修改事项"}, {"change", "修改内容"}, {"verification_mode", "验证方式"}, {"manual_reason", "需手动验证原因"}, {"steps", "参考验证步骤"}, {"keywords", "参考日志线索"}, {"expected", "参考预期结果"}, {"result", "实际验证结果"}},
}

type Item struct {
	ID          string            `json:"id"`
	Kind        string            `json:"kind"`
	Action      string            `json:"action"`
	Service     string            `json:"service"`
	Status      string            `json:"status"`
	Data        map[string]string `json:"data"`
	Online      string            `json:"online"`
	OnlineAt    string            `json:"online_at"`
	VerifyState string            `json:"verify_state"`
	VerifyRuns  []VerifyResult    `json:"verify_runs,omitempty"`
	Updated     string            `json:"updated"`
}

type Version struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Date   string `json:"date"`
	Notes  string `json:"notes"`
	// Archived 仅控制工作台导航，事项、发布进度和执行历史独立保留。
	Archived bool `json:"archived,omitempty"`
}

type VersionFile struct {
	Format  string  `json:"format"`
	Schema  int     `json:"schema"`
	Version Version `json:"version"`
	Items   []Item  `json:"items"`
}

type Execution struct {
	VerificationOnly bool   `json:"verification_only,omitempty"`
	Environment      string `json:"environment,omitempty"`
	SchemaOnly       bool   `json:"schema_only,omitempty"`
	ID               string `json:"id"`
	Version          string `json:"version"`
	ItemID           string `json:"item_id"`
	Digest           string `json:"digest"`
	Server           string `json:"server"`
	Database         string `json:"database"`
	State            string `json:"state"`
	Started          string `json:"started"`
	Finished         string `json:"finished"`
	Detail           string `json:"detail"`
}

type Batch struct {
	Environment string      `json:"environment,omitempty"`
	ID          string      `json:"id"`
	Version     string      `json:"version"`
	ItemID      string      `json:"item_id"`
	Rows        []Execution `json:"rows"`
	Running     bool        `json:"running"`
}

// Store 维护跨业务共享的记录和运行占用，领域服务通过同一个 Mu 协调状态。
// Mu 只保护短暂查询、原子落盘及状态转换；数据库与 CLI 慢调用必须在锁外执行。
type Store struct {
	Mu                 sync.Mutex
	ConfigMu           sync.RWMutex
	Settings           Settings
	Dir                string
	Versions           map[string]VersionFile
	Hashes             map[string]string
	History            []Execution
	Batch              *Batch
	Notice             string
	Revision           int64
	AIBusy             bool
	AIProgress         string
	Scan               *VersionScanState
	ScanCancel         context.CancelFunc
	Theme              string
	TaskContext        context.Context
	CancelTasks        context.CancelFunc
	Closing            bool
	VerifyQueue        *VerifyQueue
	SQLQueue           *SQLQueue
	VerifyQueueCancel  context.CancelFunc
	SQLQueueCancel     context.CancelFunc
	TaskHistory        *tasks.Repository
	SingleVerify       *VerificationTask
	SingleVerifyCancel context.CancelFunc
	SingleSQLCancel    context.CancelFunc
}

// Stamp 生成统一的本机操作时间文本。
// 无业务入参；使用接收对象或本机启动上下文，不隐式触发外部写操作。
// 返回：返回格式为 YYYY-MM-DD HH:mm:ss 的本机操作时间，不表示生产发布时刻。
func Stamp() string {
	// 统一记录本机操作时刻。

	return time.Now().Format("2006-01-02 15:04:05")
}

var ValidName = regexp.MustCompile(`^[\pL\pN][\pL\pN._() \-]{0,99}$`)

// OpenStore 初始化版本存储并恢复未确认执行状态。
// 参数 dir：当前用户的数据根目录；目录中只管理本应用记录和配置。
// 返回：返回初始化完成的 Store；配置或审计无法恢复时返回错误，版本扫描错误作为界面提示。
func OpenStore(dir string) (*Store, error) {
	// 先加载本机配置，避免扫描错误目录。
	// 版本记录和 SQL 执行审计分开保存。
	// 执行历史损坏时保留文件并报错。
	// 不能把无法解析的历史当成从未执行。
	// 上次仍为执行中的库结果视为不确定。
	// 不确定状态不进入默认失败重试集合。
	// 恢复后的状态需要先持久化。

	config, err := LoadSettings(dir)

	if err != nil {
		return nil, err
	}
	s := &Store{Dir: dir, Settings: config, Versions: map[string]VersionFile{}, Hashes: map[string]string{}, History: []Execution{}}
	// 任务历史独立恢复，异常退出不能让旧任务继续显示为运行中。
	s.TaskHistory, err = tasks.Open(filepath.Join(dir, "任务历史"))
	if err != nil {
		return nil, err
	}
	// 窗口真正退出时统一取消后台任务；收进系统托盘不取消。
	s.TaskContext, s.CancelTasks = context.WithCancel(context.Background())
	// 随机端口会改变浏览器源，主题偏好由独立本机文件恢复。
	s.Theme = LoadTheme(dir)

	if err := os.MkdirAll(filepath.Join(dir, "版本文件"), 0700); err != nil {
		return nil, err
	}

	if b, err := os.ReadFile(filepath.Join(dir, "execution-history.json")); err == nil {
		if err = json.Unmarshal(b, &s.History); err != nil {
			return nil, fmt.Errorf("执行历史损坏，已保留文件：%w", err)
		}
	}
	// 软件异常退出后的运行中记录保留为“已中断”，不能自动视为失败并重试。
	for i := range s.History {
		if s.History[i].State == "执行中" {
			s.History[i].State = "已中断"
			s.History[i].Detail = "上次退出时结果未确认，请先核对数据库"
		}
	}

	if err := s.PersistHistory(); err != nil {
		return nil, err
	}

	s.ScanFiles()
	return s, nil
}

// PersistHistory 原子保存完整逐库执行审计。
// 无业务入参；使用接收对象或本机启动上下文，不隐式触发外部写操作。
// 返回：成功返回 nil；执行审计原子写入失败时返回错误，不将内存结果当作已可靠落盘。
func (s *Store) PersistHistory() error {
	// 执行审计使用单独文件，版本编辑不会覆盖它。

	return fileio.WriteJSON(filepath.Join(s.Dir, "execution-history.json"), s.History)
}

// Save 校验、备份并保存完整版本。
// 参数 v：待保存的完整版本快照；名称需满足安全文件名约束，事项 ID 在本版本内唯一。
// 返回：成功返回 nil；校验、旧文件备份或新文件落盘失败时返回错误，内存快照不替换。
func (s *Store) Save(v VersionFile) error {
	// 所有保存都经过统一字段校验。
	// 文件名直接由已经校验的版本名称生成。
	// 旧文件先归档，保证误改可追溯。
	// 备份失败时不能继续覆盖。
	// 新文件写入成功后更新内存和摘要。

	if err := ValidateVersion(&v); err != nil {
		return err
	}

	path := filepath.Join(s.Dir, "版本文件", v.Version.Name+".json")
	// 每次覆盖前保留一份旧文件，按时间戳归档，便于 AI 写入或误操作后恢复。
	if old, err := os.ReadFile(path); err == nil {
		name := v.Version.Name + "-" + time.Now().Format("20060102-150405.000000000") + ".json"
		if err := fileio.AtomicWrite(filepath.Join(s.Dir, "自动备份", name), old); err != nil {
			return err
		}
	}

	if err := fileio.WriteJSON(path, v); err != nil {
		return err
	}
	b, _ := os.ReadFile(path)

	s.Hashes[path] = identity.Digest(string(b))
	s.Versions[v.Version.Name] = v

	s.Revision++
	return nil
}

// VerificationHash 计算影响单项验收的业务条件摘要。
// 参数 r：本轮变更与验证事项，包含标题、来源、变更类型及所有参考条件。
// 返回：返回影响验收的条件摘要，忽略结果正文和历史回填字段。
func VerificationHash(r Item) string {
	// AI 自主判断也会读取标题与代码来源，这些变化必须使原结论失效。
	// 新增、修改、删除的目的不同，变更类型也要纳入同一输入快照。
	input := struct {
		Kind, Action, Service string
		Data                  map[string]string
	}{r.Kind, r.Action, r.Service, VerificationData(r)}
	// 字符串和字符串字典可确定序列化；JSON 同时避免字段分隔符发生歧义。
	payload, _ := json.Marshal(input)
	return identity.Digest(string(payload))
}

// Find 按版本与 ID 定位唯一事项。
// 参数 version：所属版本的精确名称，不根据记录名称跨版本推断。
// 参数 id：该版本内事项的稳定唯一 ID。
// 返回：依次返回所属版本、匹配事项、事项索引与错误；缺少版本或 ID 时返回错误。
func (s *Store) Find(version, id string) (VersionFile, Item, int, error) {
	// 同一 ID 只在指定版本中查找。
	// 不能跨版本按同名记录猜测。
	// 返回索引供调用方修改独立版本副本。

	v, ok := s.Versions[version]
	if !ok {
		return v, Item{}, 0, errors.New("版本不存在")
	}

	for i, r := range v.Items {
		if r.ID == id {
			return v, r, i, nil
		}
	}
	return v, Item{}, 0, errors.New("记录不存在")
}

// Clone 创建可序列化业务对象的独立快照。
// 参数 value：参与操作的业务值；要求与当前有限集合或快照类型一致。
// 返回：返回无共享可变 map 或 slice 的独立快照；仅用于可 JSON 序列化的内部业务类型。
func Clone[T any](value T) T {
	// 本工具的快照类型都由可序列化数据组成。
	// 通过副本隔离后台任务与 HTTP 编码的可变 map。

	b, _ := json.Marshal(value)

	var copy T
	json.Unmarshal(b, &copy)
	return copy
}

// SortedVersions 返回按完整数字版本倒序的列表。
// 参数 all：内存中的完整版本映射；本方法只返回排序副本。
// 返回：返回完整数字版本倒序的列表，不修改输入映射。
func SortedVersions(all map[string]VersionFile) []VersionFile {
	// 只对版本集合的副本排序。
	// 不修改存储映射或版本名称。
	// 数字比较确保发布版本倒序稳定。

	list := []VersionFile{}
	for _, v := range all {
		list = append(list, v)
	}

	sort.Slice(list, func(i, j int) bool { return versioning.CompareVersion(list[i].Version.Name, list[j].Version.Name) > 0 })
	return list
}
