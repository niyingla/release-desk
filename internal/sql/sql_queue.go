package sqlservice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"time"

	"release-desk/internal/identity"
	sqlscript "release-desk/internal/sql/script"
	"release-desk/internal/storage/workspace"
)

var ErrNoSQLTargets = errors.New("没有待执行目标库；相同 SQL 的成功库会自动跳过，已中断记录请先核对")

type SQLPlannedItem struct {
	Name  string `json:"name"`
	Phase string `json:"phase"`
	Plan  Plan   `json:"plan"`
}

type SQLQueuePreview struct {
	Environment string           `json:"environment"`
	Token       string           `json:"token"`
	Version     string           `json:"version"`
	Items       []SQLPlannedItem `json:"items"`
	Created     time.Time        `json:"-"`
}

// BuildSQLQueuePreview 按正文类型组织全部勾选脚本并生成独立目标预览。
// 参数 version：已经确认归属的版本名称。
// 参数 items：只包含该版本 SQL 的稳定快照，允许顺序与目录名相反。
// 参数 plan：只读单脚本预览器，不执行任何 SQL。
// 返回：完整有序预览；任一有效脚本校验失败时拒绝整批，不先执行部分脚本。
func BuildSQLQueuePreview(version string, items []workspace.Item, plan func(string) (Plan, error)) (SQLQueuePreview, error) {
	// 排序由 SQL 正文决定，用户勾选顺序和归档目录都不能改变建表依赖。
	sort.SliceStable(items, func(i, j int) bool { return SqlItemLess(items[i], items[j]) })
	preview := SQLQueuePreview{Token: identity.NewID(), Version: version, Items: []SQLPlannedItem{}, Created: time.Now()}
	for _, item := range items {
		// 每个脚本单独识别所属端与目标库，不复用上一条脚本的租户集合。
		p, err := plan(item.ID)
		// 已全部成功或需人工核对的脚本保留跳过记录，不阻止其他待执行脚本预览。
		if err != nil && !errors.Is(err, ErrNoSQLTargets) {
			return preview, fmt.Errorf("%s：%w", item.Data["name"], err)
		}
		// 一个确认批次只能属于一个环境，不能混入其他环境的预览令牌。
		environment := workspace.EffectiveSQLEnvironment(p.Environment)
		if preview.Environment != "" && preview.Environment != environment {
			return preview, errors.New("批量预览包含不同执行环境")
		}
		preview.Environment = environment
		// 环境在整批生成时固定，不受之后前端切换下拉框影响。
		preview.Items = append(preview.Items, SQLPlannedItem{Name: item.Data["name"], Phase: sqlscript.InferSQLPhase(item.Data), Plan: p})
		// 显示类型再次解析正文，界面上的旧标签不能改变实际执行排序。
	}
	return preview, nil
}

// SqlItemLess 比较两条脚本的阶段和同阶段顺序。
// 参数 left：左侧已校验的 SQL 事项，仅解析正文和数字顺序字段。
// 参数 right：右侧已校验的 SQL 事项，不使用来源目录或提交路径排序。
// 返回：左侧应先执行时为 true；同阶段同顺序保留原始稳定顺序。
func SqlItemLess(left, right workspace.Item) bool {
	// 阶段索引来自统一解析器，混合脚本仍不打乱脚本内部语句。
	ranks := map[string]int{}
	for i, phase := range sqlscript.SQLPhases {
		ranks[phase] = i
	}
	l, r := ranks[sqlscript.InferSQLPhase(left.Data)], ranks[sqlscript.InferSQLPhase(right.Data)]
	// 同一套阶段优先级供单条表格与批量执行共同使用，避免两处顺序不一致。
	if l != r {
		return l < r
	}
	// 同阶段才比较人工维护的顺序号，不让数据脚本越过建表脚本。
	lo, _ := strconv.Atoi(left.Data["order"])
	// order 已在版本协议中校验为整数，相同数字依赖稳定排序保留原顺序。
	ro, _ := strconv.Atoi(right.Data["order"])
	return lo < ro
}

// ValidateSQLPlan 重新校验单条预览的原文、连接、所属端和目标集合。
// 参数 plan：由后端一次性预览保存的完整计划，不信任前端临时目标。
// 参数 config：执行启动时的固定环境配置，不包含在返回值中。
// 返回：已解析语句或校验错误；调用方须持有 Store.Mu。
func (s *Service) ValidateSQLPlan(plan Plan, config DBConfig) ([]string, error) {
	// 任何一项漂移都要求重新预览，不能在用户确认后静默扩大执行范围。
	_, validEnvironment := workspace.LookupSQLEnvironment(config.Environment)
	if !validEnvironment || config.Environment == "" || config.Environment != workspace.EffectiveSQLEnvironment(plan.Environment) || ServerID(config) != plan.Server {
		return nil, errors.New("执行环境或连接设置发生变化，请重新预览")
	}
	// 私有摘要不离开后端；账号或密码修改后旧令牌也失效，不能静默使用新连接。
	if plan.connectionDigest != "" && plan.connectionDigest != connectionDigest(config) {
		return nil, errors.New("数据库连接已更新，请重新预览")
	}
	_, item, _, err := s.Find(plan.Version, plan.ItemID)
	// 同时比较源内容和预览内容的摘要，不能只信任预览中声称的 digest。
	if err != nil || item.Kind != "sql" || identity.Digest(item.Data["sql"]) != plan.Digest || identity.Digest(plan.SQL) != plan.Digest || item.Data["side"] != plan.Side {
		return nil, errors.New("SQL 内容或所属端发生变化，请重新预览")
	}
	// 私有预览仍逐一检查数据库匹配，目标重复也不能让同一库执行两次。
	if len(MatchDBRules(plan.Databases, plan.Side, config.ScopeRules)) != len(plan.Databases) {
		return nil, errors.New("数据库匹配规则已变化，请重新预览")
	}
	// 两个不同脚本可访问同一库，但同一脚本内不能重复访问同一目标。
	seen := map[string]bool{}
	for _, name := range plan.Databases {
		if seen[name] {
			return nil, errors.New("执行目标库重复")
		}
		seen[name] = true
	}
	// 仅复核是既有目标的子集，不能借此添加另一个未确认数据库。
	checkOnly := map[string]bool{}
	// 同一目标只能属于一次复核，防止重复条目让审计含义不明确。
	for _, name := range plan.SchemaCheckOnly {
		if !seen[name] || checkOnly[name] {
			return nil, errors.New("结构复核目标无效，请重新预览")
		}
		checkOnly[name] = true
	}
	// 只读模式的每个目标必须进入复核集合，不能混入仍会执行 SQL 的目标。
	if plan.VerifyOnly && len(checkOnly) != len(seen) {
		return nil, errors.New("只读核验目标不完整，请重新预览")
	}
	// 没有目标的计划仍检查语法，避免跳过项掩盖失效来源或非法 SQL。
	// 返回解析后的原始语句顺序，不按语句类型重写混合脚本。
	return sqlscript.SplitSQL(plan.SQL)
}

// StartSQLQueue 先核对整批计划，再启动有序脚本队列。
// 参数 preview：用户已经逐条查看并勾选确认的一次性完整预览。
// 参数 config：执行前读取的环境连接快照，变化时不开始任何脚本。
// 参数 run：逐库执行器，可在测试中注入模拟行为，不向客户端暴露。
// 返回：后台接收成功返回 nil；任一脚本或忙碌校验失败时整批不执行。
func (s *Service) StartSQLQueue(preview SQLQueuePreview, config DBConfig, run func(context.Context, DBConfig, string, []string) (string, error)) error {
	// 所有原文先解析与核验，第二条无效时也不能让第一条先改库。
	s.Mu.Lock()
	defer s.Mu.Unlock()
	// 与单条执行共用占用检查，连续点击或其他窗口不能插入第二个执行入口。
	if s.Closing || s.SQLActive() || s.AIBusy {
		return errors.New("请等待当前任务完成后再批量执行")
	}
	// 队列环境与每条脚本都需复核，即使同一主机有两份连接也不能混用。
	if workspace.EffectiveSQLEnvironment(preview.Environment) != config.Environment {
		return errors.New("批量执行环境发生变化，请重新预览")
	}
	statements := [][]string{}
	// 所有语句先放入候选列表，尚未创建任何数据库连接或执行审计。
	targets := 0
	for _, item := range preview.Items {
		// 版本归属也由后端复核，不能把两个版本的脚本拼成一个批次。
		if item.Plan.Version != preview.Version {
			return errors.New("预览中包含其他版本")
		}
		parts, err := s.ValidateSQLPlan(item.Plan, config)
		// 任一项漂移整体拒绝，不出现前半批已执行、后半批才报校验错误。
		if err != nil {
			return err
		}
		statements = append(statements, parts)
		// 统计的是脚本与数据库配对次数，不是去重库数，供确认界面核对工作量。
		targets += len(item.Plan.Databases)
	}
	if targets == 0 {
		// 全部跳过时不启动空队列，也不生成虚假的执行成功记录。
		return ErrNoSQLTargets
	}
	// 整个队列持有占用，脚本切换间隙也禁止修改来源和连接。
	ctx, cancel := context.WithCancel(s.TaskContext)
	// 停止按钮只取消本批，退出软件则通过父上下文取消全部工作。
	s.SQLQueueCancel = cancel
	queue := &workspace.SQLQueue{Environment: config.Environment, ID: identity.NewID(), Version: preview.Version, Running: true, Rows: []workspace.SQLQueueRow{}}
	for _, item := range preview.Items {
		// 全部脚本在开始前即可见，不让用户只看到正在处理的一条。
		queue.Rows = append(queue.Rows, workspace.SQLQueueRow{ItemID: item.Plan.ItemID, Name: item.Name, Phase: item.Phase, Side: item.Plan.Side, State: "等待中", Executions: []workspace.Execution{}})
	}
	s.SQLQueue = queue
	// 新队列不能沿用上一次单脚本的逐库列表，否则进度会关联到错误事项。
	s.Batch = nil
	// 整批目标先持久化，失败时不进入任何数据库。
	if err := s.SaveSQLTask(); err != nil {
		queue.Running = false
		cancel()
		return err
	}
	s.Revision++
	// 运行标记先于协程建立，文件监听和前台编辑在脚本切换间隙仍保持阻塞。
	go s.RunSQLQueue(ctx, queue, preview, config, statements, run)
	return nil
}

// RunSQLQueue 顺序运行各脚本，并保留每条脚本的全部逐库结果。
// 参数 ctx：用户停止批次或退出软件时取消的上下文。
// 参数 queue：已经登记的多脚本队列，更新受记录锁保护。
// 参数 preview：不可变的后端预览计划，不接收新的库名或 SQL。
// 参数 config：固定环境连接快照，仅用于后端执行。
// 参数 statements：逐项预解析的 SQL，位置与 preview.Items 一致。
// 参数 run：逐库执行函数，业务 SQL 失败不会阻止下一库或下一脚本。
// 不返回业务值；真实状态写入独立执行历史，审计异常时停止后续脚本。
func (s *Service) RunSQLQueue(ctx context.Context, queue *workspace.SQLQueue, preview SQLQueuePreview, config DBConfig, statements [][]string, run func(context.Context, DBConfig, string, []string) (string, error)) {
	// 每条脚本落完整结果后再进入下一条，历史页面不依赖当前 Batch 引用。
	// 统一收尾释放占用，已经完成的脚本结果不会因后续失败被覆盖。
	defer s.CompleteSQLQueue(queue)
	slog.Info("多脚本执行开始", "environment", queue.Environment, "version", queue.Version, "count", len(preview.Items))
	for index, item := range preview.Items {
		// 取消在进入下一条脚本前生效，不会因某条刚结束而遗漏停止请求。
		if ctx.Err() != nil {
			return
		}
		s.Mu.Lock()
		// 跳过计划没有任何写入，因此不生成“已执行”的数据库审计。
		if len(item.Plan.Databases) == 0 {
			// 跳过信息只进入队列摘要，不伪造本来不存在的数据库执行记录。
			queue.Rows[index].State = "已跳过"
			queue.Rows[index].Detail = "没有待执行库，已成功或结果未确认的目标保持跳过"
			s.TrackTask("sql")
			s.Revision++
			s.Mu.Unlock()
			continue
		}
		batch := NewBatch(item.Plan)
		// 当前脚本仍复用单条执行的逐库数据结构，界面和持久化含义一致。
		s.Batch = batch
		queue.Rows[index].State = "执行中"
		s.TrackTask("sql")
		// 发布修订后即释放锁，慢查询和数据库连接不会冻结界面轮询。
		s.Revision++
		s.Mu.Unlock()
		// 参数只来自后端已保存预览，队列启动后不再从前端取 SQL 正文。
		// 单脚本业务错误在逐库层消化；这里只接收取消或无法可靠写审计的错误。
		err := s.RunBatchWithContext(ctx, batch, config, statements[index], func(cfg DBConfig, name string, parts []string) (string, error) { return run(ctx, cfg, name, parts) })
		s.Mu.Lock()
		// 留下独立快照，后续脚本替换 s.Batch 时不会覆盖此前逐库明细。
		queue.Rows[index].Executions = workspace.Clone(batch.Rows)
		queue.Rows[index].State = "成功"
		// 队列摘要汇总真实逐库状态，不把部分库成功当成整条脚本全部成功。
		for _, row := range batch.Rows {
			// 已中断和未执行也表示本条没有全部成功，必须留在摘要中。
			if row.State != "成功" {
				queue.Rows[index].State = "有失败"
			}
		}
		if err != nil {
			// 基础设施异常或取消单独标记停止，与普通 SQL 业务失败区分。
			queue.Rows[index].State = "已停止"
			queue.Rows[index].Detail = err.Error()
		}
		s.Revision++
		// 先让用户看到本条终态，再决定是否进入下一脚本。
		s.TrackTask("sql")
		s.Mu.Unlock()
		// 审计落盘异常必须停止，不能把不可追溯写入扩散到后续脚本。
		if err != nil {
			return
		}
	}
}

// CompleteSQLQueue 标记未开始脚本并释放整批执行占用。
// 参数 queue：本轮队列对象，已经完成的逐库明细原样保留。
// 不返回业务值；默认执行和单库失败的继续语义不改变。
func (s *Service) CompleteSQLQueue(queue *workspace.SQLQueue) {
	// 停止后的剩余脚本明确是未执行，不能被用户误认为已经处理。
	s.Mu.Lock()
	defer s.Mu.Unlock()
	// 未开始项与已经失败项含义不同，停止不能把它们都填成失败。
	for i := range queue.Rows {
		if queue.Rows[i].State == "等待中" {
			queue.Rows[i].State = "未执行"
		}
	}
	queue.Running = false
	s.TrackTask("sql")
	// 运行标记最后释放，保证整批收尾完成前来源文件不会被监听器替换。
	if s.SQLQueueCancel != nil {
		// 清除旧取消器，后续按钮不能意外操作上一批次的上下文。
		s.SQLQueueCancel()
		s.SQLQueueCancel = nil
	}
	s.Notice = "批量 SQL 已结束，可查看各脚本逐库结果"
	// 工作台保留简短摘要，完整明细继续由队列和独立执行历史承担。
	s.Revision++
	slog.Info("多脚本执行结束", "environment", queue.Environment, "version", queue.Version, "count", len(queue.Rows))
	// 历史独立落盘，关闭结果面板或重新选择事项不会丢失逐库记录。
}
