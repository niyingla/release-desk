package aiservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"release-desk/internal/identity"
	"release-desk/internal/platform/process"
	"release-desk/internal/policy"
	"release-desk/internal/storage/fileio"
	"release-desk/internal/storage/workspace"
	"release-desk/web"
)

type VerifyRequest struct {
	Version string `json:"version"`
	ID      string `json:"id"`
	Start   string `json:"start"`
	End     string `json:"end"`
}

// StartVerify 校验条件并提交后台验收任务。
// 参数 req：本次用户主动选择的版本、事项和查询条件请求。
// 返回：成功返回 nil 结果与 nil 错误；请求校验、记录定位或后续职责处理失败时返回错误，未返回业务成功凭证。
func (s *Service) StartVerify(req VerifyRequest) error {
	// 每轮验收独立绑定取消器，停止当前验证不会取消未来任务。
	// 验证必须由用户主动点击发起。
	// 未找到本机 CLI 时直接给出设置提示。
	// 时间窗先校验，避免无效请求消耗账号额度。
	// 只允许选择变更与验证类型的记录。
	// 同时只运行一个 Codex 验证任务。
	// 服务、检索词与预期是参考线索，缺失时由 AI 结合版本代码分析。
	// 证据是否充分交给实际核验判断，不能在启动前因缺少固定词就跳过。
	// 克隆输入后再交给后台进程。

	// 前置检查在启动进程前完成，避免对明显无效的条件消耗账号额度。
	exe := FindCodex()

	if exe == "" {
		return errors.New("未找到 Codex CLI，请先安装并登录")
	}
	if err := ValidateVerifyWindow(req); err != nil {
		return err
	}

	s.Mu.Lock()
	defer s.Mu.Unlock()
	// × 退出后取消已有任务，也禁止补启动另一个 CLI 进程。
	if s.Closing {
		return errors.New("软件正在退出，未启动验证")
	}

	_, item, _, err := s.Find(req.Version, req.ID)
	if err != nil {
		return err
	}
	if item.Kind != "verify" {
		return errors.New("请选择变更与验证记录")
	}
	// 旧单项入口也遵守人工分类，不因绕开新版列表就消耗模型额度。
	if err := RequireAutomaticVerification([]workspace.Item{item}); err != nil {
		return err
	}

	if s.Scan != nil && s.Scan.Running {
		// 扫描和验收共享一个 Codex 占用，避免两个任务争用同一版本结果。
		return fmt.Errorf("正在扫描版本 %s，请查看扫描进度，或终止扫描后再验证", s.Scan.Version)
	}
	if s.SQLActive() {
		// 多脚本切换间隙仍属于 SQL 工作区，不能据单脚本标记误判为空闲。
		return errors.New("SQL 任务正在执行，请等待执行完成后再验证")
	}
	if s.AIBusy {
		// 批量验收持续持有占用，单项入口不能插队。
		return errors.New("Codex 正在执行其他验证，请等待当前验证完成")
	}
	// 固定本次条件快照，结果回填时再次比较，避免旧证据覆盖新改动。

	snapshot := workspace.Clone(item)
	config := s.Config()
	ctx, cancel := context.WithCancel(s.TaskContext)
	s.SingleVerifyCancel = cancel
	s.VerifyQueue = nil
	s.SingleVerify = &workspace.VerificationTask{ID: identity.NewID(), Version: req.Version, ItemID: req.ID, Title: item.Data["title"], Condition: VerificationCondition(req, item), Running: true, Result: workspace.VerifyResult{Status: "验证中"}}
	// 本轮范围先持久化，保存失败不消耗一次日志查询。
	if err := s.SaveVerifyTask(); err != nil {
		s.SingleVerify.Running = false
		cancel()
		return err
	}

	s.AIBusy = true
	s.AIProgress = "AI 正在结合代码改动与实际证据验证，请稍候…"

	s.Revision++
	go s.VerifyInBackground(ctx, exe, req, snapshot, config)
	return nil
}

// ValidateVerifyWindow 校验北京时间日志查询窗口。
// 参数 req：本次用户主动选择的版本、事项和查询条件请求。
// 返回：合法时间窗返回 nil；格式错误、时间不递增或超前过多时返回错误。
func ValidateVerifyWindow(req VerifyRequest) error {
	// 页面输入按北京时间解释。
	// 查询开始与结束不能相同。

	// 用户输入明确按北京时间解释，不依赖电脑当前时区。
	zone := time.FixedZone("Asia/Shanghai", 8*60*60)
	layout := "2006-01-02T15:04"
	start, err := time.ParseInLocation(layout, req.Start, zone)

	if err != nil {
		return errors.New("开始时间无效")
	}
	end, err := time.ParseInLocation(layout, req.End, zone)

	// 时间必须递增；允许几分钟的时钟偏差，避免本机轻微误差阻断验证。
	if err != nil || !end.After(start) || end.After(time.Now().Add(5*time.Minute)) {
		return errors.New("验证时间范围无效")
	}
	return nil
}

// VerifyInBackground 执行并回填单项 Codex 验证。
// 参数 ctx：本轮可独立取消的上下文，不影响其他会话或未来任务。
// 参数 exe：已经发现的本机可执行文件路径，不包含用户业务指令。
// 参数 req：本次用户主动选择的版本、事项和查询条件请求。
// 参数 item：当前所选事项的独立快照，不接受模型修改执行或发布状态。
// 参数 config：已经校验的本机配置快照；调用期间保持一致。
// 不返回业务值；运行结果通过受控状态、记录或日志反馈，失败不会伪装为成功。
func (s *Service) VerifyInBackground(ctx context.Context, exe string, req VerifyRequest, item workspace.Item, config workspace.Settings) {
	// 取消后只记录停止，不把未完成的日志查询解释成业务失败。
	// 旧结论只在验收保存成功后更新，磁盘错误留作本轮保存失败。
	// 后台的最后一次回填也必须尊重停止请求，避免迟到证据覆盖原结果。
	// 开始日志只记录版本和业务 ID。
	// 不保存 Codex 的原始输出流。
	// 调用错误表示无法获得证据，不能自动判为业务失败。

	started := time.Now()

	slog.Info("Codex 验证开始", "version", req.Version, "item_id", req.ID, "service", item.Service)
	result, err := s.InvokeCodex(ctx, exe, req, item, config)
	// 停止不能写成业务验证失败，也不能回填取消后迟到的通过结论。
	if ctx.Err() != nil {
		s.CompleteSingleVerify(workspace.VerifyResult{Status: "已停止", Summary: "本轮查询已停止，原有验收结论保留"})
		return
	}

	// 调用失败属于“证据不足”，不能当作业务失败或上线通过。
	if err != nil {
		result = workspace.VerifyResult{Status: "证据不足", Confirmation: "未确认", Blocker: "取证受阻", Summary: err.Error(), Environment: "生产", Window: req.Start + " ~ " + req.End, Evidence: []string{}, NextSteps: "检查代码仓库、Codex 登录和只读取证权限后重新验证"}
	}

	result.At = workspace.Stamp()
	if err = s.FinishVerification(ctx, req, item, result); err != nil {
		result.Status = "保存失败"
		result.Summary = err.Error()
		if errors.Is(err, context.Canceled) {
			result.Status = "已停止"
			result.Summary = "已停止查询，迟到证据未写入事项"
		}
	}
	s.CompleteSingleVerify(result)
	slog.Info("Codex 验证结束", "version", req.Version, "item_id", req.ID, "state", result.Status, "duration_ms", time.Since(started).Milliseconds())
}

// CompleteSingleVerify 保存本轮终态并释放独立取消句柄。
// 参数 result：已确认的受限验收结果；停止时只记录停止语义。
// 不返回业务值；历史失败通过统一通知提示，人工发布状态不自动改变。
func (s *Service) CompleteSingleVerify(result workspace.VerifyResult) {
	// 单项结果保存在独立任务中，关闭弹窗不会删除证据。
	// 任务对象可能在隔离测试中未初始化，不能据此创建虚假的验收记录。
	// 取消句柄释放后仍保留最终任务对象，供当前窗口和历史查询。
	// AI 占用直到该收尾完成才释放，避免另一轮扫描被误清除。
	// 回填与停止请求串行，不能把已结束任务留成“仍在查询”。
	s.Mu.Lock()
	defer s.Mu.Unlock()
	if s.SingleVerify == nil {
		return
	}
	s.SingleVerify.Result = result
	s.SingleVerify.Running = false
	s.AIBusy = false
	// 正常完成也释放上下文，后续任务不会复用上一轮取消器。
	if s.SingleVerifyCancel != nil {
		s.SingleVerifyCancel()
		s.SingleVerifyCancel = nil
	}
	s.TrackTask("verify")
	s.AIProgress = "本轮 AI 验证已结束"
	s.Revision++
}

// InvokeCodex 以只读模式调用 Codex 并读取结构化结果。
// 参数 parent：本次任务或队列的可取消上下文，停止批次不会退出软件。
// 参数 exe：已经发现的本机可执行文件路径，不包含用户业务指令。
// 参数 req：本次用户主动选择的版本、事项和查询条件请求。
// 参数 item：当前所选事项的独立快照，不接受模型修改执行或发布状态。
// 参数 config：已经校验的本机配置快照；调用期间保持一致。
// 返回：返回结构化验收结果；进程、超时、输出读取或证据检查失败时返回错误。
func (s *Service) InvokeCodex(parent context.Context, exe string, req VerifyRequest, item workspace.Item, config workspace.Settings) (workspace.VerifyResult, error) {
	// 网络阻断不是业务失败；先在同一只读权限下预检，再消耗模型额度。
	ReportVerificationProgress(parent, "正在预检日志网络与只读权限，尚未调用 AI")
	if err := s.preflightVerificationNetwork(parent, exe, config); err != nil {
		return workspace.VerifyResult{}, err
	}
	ReportVerificationProgress(parent, "日志网络预检通过；查询权限和实际样本由本轮 AI 取证确认")
	// 每项继续独立核验，不能把同批网络可达或他项通过当成本项业务证据。
	// 每次调用使用唯一目录，隔离旧结果。
	// Schema 来自软件内嵌资源，不接受记录内容更改输出结构。
	// 结果路径由应用创建，不由业务字段指定。
	// 命令使用参数数组，不交给 shell 重新解释。
	// 复用用户登录态，不读取或复制登录令牌。
	// 不通过本工具指定或升级用户模型。

	// 每次运行使用独立目录，旧输出不会误作本次结果。
	runDir := filepath.Join(s.Dir, "Codex验证", identity.NewID())

	if err := os.MkdirAll(runDir, 0700); err != nil {
		return workspace.VerifyResult{}, err
	}
	schema, err := web.Assets.ReadFile("prompts/verification-schema.json")
	if err != nil {
		return workspace.VerifyResult{}, err
	}

	schemaPath := filepath.Join(runDir, "schema.json")
	outputPath := filepath.Join(runDir, "result.json")
	if err = fileio.AtomicWrite(schemaPath, schema); err != nil {
		return workspace.VerifyResult{}, err
	}

	// CLI 使用用户已有配置与登录态，只授予只读权限，不替用户指定模型。

	ctx, cancel := context.WithTimeout(parent, policy.CodexTimeoutMinutes*time.Minute)
	defer cancel()

	// 文件仍只读；只给本次进程开放配置中的日志主机，密钥由既有只读脚本加载。
	permissions, err := VerificationPermissionArguments(config)
	if err != nil {
		// 权限构造失败立即结束，不尝试取消沙箱限制继续运行。
		return workspace.VerifyResult{}, err
	}
	args := append([]string{"exec", "--json"}, permissions...)
	args = append(args, "--ephemeral", "--color", "never", "--output-schema", schemaPath, "-o", outputPath, "-C", config.CodeRepository, "-")
	cmd := process.Command(ctx, exe, args...)
	// 同一工作区锁下读取人工发布背景，其他验证项的结论不会被拼进输入。
	s.Mu.Lock()
	publication := s.Versions[req.Version].Version
	s.Mu.Unlock()
	cmd.Stdin = strings.NewReader(VerificationPrompt(req.Version, item, req.Start, req.End, config, publication))

	// JSONL 只消费明确可公开的进度摘要，原始事件与 stderr 均不落盘。
	cmd.Stdout = newVerificationEventWriter(ctx, func(summary string) { ReportVerificationProgress(ctx, summary) })

	cmd.Stderr = io.Discard
	if err = process.RunManagedCommand(cmd); err != nil {
		return workspace.VerifyResult{}, fmt.Errorf("Codex 未完成：%w；请检查代码仓库、登录和只读取证权限", err)
	}
	return ReadVerificationResult(outputPath)
}

// ReadVerificationResult 检查 Codex 结果格式与最小证据要求。
// 参数 path：本方法要读取、写入或打开的受控本机路径；不得由业务正文拼接任意系统路径。
// 返回：返回已检查环境与状态的结果；无证据的通过结论会降为证据不足。
func ReadVerificationResult(path string) (workspace.VerifyResult, error) {
	// 结果文件只属于本次唯一运行目录。
	// 读取失败不能使用上一次验收结果替代。
	// JSON 格式正确仍需校验有限状态集合。
	// 环境必须明确为生产。
	// 通过结论必须具有摘要与至少一条证据。

	var result workspace.VerifyResult
	content, err := os.ReadFile(path)

	if err != nil {
		return result, errors.New("Codex 未生成验证结果")
	}

	if err = json.Unmarshal(content, &result); err != nil {
		return result, errors.New("Codex 返回结果格式无效")
	}

	// 状态与环境都经过校验，格式正确也不能代替实际证据。
	if !slices.Contains([]string{"通过", "失败", "证据不足", "未发现异常"}, result.Status) || result.Environment != "生产" {
		return result, errors.New("Codex 结论或环境无效")
	}
	// 新字段只接受协议内值；空值兼容旧版历史和本机测试结果。
	if !slices.Contains([]string{"", "直接确认", "高概率确认", "未确认"}, result.Confirmation) || !slices.Contains([]string{"", "取证受阻", "无适用样本", "版本关联不足", "关键证据缺失"}, result.Blocker) {
		return result, errors.New("Codex 确认方式或不足原因无效")
	}

	// 不限制证据种类或固定关键词，只排除空白占位，防止空数组伪装成已核验。
	evidence := make([]string, 0, len(result.Evidence))
	for _, entry := range result.Evidence {
		if strings.TrimSpace(entry) != "" {
			evidence = append(evidence, entry)
		}
	}
	result.Evidence = evidence
	// 中立状态也必须实际查过日志；工具失败或空白依据不能伪装成“没发现问题”。
	if result.Status == "未发现异常" && (!result.LogQuerySucceeded || result.Confirmation != "未确认" || result.Blocker == "取证受阻" || len(evidence) == 0) {
		result.Status = "证据不足"
		result.Summary = "尚无成功查询及有效观察依据，不能标为未发现异常"
	}
	// 允许高概率判断，但不能把模型明确未确认的结论显示成已通过。
	if result.Status == "通过" && (result.Confirmation == "未确认" || result.Blocker != "") {
		result.Status = "证据不足"
		result.Summary = "本轮仍存在决定性证据缺口，尚未确认目标实现"
	}
	// 有结论但没有任何可读依据时，保留待补证语义，不替模型编造验证结果。
	if result.Status == "通过" && (len(result.Evidence) == 0 || strings.TrimSpace(result.Summary) == "") {
		result.Status = "证据不足"
		result.Summary = "未提供有效核验证据，不能自动判定通过"
	}
	return result, nil
}

// FinishVerification 在条件仍匹配时更新当前验收状态。
// 参数 ctx：本轮单项或批次上下文；停止后不能再接受迟到的结论。
// 参数 req：本次用户主动选择的版本、事项和查询条件请求。
// 参数 snapshot：后台操作启动时固定的业务条件快照。
// 参数 result：经过格式与证据约束检查的结构化验收结果。
// 返回：保存成功返回 nil；找不到事项或写盘失败返回错误，批量调用应停止后续验收。
func (s *Service) FinishVerification(ctx context.Context, req VerifyRequest, snapshot workspace.Item, result workspace.VerifyResult) error {
	// 后台回填与界面编辑共用记录锁。
	// 释放忙碌状态保证用户可以继续操作。
	// 找不到原记录时不创建幽灵记录。
	// 更新使用副本，持久化失败不污染已有对象。
	// 每次结果追加到验证历史而不是覆盖。
	// 修改条件后旧结论只能作为历史查看。
	// 只有摘要仍一致才更新当前验收状态。

	s.Mu.Lock()

	defer s.Mu.Unlock()
	// 停止和回填在同一个临界区判定，取消后返回的成功结果也不能覆盖旧结论。
	if ctx.Err() != nil || s.Closing || s.VerifyQueue != nil && s.VerifyQueue.Running && s.VerifyQueue.Version == req.Version && s.VerifyQueue.CancelRequested {
		return context.Canceled
	}

	// 批量验收在两项之间仍持有占用，单项完成才可立即释放。
	s.AIBusy = s.VerifyQueue != nil && s.VerifyQueue.Running || s.SingleVerify != nil && s.SingleVerify.Running
	// 停止先被接受后，迟到证据不得覆盖原事项已有的人工或历史结论。
	if s.SingleVerify != nil && s.SingleVerify.ItemID == req.ID && s.SingleVerify.CancelRequested {
		return context.Canceled
	}
	version, current, index, err := s.Find(req.Version, req.ID)

	// 即使当前验证条件发生变化，旧证据仍可追溯，但不能更新当前验收状态。

	if err == nil {
		version = workspace.Clone(version)
		current = version.Items[index]
		current.VerifyRuns = append(current.VerifyRuns, result)
		current.VerifyState = workspace.PendingVerificationState(current)
		if workspace.VerificationHash(current) == workspace.VerificationHash(snapshot) {
			current.VerifyState = result.Status
			current.Data["result"] = FormatResult(result)
		}
		version.Items[index] = current
		if saveErr := s.Save(version); saveErr != nil {
			s.Notice = "验证结果保存失败：" + saveErr.Error()
			s.Revision++
			return saveErr
		}
	}

	// 只把结论与汇总写入工作台提示，不向日志重复输出生产证据正文。

	s.AIProgress = "Codex 验证完成：" + result.Status

	s.Notice = s.AIProgress
	s.Revision++
	return err
}

// FormatResult 将验收结论转换成可复制文本。
// 参数 result：经过格式与证据约束检查的结构化验收结果。
// 返回：返回包含结论、环境、窗口、证据和后续步骤的完整纯文本。
func FormatResult(result workspace.VerifyResult) string {
	// 纯文本适合一键复制到独立 AI 对话。

	// 生成适合单项复制的纯文本，保留查询窗口与后续补证步骤。
	// 分项分析沿用同一纯文本格式，复制结果与任务历史都能核对判断过程。
	analysis := result.Analysis.Text()
	// 确认方式属于本轮证据快照，间接判断不能在复制时伪装成直接复现。
	if result.Confirmation != "" {
		analysis = "确认方式：" + result.Confirmation + "\n" + analysis
	}
	// 不足分类用于区分网络故障和实际缺样本，不能在复制时丢失可操作原因。
	if result.Blocker != "" {
		analysis = "不足原因：" + result.Blocker + "\n" + analysis
	}
	if analysis != "" {
		analysis = "\n" + analysis
	}
	return result.Status + " · " + result.At + "\n环境：" + result.Environment + "\n窗口：" + result.Window + "\n" + result.Summary + analysis + "\n证据：\n" + strings.Join(result.Evidence, "\n") + "\n后续：" + result.NextSteps
}
