package sqlservice

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/go-sql-driver/mysql"

	"release-desk/internal/identity"
	"release-desk/internal/policy"
	"release-desk/internal/sql/schema"
	"release-desk/internal/storage/workspace"
)

type DatabaseRunner func(DBConfig, string, []string) (string, error)

type DatabaseFailure struct {
	Err       error
	Uncertain bool
}

// Error 返回封装后的数据库错误说明。
// 无业务入参；使用接收对象或本机启动上下文，不隐式触发外部写操作。
// 返回：返回封装错误的说明文本，保留底层错误语义。
func (e *DatabaseFailure) Error() string {
	// 错误正文由底层数据库调用提供。

	return e.Err.Error()
}

// Unwrap 暴露底层错误用于类型判断。
// 无业务入参；使用接收对象或本机启动上下文，不隐式触发外部写操作。
// 返回：返回底层 error，供 errors.As 判断服务端错误与不确定连接结果。
func (e *DatabaseFailure) Unwrap() error {
	// 保留底层错误供 errors.As 判断 MySQL 服务端错误。

	return e.Err
}

// Execute 重新核验预览并启动逐库执行。
// 参数 plan：绑定 SQL 摘要、服务器和全部目标库的后端执行计划。
// 返回：成功返回 nil 结果与 nil 错误；请求校验、记录定位或后续职责处理失败时返回错误，未返回业务成功凭证。
func (s *Service) Execute(plan Plan) error {
	// 执行参数必须来自后端已确认预览。
	// 连接标识变化时要求重新预览。
	// 重新解析脚本防止绕过预览检查。
	// 重新匹配目标范围，拒绝已经失效的规则。
	// 同一时间只有一个运行批次。
	// SQL 摘要与所属端必须仍然一致。
	// 目标列表先完整进入等待状态。
	// 后台执行不持有整个长事务期间的记录锁。

	s.Mu.Lock()

	defer s.Mu.Unlock()
	// 与保存连接共用工作区锁，核验到启动期间不能切换连接快照。
	config, err := s.LoadDB(plan.Environment)
	if err != nil {
		return err
	}
	// 退出信号与启动在同一锁内检查，不允许窗口关闭后新开一批数据库写入。
	if s.Closing {
		return errors.New("软件正在退出，未启动 SQL")
	}
	if s.SQLActive() || s.AIBusy {
		return errors.New("已有 SQL 运行任务")
	}

	statements, err := s.ValidateSQLPlan(plan, config)
	if err != nil {
		return err
	}

	// 全部目标先进入等待中状态，界面从任务开始就能看到完整列表。

	batch := NewBatch(plan)
	ctx, cancel := context.WithCancel(s.TaskContext)
	s.SingleSQLCancel = cancel
	// 运行标记先于协程启动可见，退出路径能立即识别并取消这批任务。
	s.Batch = batch
	s.SQLQueue = nil
	// 历史先落盘再启动执行；取消上下文仅属于这一轮单脚本任务。
	if err = s.SaveSQLTask(); err != nil {
		batch.Running = false
		cancel()
		return err
	}

	s.Revision++
	go func() {
		defer cancel()
		s.RunBatchWithContext(ctx, batch, config, statements, func(config DBConfig, name string, statements []string) (string, error) {
			return RunDatabase(ctx, config, name, statements)
		})
	}()
	return nil
}

// NewBatch 为全部目标创建可观察的执行批次。
// 参数 plan：绑定 SQL 摘要、服务器和全部目标库的后端执行计划。
// 返回：返回含全部等待中目标行的独立运行批次，不访问数据库。
func NewBatch(plan Plan) *workspace.Batch {
	// 批次标识与每条逐库审计标识彼此独立。

	// 目标列表来自后端预览，执行器不会接受客户端临时追加数据库。
	batch := &workspace.Batch{Environment: workspace.EffectiveSQLEnvironment(plan.Environment), ID: identity.NewID(), Version: plan.Version, ItemID: plan.ItemID, Running: true, Rows: []workspace.Execution{}}
	for _, database := range plan.Databases {
		batch.Rows = append(batch.Rows, workspace.Execution{Environment: batch.Environment, ID: identity.NewID(), Version: plan.Version, ItemID: plan.ItemID, Digest: plan.Digest, Server: plan.Server, Database: database, State: "等待中", SchemaOnly: slices.Contains(plan.SchemaCheckOnly, database)})
	}
	// 显式复核单独标记，不能把一次只读查询当成已经执行过数据初始化。
	for index := range batch.Rows {
		batch.Rows[index].VerificationOnly = plan.VerifyOnly
	}
	return batch
}

// RunBatch 逐库执行并保留失败继续语义。
// 参数 batch：本次后台运行批次；逐库状态由记录锁保护。
// 参数 config：已经校验的本机配置快照；调用期间保持一致。
// 参数 statements：已经通过语法和作用范围检查的有序 SQL 语句列表。
// 参数 run：单库执行函数，生产传真实数据库实现，测试可注入无副作用的模拟实现。
// 不返回业务值；运行结果通过受控状态、记录或日志反馈，失败不会伪装为成功。
func (s *Service) RunBatch(batch *workspace.Batch, config DBConfig, statements []string, run DatabaseRunner) {
	// 单脚本仍复用同一执行器，与多脚本队列保持逐库失败继续和审计一致。
	s.RunBatchWithContext(s.TaskContext, batch, config, statements, run)
}

// RunBatchWithContext 执行一条脚本的全部目标并返回执行基础设施错误。
// 参数 ctx：本轮单脚本或多脚本批次的取消上下文。
// 参数 batch：包含所有目标的独立批次，变更受记录锁保护。
// 参数 config：固定的所选环境连接配置，不在日志输出凭据。
// 参数 statements：已经核验的 SQL 语句，脚本内部顺序保持原文。
// 参数 run：单库执行器，测试可使用无副作用的模拟实现。
// 返回：SQL 业务错误已落逐库审计并继续；取消或审计失败才返回错误停止后续脚本。
func (s *Service) RunBatchWithContext(ctx context.Context, batch *workspace.Batch, config DBConfig, statements []string, run DatabaseRunner) error {
	// 执行顺序沿用预览时的目标列表。
	// 单个库业务失败继续后续库。
	// 不能持久化审计时停止，避免产生不可追溯写入。

	// 业务 SQL 失败继续；本地审计不能持久化时必须停止，避免无记录地继续改库。

	defer s.CompleteBatch(batch)

	for index := range batch.Rows {
		// 停止批次后不再进入下一租户，但当前目标仍先完成中断审计。
		if ctx.Err() != nil {
			return ctx.Err()
		}
		started := time.Now()
		historyIndex, err := s.BeginDatabase(batch, index)
		if err != nil {
			return err
		}

		// 数据库操作在记录锁外执行，较慢的租户不会阻塞界面刷新。
		// 历史成功库只复核结构，不因核验而重复执行初始化数据。
		targetConfig := config
		targetConfig.SchemaOnly = batch.Rows[index].SchemaOnly
		detail, runErr := run(targetConfig, batch.Rows[index].Database, statements)
		row := workspace.Clone(batch.Rows[index])
		row.State, row.Detail = ExecutionOutcome(detail, runErr, time.Since(started))
		// 本轮只读失败也没有发送业务 DDL/DML，不沿用写入可能已提交的提示。
		if row.VerificationOnly && runErr != nil {
			row.Detail = runErr.Error() + "；本次仅查询结构，未执行 DDL/DML"
		}
		row.Finished = workspace.Stamp()
		if err = s.FinishDatabase(batch, index, historyIndex, row); err != nil {
			return err
		}

		// 日志只记录库名、结果、耗时，不记录 SQL 原文或服务端敏感错误片段。
		slog.Info("单库执行完成", "environment", batch.Environment, "version", batch.Version, "item_id", batch.ItemID, "database", row.Database, "state", row.State, "duration_ms", time.Since(started).Milliseconds())
	}
	return nil
}

// BeginDatabase 在访问数据库前持久化开始审计。
// 参数 batch：本次后台运行批次；逐库状态由记录锁保护。
// 参数 index：当前批次或版本中的记录索引，来自后端已验证的定位结果。
// 返回：返回本目标对应的历史索引；开始审计无法落盘时返回错误且不访问数据库。
func (s *Service) BeginDatabase(batch *workspace.Batch, index int) (int, error) {
	// 持久化开始记录后才允许连接目标库。
	// 异常退出时仍能识别结果未确认的目标。
	// 历史索引只在本批次追加时产生。
	// 审计落盘失败必须让本库保持未执行语义。
	// 修订号在可见状态变化后更新。

	s.Mu.Lock()

	defer s.Mu.Unlock()
	// 主动退出停止后续租户，已开始的库仍需在取消后记录真实或不确定结果。
	if s.Closing {
		return 0, errors.New("用户退出软件，后续目标未执行")
	}
	// 先持久化“执行中”再访问数据库，进程异常退出时仍有待核对记录。
	batch.Rows[index].State = "执行中"

	batch.Rows[index].Started = workspace.Stamp()
	s.History = append(s.History, batch.Rows[index])

	historyIndex := len(s.History) - 1
	if err := s.PersistHistory(); err != nil {
		batch.Rows[index].State = "未执行"
		batch.Rows[index].Detail = "审计写入失败，后续操作已停止"
		s.Notice = batch.Rows[index].Detail
		return 0, err
	}

	s.Revision++
	s.TrackTask("sql")
	return historyIndex, nil
}

// FinishDatabase 一致更新界面结果与执行审计。
// 参数 batch：本次后台运行批次；逐库状态由记录锁保护。
// 参数 index：当前批次或版本中的记录索引，来自后端已验证的定位结果。
// 参数 historyIndex：开始审计时追加的历史位置，结束时只更新同一条审计。
// 参数 row：单个目标库完整的执行结果快照。
// 返回：成功返回 nil；结果审计无法落盘时返回错误，调用方停止后续目标。
func (s *Service) FinishDatabase(batch *workspace.Batch, index, historyIndex int, row workspace.Execution) error {
	// 任务快照和正式逐库审计承担不同职责，进度不能代替正式审计落盘。
	// 界面行与历史行来自同一结果快照。
	// 失败库的明细不会被后续库覆盖。
	// 先更新本地结果，再持久化完整审计。

	s.Mu.Lock()

	defer s.Mu.Unlock()
	// 用同一个结果快照更新界面与审计，失败库不会阻断下一库的业务执行。
	batch.Rows[index] = row
	s.History[historyIndex] = row
	s.TrackTask("sql")

	s.Revision++
	if err := s.PersistHistory(); err != nil {
		s.Notice = "执行结果审计写入失败，请核对目标库；后续操作已停止"
		return err
	}
	return nil
}

// CompleteBatch 释放批次运行状态并记录汇总。
// 参数 batch：本次后台运行批次；逐库状态由记录锁保护。
// 不返回业务值；运行结果通过受控状态、记录或日志反馈，失败不会伪装为成功。
func (s *Service) CompleteBatch(batch *workspace.Batch) {
	// 无论批次正常结束还是提前退出都释放运行标记。
	// 完成状态不自动修改发布或验收状态。

	// running 无论成功或错误都释放，否则界面会永久停留在执行中。
	s.Mu.Lock()
	defer s.Mu.Unlock()

	batch.Running = false
	// 取消或审计异常提前结束时，剩余目标明确表示未执行，不能永远停留在等待中。
	for i := range batch.Rows {
		if batch.Rows[i].State == "等待中" {
			batch.Rows[i].State = "未执行"
		}
	}
	s.TrackTask("sql")
	s.Revision++
	slog.Info("SQL 批次结束", "version", batch.Version, "item_id", batch.ItemID, "target_count", len(batch.Rows))
}

// ExecutionOutcome 区分成功、明确失败和结果不确定。
// 参数 detail：单库调用返回的汇总说明，不记录到公共日志正文。
// 参数 err：本次底层错误；用于区分明确失败与提交结果不确定。
// 参数 elapsed：从当前目标库开始执行到完成的实际耗时。
// 返回：返回逐库状态与包含耗时的详情；不确定提交返回已中断而非普通失败。
func ExecutionOutcome(detail string, err error, elapsed time.Duration) (string, string) {
	// 待核验表示缺少可靠结构结论，不能映射成普通 SQL 失败后自动重试。
	// 普通服务端 SQL 错误可以明确显示失败。
	// 连接或提交结果不确定时显示已中断。
	// 不确定结果不能进入默认失败重试集合。

	state := "成功"

	if err != nil {
		// 核验差异与无法核验分别保留，不依据 Exec 返回值伪造结构成功。
		var verification *schema.VerificationError
		if errors.As(err, &verification) {
			state = "结构不一致"
			if verification.Unverified {
				state = "待核验"
			}
			return state, fmt.Sprintf("%.2fs · %s；DDL 可能已提交，需核对实际结果", elapsed.Seconds(), verification.Message)
		}
		// 网络或提交结果不确定时禁止自动重试，避免重复执行可能已经提交的数据变更。
		state = "失败"
		var failure *DatabaseFailure
		if errors.As(err, &failure) && failure.Uncertain {
			state = "已中断"
		}
		detail = err.Error() + "；前序 DDL 可能已提交，重试前请核对"
	}
	return state, fmt.Sprintf("%.2fs · %s", elapsed.Seconds(), detail)
}

// RunDatabase 在单个数据库内逐条执行完整脚本。
// 参数 parent：应用任务上下文；× 退出会取消，不影响托盘最小化后的继续运行。
// 参数 config：已经校验的本机配置快照；调用期间保持一致。
// 参数 name：由预览确认并再次命中所属端规则的目标数据库名。
// 参数 statements：已经通过语法和作用范围检查的有序 SQL 语句列表。
// 返回：返回执行语句数和影响行数摘要；语句或提交失败时返回可区分不确定性的错误。
func RunDatabase(parent context.Context, config DBConfig, name string, statements []string) (string, error) {
	// 每个目标库使用独立连接与事务。
	// 脚本执行受总时限约束，避免无限占用任务。
	// 失败时尝试回滚当前事务。
	// MySQL DDL 隐式提交不能被本地事务恢复。
	// 逐条执行才能定位具体失败语句。
	// 服务端明确报错与网络断线分别归类。
	// 影响行数只用于摘要，不代表业务验收通过。

	db, err := ConnectDB(config, name)
	if err != nil {
		return "", err
	}

	defer db.Close()
	ctx, cancel := context.WithTimeout(parent, policy.DatabaseScriptMinutes*time.Minute)

	defer cancel()

	// 每个库独立事务；MySQL DDL 的隐式提交语义在预览和错误结果中明确提示。
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: config.SchemaOnly})

	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	// 元数据与写入绑定同一目标连接，结构执行前后都查询真实定义。
	detail, err := schema.Run(ctx, statements, schema.TxAccess{Tx: tx}, config.SchemaOnly)
	if err != nil {
		var verification *schema.VerificationError
		if errors.As(err, &verification) {
			return "", err
		}
		var serverError *mysql.MySQLError
		return "", &DatabaseFailure{Err: err, Uncertain: !errors.As(err, &serverError)}
	}

	// 提交失败不等于没有提交，单独标为需人工核对的“已中断”。

	if err = tx.Commit(); err != nil {
		return "", &DatabaseFailure{Err: fmt.Errorf("提交结果未确认：%w", err), Uncertain: true}
	}
	return detail, nil
}
