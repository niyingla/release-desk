package app

import (
	"fmt"
	"sort"

	aiservice "release-desk/internal/ai"
	"release-desk/internal/identity"
	"release-desk/internal/policy"
	sqlservice "release-desk/internal/sql"
	"release-desk/internal/storage/workspace"
	"release-desk/internal/versioninfo"
	"release-desk/internal/versioning"
)

type endpoint func([]byte) (any, error)

// handlers 注册按职责划分的业务接口。
// 无业务入参；使用接收对象或本机启动上下文，不隐式触发外部写操作。
// 返回：返回操作名到处理函数的映射，由统一 HTTP 入口调用。
func (a *App) handlers() map[string]endpoint {
	// 任务中心只负责查询与停止，回看历史不能重新触发外部调用。
	// 端点按业务职责分组，只负责派发到各自处理器。
	// HTTP 鉴权和请求大小校验在统一入口执行。
	// SQL 来源刷新与数据库执行分开，更新文件内容不应隐式触发改库。
	// SQL 预览与实际执行使用不同端点，不能省略确认步骤。
	// 外观偏好与业务任务独立，不触发版本或数据库更新。
	// 批次预览、启动和停止各有独立端点，勾选或全选本身不能触发外部调用。

	return map[string]endpoint{
		// 更新接口独立于业务任务，只查询公开发布信息或打开已校验的 GitHub 链接。
		"update/check":    a.services().Updates.CheckEndpoint,
		"update/open":     a.services().Updates.OpenEndpoint,
		"version/archive": a.services().Release.ArchiveVersion,
		"state":           a.state, "version/save": a.services().Release.SaveVersion, "version/delete": a.services().Release.DeleteVersion, "item/save": a.services().Release.SaveItem, "item/delete": a.services().Release.DeleteItem, "item/online": a.services().Release.ToggleOnline,
		"git/versions": a.services().Git.GitVersions, "git/sync": a.services().Git.GitSync, "git/scripts": a.services().Git.GitScripts, "git/import": a.services().Git.GitImport,
		"git/refresh": a.services().Git.GitRefresh,
		"db/settings": a.services().SQL.DBSettings, "db/save": a.services().SQL.DBSave, "db/plan": a.services().SQL.DBPlan, "db/execute": a.services().SQL.DBExecute,
		"ai/template": a.services().AI.AITemplate, "ai/verify": a.services().AI.AIVerify, "ai/prompt": a.services().AI.AIPrompt, "folder": a.services().Files.OpenFolder, "backup": a.services().Files.Backup, "restore": a.services().Files.Restore, "export": a.services().Files.Export,
		"ai/scan":         a.services().AI.AIScan,
		"ai/scan/cancel":  a.services().AI.CancelVersionScan,
		"ai/verify-batch": a.services().AI.AIVerifyBatch, "ai/verify-batch/cancel": a.services().AI.CancelVerifyBatch,
		"db/batch/plan": a.services().SQL.DBBatchPlan, "db/batch/execute": a.services().SQL.DBBatchExecute, "db/batch/cancel": a.services().SQL.CancelSQLBatch,
		"theme/save":   a.services().Settings.SaveTheme,
		"settings/get": a.services().Settings.GetSettings, "settings/save": a.services().Settings.SaveSettings,
		"clipboard/copy": a.services().Files.CopyClipboard,
		"task/detail":    a.services().Tasks.Detail, "task/cancel": a.services().Tasks.Cancel,
	}
}

// state 生成界面所需的一致状态快照。
// 无业务入参；使用接收对象或本机启动上下文，不隐式触发外部写操作。
// 返回：返回版本、逐库历史、运行批次与界面元数据的独立快照，不含数据库密码。
func (a *App) state(_ []byte) (any, error) {
	// 历史目录与详情分开，周期轮询只携带轻量状态摘要。
	// 界面状态来自同一时刻的快照。
	// 持有记录锁期间不执行数据库或 Codex 查询。
	// 版本按数字段排序，避免字符串排序错误。
	// SQL 摘要只用于当前内容与执行历史匹配。
	// 摘要是派生字段，不代表执行成功。

	s := a.Store
	// 文件系统和工具路径探测不占用工作区锁，磁盘繁忙时仍可接收停止与退出。
	connectionID := ""
	connections := map[string]string{}
	for _, environment := range workspace.SQLEnvironments {
		// 摘要仅提供服务器标识，环境未配置时不借用其他连接。
		if cfg, err := (&sqlservice.Service{Store: s}).LoadDB(environment.ID); err == nil {
			connections[environment.ID] = sqlservice.ServerID(cfg)
		}
	}
	connectionID = connections["test"]
	codexAvailable := aiservice.FindCodex() != ""
	// 并发偏好随本机设置恢复，不依赖每次启动都会变化的浏览器端口。
	verifyDefault := s.Config().VerificationConcurrency
	if verifyDefault == 0 {
		verifyDefault = policy.VerificationConcurrency
	}
	s.Mu.Lock()

	defer s.Mu.Unlock()
	// 返回值深拷贝，HTTP 编码期间不会与执行协程、文件扫描协程发生数据竞争。
	versions := workspace.Clone(workspace.SortedVersions(s.Versions))

	for vi := range versions {
		for ri := range versions[vi].Items {
			item := &versions[vi].Items[ri]
			if item.Kind == "sql" {
				item.Data["content_digest"] = identity.Digest(item.Data["sql"])
			}
		}
	}
	// 列表仅返回轻量目录，逐库明细和生产证据通过独立详情接口读取。
	var taskList any = []any{}
	if s.TaskHistory != nil {
		taskList = s.TaskHistory.List()
	}
	// 软件版本独立于用户正在整理的业务发布版本。
	software, err := versioninfo.Read()
	if err != nil {
		return nil, fmt.Errorf("软件版本记录损坏：%w", err)
	}
	// 表单和入库校验共用操作选项，新增关闭语义时不在前端另建白名单。
	actions := map[string][]string{}
	for _, kind := range workspace.Kinds {
		actions[kind] = workspace.ItemActionOptions(kind)
	}

	// 环境摘要独立采样，不参与后台任务的执行范围或源内容核验。
	return workspace.Clone(map[string]any{"software_update": a.services().Updates.Snapshot(), "verification_default_concurrency": verifyDefault, "verification_concurrency": policy.VerificationConcurrency, "single_verify": s.SingleVerify, "tasks": taskList, "software": software, "versions": versions, "history": s.History, "batch": s.Batch, "scan": s.Scan, "verify_queue": s.VerifyQueue, "sql_queue": s.SQLQueue, "revision": s.Revision, "notice": s.Notice, "ai_busy": s.AIBusy, "ai_progress": s.AIProgress, "data_dir": s.Dir, "codex_available": codexAvailable, "fields": workspace.Fields, "action_options": actions, "config_requirements": workspace.ConfigRequirements, "labels": workspace.KindLabels, "connection_id": connectionID, "sql_connections": connections, "sql_environments": workspace.SQLEnvironments}), nil
}

// check 执行只读的环境与工具可用性检查。
// 无业务入参；使用接收对象或本机启动上下文，不隐式触发外部写操作。
// 返回：成功返回 nil 结果与 nil 错误；请求校验、记录定位或后续职责处理失败时返回错误，未返回业务成功凭证。
func (a *App) check() error {
	// 此模式只用于只读连通性检查。
	// 不执行版本文件中的 SQL。
	// 依次按三个业务端识别目标库。
	// 数量使用当前账号真实可见库清单统计。
	// Git 版本来源于当前选定引用。
	// Codex 可用仅表示找到程序，不代表已经验证业务。

	cfg, e := (&sqlservice.Service{Store: a.Store}).LoadDB("test")
	if e != nil {
		return e
	}

	for _, side := range []string{"租户端", "服务商端", "平台端"} {
		names, e := sqlservice.DiscoverDB(cfg, side)
		if e != nil {
			return e
		}
		fmt.Printf("测试环境 · %s：%d 个目标库\n", side, len(names))
	}

	list, e := a.Git.Versions()
	if e != nil {
		return e
	}

	sort.Slice(list, func(i, j int) bool { return versioning.CompareVersion(list[i].Name, list[j].Name) > 0 })

	fmt.Printf("Git 版本：%d；Codex 可用：%t\n", len(list), aiservice.FindCodex() != "")
	return nil
}
