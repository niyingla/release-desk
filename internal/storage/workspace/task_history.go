package workspace

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"release-desk/internal/tasks"
)

type VerificationTask struct {
	ID              string       `json:"id"`
	Version         string       `json:"version"`
	ItemID          string       `json:"item_id"`
	Title           string       `json:"title"`
	Condition       string       `json:"condition"`
	Running         bool         `json:"running"`
	CancelRequested bool         `json:"cancel_requested"`
	Result          VerifyResult `json:"result"`
}

// SaveScanTask 保存当前扫描的阶段与最终摘要。
// 无业务入参；调用方须持有 Mu，避免记录混合状态。
// 返回：写盘错误；没有当前扫描或隔离测试未装配历史仓库时返回 nil。
func (s *Store) SaveScanTask() error {
	// 当前扫描的稳定 ID 连接阶段、停止请求和最终摘要。
	// 扫描失败不能保留合并成功的数量，业务写盘入口已经负责清空统计。
	// 人工停止有独立终态，不能显示成模型故障。
	// 待确认项保留为提示，不把模型警告升级成已验证结论。
	// 摘要只说明本轮扫描，不替用户改变业务版本的发布状态。
	// 固定五类顺序便于对比每轮结果，不依赖 map 的迭代顺序。
	// 扫描候选和凭据不进入任务文件，只保留阶段、统计与已公开的摘要。
	if s.TaskHistory == nil || s.Scan == nil {
		return nil
	}
	scan := s.Scan
	status := "运行中"
	if !scan.Running {
		status = "已完成"
		if scan.Cancelled {
			status = "已停止"
		} else if scan.Error != "" {
			status = "失败"
		}
	}
	// 用户终止优先于阶段轮询，不能把迟到进度显示为重新开始。
	if scan.Running && scan.CancelRequested {
		status = "正在停止"
	}
	rows := []tasks.Row{{ID: scan.ID, Title: "扫描摘要", State: status, Detail: scan.Summary}}
	for _, warning := range scan.Warnings {
		rows = append(rows, tasks.Row{Title: "待确认", State: "待核对", Detail: warning})
	}
	if scan.Error != "" {
		rows = append(rows, tasks.Row{Title: "错误", State: "失败", Detail: scan.Error})
	}
	// 五类数量来自已经完成的扫描状态，不依据模型文字猜测成功数量。
	for _, kind := range Kinds {
		if count := scan.Counts[kind]; count > 0 {
			rows = append(rows, tasks.Row{Title: KindLabels[kind], State: "已合并", Detail: fmt.Sprintf("%d 项", count)})
		}
	}
	return s.TaskHistory.Save(tasks.Record{ID: scan.ID, Kind: "scan", Version: scan.Version, Title: "版本扫描 · " + scan.Author, Running: scan.Running, Status: status, Progress: scan.Phase, Rows: rows})
}

// SaveSQLTask 保存单条或多脚本任务，并覆盖全部目标库。
// 无业务入参；调用方须持有 Mu，多脚本优先于当前单脚本视图。
// 返回：任务历史原子写入错误；逐库正式执行审计仍由独立文件保存。
func (s *Store) SaveSQLTask() error {
	// 多脚本只有一个父任务，当前脚本只是该任务的一部分。
	// 已完成脚本保存自己的副本，后续脚本不能覆盖前面的逐库结果。
	// 运行中脚本从实时 Batch 取全部目标，等待库也必须显示。
	// 队列结束时继续使用父任务 ID，不能退回最后一个单脚本 ID。
	// 单脚本模式与队列模式共用正式审计，但各有独立任务身份。
	// 汇总数量只表示已处理目标，不能表示业务已验收。
	// 本地任务文件不保存连接账号、密码或完整 SQL 内容。
	// 队列持有父任务身份，脚本切换不能在任务中心制造多份父任务。
	if s.TaskHistory == nil {
		return nil
	}
	var record tasks.Record
	// 环境随任务快照保存，不能读取当前界面的环境选择。
	environment := ""
	if s.SQLQueue != nil && s.SQLQueue.Running {
		queue := s.SQLQueue
		environment = queue.Environment
		record = tasks.Record{ID: queue.ID, Kind: "sql", Version: queue.Version, Title: "批量 SQL 执行", Running: queue.Running}
		for _, row := range queue.Rows {
			executions := row.Executions
			// 当前脚本逐库状态实时读取 Batch，已完成脚本使用自己的结果副本。
			if s.Batch != nil && s.Batch.ItemID == row.ItemID && s.Batch.Version == queue.Version {
				executions = s.Batch.Rows
			}
			record.Rows = append(record.Rows, tasks.Row{ID: row.ItemID, Title: row.Name, State: row.State, Detail: row.Detail, Children: databaseTaskRows(executions)})
		}
	} else if s.SQLQueue != nil && s.Batch != nil && s.SQLQueue.Version == s.Batch.Version && s.SQLQueue.ID != "" {
		// 收尾后仍保存父队列，不退回最后一个脚本而丢失前面的结果。
		queue := s.SQLQueue
		environment = queue.Environment
		record = tasks.Record{ID: queue.ID, Kind: "sql", Version: queue.Version, Title: "批量 SQL 执行", Running: false}
		for _, row := range queue.Rows {
			record.Rows = append(record.Rows, tasks.Row{ID: row.ItemID, Title: row.Name, State: row.State, Detail: row.Detail, Children: databaseTaskRows(row.Executions)})
		}
	} else if s.Batch != nil {
		batch := s.Batch
		environment = batch.Environment
		record = tasks.Record{ID: batch.ID, Kind: "sql", Version: batch.Version, Title: "SQL 执行", Running: batch.Running, Rows: databaseTaskRows(batch.Rows)}
		// 显式核验与执行在任务标题中区分，回看结果不会误认为发生写入。
		if len(batch.Rows) > 0 && batch.Rows[0].VerificationOnly {
			record.Title = "只读结构核验"
		}
	} else {
		return nil
	}
	// 汇总仅依据行结果；SQL 返回 nil 不能替代结构或业务验收。
	// 环境显示在任务标题，回看历史不依赖用户当前选择的环境。
	if option, ok := LookupSQLEnvironment(environment); ok {
		record.Title = option.Label + " · " + record.Title
	}
	record.Status = taskOutcome(record.Rows, record.Running)
	record.Progress = taskProgress(record.Rows)
	return s.TaskHistory.Save(record)
}

// SaveVerifyTask 保存单项或批量验证及逐项证据。
// 无业务入参；调用方须持有 Mu，证据只来自已完成的受限结果。
// 返回：历史写盘错误；不将写入失败伪装为已可靠保存。
func (s *Store) SaveVerifyTask() error {
	// 单项与批量使用不同任务 ID，重试不会覆盖旧证据。
	// 批量条件在启动时固定，证据必须对应当时的服务与日志窗口。
	// 当前队列尚未完成时，不读取旧单项任务作为本轮结果。
	// 完成后保留每项状态，不用一个父级成功抹去失败或证据不足。
	// 结果摘要与证据分栏保存，列表轮询不会携带全部生产日志。
	// 参考条件缺失仍由 AI 分析，历史保存实际目标和采用的证据，不预先跳过。
	// 写盘错误沿原调用链返回，业务结果不会被伪装成可靠历史。
	// 运行中的批量优先，防止上次单项任务被后续队列覆盖。
	if s.TaskHistory == nil {
		return nil
	}
	var record tasks.Record
	if s.VerifyQueue != nil && (s.VerifyQueue.Running || s.SingleVerify == nil) {
		queue := s.VerifyQueue
		record = tasks.Record{ID: queue.ID, Kind: "verify", Version: queue.Version, Title: "批量 AI 验证", Running: queue.Running}
		for _, row := range queue.Rows {
			entry := tasks.Row{ID: row.ItemID, Title: row.Title, State: row.State, Detail: row.Detail, Condition: row.Condition}
			if row.Result != nil {
				entry.Evidence = row.Result.Evidence
				entry.Detail = formatTaskEvidence(*row.Result)
			}
			record.Rows = append(record.Rows, entry)
		}
	} else if s.SingleVerify != nil {
		task := s.SingleVerify
		record = tasks.Record{ID: task.ID, Kind: "verify", Version: task.Version, Title: task.Title, Running: task.Running, Rows: []tasks.Row{{ID: task.ItemID, Title: task.Title, State: task.Result.Status, Condition: task.Condition, Detail: formatTaskEvidence(task.Result), Evidence: task.Result.Evidence}}}
	} else {
		return nil
	}
	// 失败、证据不足和未验证都独立保留，不能混成绿色成功。
	record.Status = taskOutcome(record.Rows, record.Running)
	record.Progress = taskProgress(record.Rows)
	return s.TaskHistory.Save(record)
}

// TrackTask 尝试保存进度，失败时告知界面并保留正式业务审计。
// 参数 kind：scan、sql 或 verify，仅接受应用内部调用。
// 不返回业务值；调用方须持有 Mu，不能在数据库操作期间持锁调用。
func (s *Store) TrackTask(kind string) {
	// 此处是已开始任务的进度保存，不在磁盘错误时重新运行外部操作。
	// 正式逐库审计仍单独保存，进度仓库不能替代数据库执行凭证。
	// 错误提示不打印任务原文，避免把生产证据扩散到运行日志。
	// 启动阶段由调用方直接检查保存错误；此入口用于已运行任务的进度。
	var err error
	switch kind {
	case "scan":
		err = s.SaveScanTask()
	case "sql":
		err = s.SaveSQLTask()
	case "verify":
		err = s.SaveVerifyTask()
	}
	if err != nil {
		// 不输出任务原文或日志证据，只记录写盘故障及任务类别。
		s.Notice = "任务历史保存失败，请保留当前结果并检查磁盘"
		slog.Error("任务历史保存失败", "kind", kind, "error", err)
	}
}

// StopTask 取消精确匹配的当前任务，直到真实收尾前继续占用工作区。
// 参数 id：任务中心提供的本轮稳定 ID，旧历史不能停止新任务。
// 返回：停止已接受返回 nil；任务不存在或已完成时返回错误。
func (s *Store) StopTask(id string) error {
	// 只接受本轮精确 ID，历史按钮不能误停后来启动的任务。
	// 扫描、日志验证和 SQL 均共享工作区的占用边界。
	// 扫描停止会丢弃尚未校验保存的候选，保留已有版本文件。
	// 批量验证同时取消全部工作者，已经保存的结论继续有效。
	// 批量 SQL 在下一个库或语句前检查取消，不再启动后续目标。
	// 单项验证有自己的上下文，不能用退出软件代替停止查询。
	// 单脚本 SQL 的取消器独立于窗口上下文，未来脚本仍可启动。
	// 队列运行时禁止拿当前子脚本 ID 绕过父任务的停止入口。
	// 上下文取消是协作信号，外部数据库可能已提交部分 DDL。
	// 任务真实收尾前继续持有运行标记，不能并发切换配置。
	// 先记录已有进度再显示正在停止，不能丢失此前成功的子项。
	// 停止请求本身也需写盘，磁盘异常会返回明确错误。
	// 已完成记录保持不可取消，不能因为窗口旧状态重写它。
	// 修订号让收起的主界面也能及时看见停止请求。
	// 日志仅记录任务类别和 ID，不打印生产证据或 SQL。
	// 停止与启动、最终回填共用同一个锁，不能出现停止后又启动下一项。
	s.Mu.Lock()
	defer s.Mu.Unlock()
	kind := ""
	switch {
	case s.Scan != nil && s.Scan.Running && s.Scan.ID == id:
		s.Scan.CancelRequested = true
		s.Scan.Phase = "正在终止扫描"
		kind = "scan"
		if s.ScanCancel != nil {
			s.ScanCancel()
		}
	case s.VerifyQueue != nil && s.VerifyQueue.Running && s.VerifyQueue.ID == id:
		kind = "verify"
		// 停止标记与结果保存使用同一把锁，拒绝取消后迟到的通过结论。
		s.VerifyQueue.CancelRequested = true
		if s.VerifyQueueCancel != nil {
			s.VerifyQueueCancel()
		}
	case s.SQLQueue != nil && s.SQLQueue.Running && s.SQLQueue.ID == id:
		kind = "sql"
		if s.SQLQueueCancel != nil {
			s.SQLQueueCancel()
		}
	case s.SingleVerify != nil && s.SingleVerify.Running && s.SingleVerify.ID == id:
		kind = "verify"
		s.SingleVerify.CancelRequested = true
		if s.SingleVerifyCancel != nil {
			s.SingleVerifyCancel()
		}
	case s.Batch != nil && s.Batch.Running && s.Batch.ID == id && (s.SQLQueue == nil || !s.SQLQueue.Running):
		kind = "sql"
		if s.SingleSQLCancel != nil {
			s.SingleSQLCancel()
		}
	default:
		return errors.New("任务已结束或已经变化，请查看最新结果")
	}
	// 只发出取消请求；不杀其他会话、不声称 SQL 已回滚，也不立即释放占用。
	s.TrackTask(kind)
	if s.TaskHistory != nil {
		if record, ok := s.TaskHistory.Get(id); ok && record.Running {
			// 停止意图独立于最终结果，即使某个库需人工核对也能追溯用户操作。
			record.StopRequested = true
			record.Status = "正在停止"
			record.Progress = "正在取消并保存已有结果"
			if err := s.TaskHistory.Save(record); err != nil {
				return err
			}
		}
	}
	s.Revision++
	slog.Info("请求停止任务", "kind", kind, "task_id", id)
	return nil
}

// databaseTaskRows 将正式逐库审计投影为任务明细，不包含连接凭据和 SQL 原文。
// 参数 executions：当前脚本的完整目标列表，尚未执行的库也需要展示。
// 返回：保持目标顺序的独立明细行。
func databaseTaskRows(executions []Execution) []tasks.Row {
	// 逐库状态与正式审计保持同一用语，不能从父任务成功反推子库成功。
	rows := []tasks.Row{}
	for _, entry := range executions {
		// 环境与服务器作为当时执行条件保存，不包含账号或凭据。
		option, _ := LookupSQLEnvironment(entry.Environment)
		rows = append(rows, tasks.Row{ID: entry.ID, Title: entry.Database, State: entry.State, Detail: entry.Detail, Condition: option.Label + " · " + entry.Server})
	}
	// 返回副本，不把执行器正在更新的切片交给 HTTP 编码。
	return rows
}

// taskOutcome 汇总行与子行，保留失败、停止和证据不足的区别。
// 参数 rows：本轮完整状态树；参数 running：后台是否仍真实运行。
// 返回：运行中、已完成、有失败、已停止、已中断或证据不足。
func taskOutcome(rows []tasks.Row, running bool) string {
	// 父任务的处理完成与业务核验通过是两个概念。
	// 结果未知优先提示人工核对，不能落入自动失败重试。
	// 明确失败保留失败语义，即使其他子项全部成功。
	// 尚未开始的项表示停止造成的不完整处理，不作为执行失败。
	// 缺少证据或无法核验的结果不能被计入绿色成功。
	// SQL 子行汇总必须遍历实际目标库，不能只取第一条成功记录。
	// 状态归并只读取快照，不会修改原始逐项结果。
	// 运行标记优先于已完成行数，收尾尚未退出时不能接受下一任务。
	if running {
		return "运行中"
	}
	result := "已完成"
	for _, row := range rows {
		states := []string{row.State}
		if len(row.Children) > 0 {
			states = append(states, taskOutcome(row.Children, false))
		}
		// 父项可能因审计保存失败停止，即使子库都成功也不能吞掉父项错误。
		for _, state := range states {
			// 不确定结果需要人工核对，优先级高于明确的业务失败。
			switch state {
			case "已中断":
				return "已中断"
			case "失败", "有失败", "保存失败", "结构不一致":
				result = "有失败"
			case "未执行", "未验证", "已停止", "未开始":
				if result == "已完成" {
					result = "已停止"
				}
			case "证据不足", "待核验", "已跳过":
				if result == "已完成" {
					result = "证据不足"
				}
			}
		}
	}
	return result
}

// taskProgress 生成完成数量摘要，不包含日志正文。
// 参数 rows：顶层事项或目标库的完整列表。
// 返回：已处理项数与总项数，用于任务目录展示。
func taskProgress(rows []tasks.Row) string {
	// 失败项也算处理过，但其失败结论由状态列单独显示。
	// 未开始项不计为已完成；失败项已处理，但结论仍通过独立状态呈现。
	done := 0
	for _, row := range rows {
		if row.State != "等待中" && row.State != "执行中" && row.State != "验证中" {
			done++
		}
	}
	// 数量仅反映处理进度，不能当成业务验收通过数量。
	return fmt.Sprintf("已处理 %d / %d 项", done, len(rows))
}

// formatTaskEvidence 保留受限结果的摘要、时间窗和后续建议。
// 参数 result：已经通过验证协议校验的日志结果。
// 返回：纯文本明细；证据列表通过独立字段展示。
func formatTaskEvidence(result VerifyResult) string {
	// 后续建议属于当时验收结果，不能用当前修改后的条件重新生成。
	// 生产证据不会混入公共运行日志，只保存到当前用户本地历史。
	parts := []string{result.Summary}
	// 代码、日志与综合理由同结果一起留档，不能只保存最后的状态。
	if result.Analysis != nil {
		parts = append(parts, result.Analysis.Text())
	}
	if result.Window != "" {
		parts = append(parts, "查询窗口："+result.Window)
	}
	if result.NextSteps != "" {
		parts = append(parts, "后续建议："+result.NextSteps)
	}
	// 原文保留换行，界面按文本转义，不解释日志中的 HTML。
	return strings.Join(parts, "\n")
}
