package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"release-desk/internal/platform/process"
	"release-desk/internal/policy"
	"release-desk/internal/versioning"
)

type Git struct {
	mu       sync.Mutex
	configMu sync.RWMutex
	path     string
	ref      string
	author   string
}

// New 建立仅依赖指定目录与作者的 Git 读取器。
// 参数 path：本机绝对仓库目录；参数 author：需要筛选的精确作者配置。
// 返回：初始固定读取 HEAD 的读取器；创建对象不执行 Git 命令。
func New(path, author string) *Git {
	// 构造时不 fetch 或切分支，读取引用只在用户同步时更新。
	return &Git{path: path, author: author, ref: "HEAD"}
}

// Configure 同时更新仓库和作者并重置读取引用。
// 参数 path：已经过设置校验的绝对目录；参数 author：新的非空作者。
// 不返回业务值；上层须先确认没有正在执行的业务任务。
func (g *Git) Configure(path, author string) {
	// 锁顺序与版本、脚本读取一致，避免出现路径和作者来自两份设置。
	g.mu.Lock()
	defer g.mu.Unlock()
	// 命令执行期间保留配置读锁，本轮命令不会中途换目录。
	g.configMu.Lock()
	defer g.configMu.Unlock()
	// 原仓库的上游引用不能带入新仓库，重新从本地 HEAD 开始。
	g.path, g.author, g.ref = path, author, "HEAD"
}

// Directory 返回当前仓库目录的受锁快照。
// 无业务入参；只返回配置路径，不读取目录内文件。
// 返回：供来源提示显示的绝对目录。
func (g *Git) Directory() string {
	// 目录、作者和引用只在本包维护，外部服务不能直接操纵锁或字段。
	g.configMu.RLock()
	defer g.configMu.RUnlock()
	// 返回字符串值而不是配置对象引用，调用方不能改变当前仓库。
	return g.path
}

type GitVersion struct {
	Name   string `json:"name"`
	Folder string `json:"folder"`
	Count  int    `json:"count"`
}

type GitScript struct {
	Path    string `json:"path"`
	Commit  string `json:"commit"`
	Author  string `json:"author"`
	Date    string `json:"date"`
	Side    string `json:"side"`
	Message string `json:"message"`
}

// Run 在指定仓库内执行有界 Git 命令。
// 参数 args：传给辅助程序的独立参数；不拼接成 shell 命令。
// 返回：返回 Git 标准合并输出文本；执行失败返回有界错误摘要。
func (g *Git) Run(args ...string) (string, error) {
	// 每条 Git 命令限定在用户配置仓库。
	// 独立配置读锁避免读取一半更新的路径。
	// 调用具备超时，远端不可用不会无限等待。
	// 使用 core.quotepath=false 保留中文路径。
	// 合并输出只用于错误提示，不解析为操作指令。
	// 错误消息截断，避免异常输出占满界面。

	g.configMu.RLock()
	defer g.configMu.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), policy.GitTimeoutSeconds*time.Second)

	defer cancel()
	cmd := process.Command(ctx, "git", append([]string{"-C", g.path, "-c", "core.quotepath=false"}, args...)...)

	b, err := cmd.CombinedOutput()

	if err != nil {
		msg := strings.TrimSpace(string(b))
		if len(msg) > 800 {
			msg = msg[:800]
		}
		return "", fmt.Errorf("Git 操作失败：%s", msg)
	}
	return strings.TrimPrefix(string(b), "\ufeff"), nil
}

// Branch 读取仓库当前分支名。
// 无业务入参；使用接收对象或本机启动上下文，不隐式触发外部写操作。
// 返回：返回去除首尾空白的当前分支名与读取错误。
func (g *Git) Branch() (string, error) {
	// 分支名称来自 Git 自身，不从目录名猜测。

	s, e := g.Run("branch", "--show-current")
	return strings.TrimSpace(s), e
}

// Sync 获取当前上游分支而不覆盖工作区。
// 无业务入参；使用接收对象或本机启动上下文，不隐式触发外部写操作。
// 返回：成功返回 nil；缺少上游或 fetch 失败时返回错误，本地工作区保持不变。
func (g *Git) Sync() error {
	// 同步范围是当前分支的上游。
	// 没有上游时不猜测 master 或其他发布分支。
	// 只获取远端对象，不执行 pull 或 checkout。
	// 本地工作区文件不会被覆盖。
	// 本地领先时继续读取 HEAD。
	// 远端领先或分叉时读取刚获取的上游引用。

	g.mu.Lock()
	defer g.mu.Unlock()

	tracking, err := g.Run("rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	if err != nil {
		return errors.New("当前分支没有上游分支，请在 Git 中设置后再同步")
	}

	parts := strings.SplitN(strings.TrimSpace(tracking), "/", 2)
	if len(parts) != 2 {
		return errors.New("上游分支信息无效")
	}

	if _, err = g.Run("fetch", "--no-tags", parts[0], parts[1]); err != nil {
		return err
	}
	g.ref = strings.TrimSpace(tracking)

	if _, err = g.Run("merge-base", "--is-ancestor", g.ref, "HEAD"); err == nil {
		g.ref = "HEAD"
	}
	return nil
}

// Versions 从 Git 对象树汇总版本候选。
// 无业务入参；使用接收对象或本机启动上下文，不隐式触发外部写操作。
// 返回：返回数字版本倒序的目录候选与 SQL 文件数；仓库读取失败时返回错误。
func (g *Git) Versions() ([]GitVersion, error) {
	// 目录来自 Git 对象树，不依赖工作区文件状态。
	// 只把 SQL 文件纳入版本来源统计。
	// 以顶层发布或补丁目录提取完整版本号。
	// 同一版本的不同目录累计文件数。
	// 无法识别的普通目录不强行归为某版本。
	// 保留来源目录用于用户判断。
	// 不默认把所有版本加入工作台。
	// 排序依据完整数字版本。
	// 本操作不会 fetch，网络同步另由用户按钮触发。
	// 查不到 Git 仓库时返回错误。
	// 版本清单只作候选，不触发发布。

	g.mu.Lock()

	defer g.mu.Unlock()

	text, err := g.Run("ls-tree", "-r", "--name-only", g.ref)

	if err != nil {
		return nil, err
	}

	found := map[string]GitVersion{}

	re := regexp.MustCompile(`(?i)(?:release|hotfix)[-_]?(\d+(?:\.\d+)+)`)

	for _, p := range strings.Split(text, "\n") {
		if !strings.HasSuffix(strings.ToLower(p), ".sql") {
			continue
		}
		root := strings.Split(p, "/")[0]
		m := re.FindStringSubmatch(root)
		if m == nil {
			continue
		}
		v := found[m[1]]
		v.Name = m[1]
		v.Folder = root
		v.Count++
		found[m[1]] = v
	}

	list := []GitVersion{}

	for _, v := range found {
		list = append(list, v)
	}

	sort.Slice(list, func(i, j int) bool { return versioning.CompareVersion(list[i].Name, list[j].Name) > 0 })
	return list, nil
}

// Scripts 列出指定作者最新涉及的 SQL 文件。
// 参数 version：当前操作的版本标识或完整版本快照，用于限定事项归属。
// 返回：返回每个 SQL 路径该作者最近一次提交的信息；完整版本不匹配的路径被排除。
func (g *Git) Scripts(version string) ([]GitScript, error) {
	// 日志范围限定在当前选定引用。
	// 排除合并提交，避免重复解释合并路径。
	// 作者筛选来自用户配置。
	// 提交分隔符与文件路径分开解析。
	// 每个路径只保留该作者最近一次涉及记录。
	// 保留完整提交 SHA，后续读取不受工作区变化影响。
	// 文件只限 SQL 扩展名。
	// 版本过滤只对顶层版本目录生效。
	// 完整数字版本匹配，避免 5.7.2 命中 5.7.20。
	// 所属端由仓库目录判断。
	// 未带服务商或平台标识的文件使用租户端约定。
	// 返回的是候选元信息，还需要用户选择导入。

	g.mu.Lock()

	defer g.mu.Unlock()

	text, err := g.Run("log", g.ref, "--no-merges", "--format=@@%H|%an|%ad|%s", "--date=short", "--name-only", "--author="+g.author, "--", "*.sql")

	if err != nil {
		return nil, err
	}

	seen := map[string]bool{}

	list := []GitScript{}

	var commit []string

	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "@@") {
			commit = strings.SplitN(line[2:], "|", 4)
			continue
		}
		if len(commit) != 4 || line == "" || seen[line] || !strings.HasSuffix(strings.ToLower(line), ".sql") {
			continue
		}
		seen[line] = true
		if version != "" {
			parts := regexp.MustCompile(`\d+(?:\.\d+)+`).FindAllString(strings.Split(line, "/")[0], -1)
			if !slices.Contains(parts, version) {
				continue
			}
		}
		side := "租户端"
		if strings.Contains(line, "服务商") {
			side = "服务商端"
		} else if strings.Contains(line, "平台") {
			side = "平台端"
		}
		list = append(list, GitScript{Path: line, Commit: commit[0], Author: commit[1], Date: commit[2], Message: commit[3], Side: side})
	}

	return list, nil
}

// Content 读取已验证 Git 候选的提交内容。
// 参数 record：用户选中的 Git SQL 候选，读取前再次验证提交与路径。
// 返回：返回经后端候选清单验证的提交时 SQL 原文；伪造路径或读取失败返回错误。
func (g *Git) Content(record GitScript) (string, error) {
	// 候选必须出现在当前作者提交清单中。
	// 前端不能通过伪造路径读取任意 Git 对象。

	// 客户端不能提交任意 Git 对象：需与当前分支作者提交列表严格匹配。
	records, err := g.Scripts("")

	if err != nil {
		return "", err
	}
	for _, r := range records {
		if r.Path == record.Path && r.Commit == record.Commit {
			return g.Run("show", r.Commit+":"+r.Path)
		}
	}
	return "", errors.New("所选 SQL 不在当前作者提交清单中")
}
