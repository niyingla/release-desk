package aiservice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	gitrepo "release-desk/internal/git/repository"
	"release-desk/internal/identity"
	"release-desk/internal/storage/workspace"
)

// ScanResult 不含人工状态和执行结果，模型只能给出有来源的业务候选。
type ScanResult struct {
	Summary  string     `json:"summary"`
	Warnings []string   `json:"warnings"`
	Items    []ScanItem `json:"items"`
}

type ScanItem struct {
	ID       string      `json:"id"`
	Kind     string      `json:"kind"`
	Action   string      `json:"action"`
	Service  string      `json:"service"`
	Fields   []ScanField `json:"fields"`
	Evidence []string    `json:"evidence"`
}

type ScanField struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type ScanRunner func(context.Context, workspace.VersionFile, workspace.Settings) (ScanResult, error)

// AIScan 启动用户明确选择的当前版本扫描。
// 参数 body：包含 name 的版本请求；只允许已经创建的本地版本。
// 返回：后台任务接收成功返回 nil；版本、工具或忙碌条件不满足时返回错误。
func (a *Service) AIScan(body []byte) (any, error) {
	// 扫描与生产日志验证独立派发，避免误触日志或数据库调用。
	req, err := workspace.Decode[workspace.Version](body)
	if err != nil {
		return nil, err
	}
	// 没有 CLI 时在启动前给出明确错误，不生成空白的成功结果。
	// 工具发现只查安装路径，不读取登录令牌。
	exe := FindCodex()
	if exe == "" {
		return nil, errors.New("未找到 Codex CLI，请先安装并登录")
	}
	// CLI 适配器只返回候选，数据写入统一由 Store 完成。
	return nil, a.StartVersionScan(req.Name, func(ctx context.Context, v workspace.VersionFile, config workspace.Settings) (ScanResult, error) {
		return a.InvokeVersionScan(ctx, exe, v, config)
	})
}

// StartVersionScan 固定输入快照并提交一个后台扫描任务。
// 参数 name：用户选定版本的精确名称，不自动创建其他版本。
// 参数 runner：只读扫描器；生产调用 CLI，测试使用模拟结果。
// 返回：成功返回 nil；版本不存在、SQL 或 Codex 正忙时不启动任务。
func (s *Service) StartVersionScan(name string, runner ScanRunner) error {
	// 与执行和验收共用忙碌约束，扫描期间不会切换仓库配置。
	s.Mu.Lock()
	defer s.Mu.Unlock()
	// 互斥检查与占用写入在同一临界区，连续点击只能启动一次。
	if s.Closing {
		return errors.New("软件正在退出，未启动扫描")
	}
	if s.AIBusy || s.SQLActive() {
		return errors.New("请等待当前 SQL 或 Codex 任务完成")
	}
	v, exists := s.Versions[name]
	// 已保存的版本才有可靠备份目标，不替用户猜测或新增版本。
	if !exists {
		return errors.New("请先保存版本")
	}
	// 日志与提示只包含版本和作者，不输出源文件正文或凭据。
	config := s.Config()
	// 每次重试独立生成进度，旧错误和统计不会混入本轮。
	ctx, cancel := context.WithCancel(s.TaskContext)
	// 本轮持有独立取消句柄，手动终止不能影响其他验收或整个软件。
	s.ScanCancel = cancel
	previousScan := s.Scan
	s.Scan = &workspace.VersionScanState{ID: identity.NewID(), Version: name, Author: config.GitAuthor, Running: true, Phase: "准备扫描", Started: workspace.Stamp(), Warnings: []string{}, Counts: map[string]int{}}
	// 历史入口必须先落盘，失败时不启动收费调用或修改版本。
	if err := s.SaveScanTask(); err != nil {
		// 没有真正启动时恢复此前展示，不能留下一个空白的“扫描完成”。
		s.Scan = previousScan
		cancel()
		return err
	}
	s.AIBusy, s.AIProgress = true, "Codex 正在扫描版本 "+name+"，仅整理 "+config.GitAuthor+" 的改动…"
	// 版本轮询按修订号更新，开始任务也必须推动修订。
	s.Revision++
	// 后台持有独立快照，轮询读取不会与候选构建产生数据竞争。
	go s.ScanVersionInBackground(ctx, workspace.Clone(v), config, runner)
	return nil
}

// CancelVersionScan 终止用户当前看到的扫描，不释放尚未退出的后台任务占用。
// 参数 body：包含本轮稳定 id，旧弹窗或重复请求不能取消另一轮任务。
// 返回：取消已请求返回 nil；任务已变化、已完成或不存在时返回明确错误。
func (a *Service) CancelVersionScan(body []byte) (any, error) {
	// 兼容原来的按钮端点，但取消身份、持久化和占用收尾统一由任务中心管理。
	req, err := workspace.Decode[struct {
		ID string `json:"id"`
	}](body)
	if err != nil {
		return nil, err
	}
	// 精确 ID 绑定本轮任务，旧弹窗不能取消后来启动的扫描、验证或 SQL。
	// 这里只请求停止，不提前释放工作区，也不声称数据库写入已回滚。
	return nil, a.Store.StopTask(req.ID)
}

// SetVersionScanPhase 回显当前实际阶段，终止请求优先于迟到的进度更新。
// 参数 phase：软件自身生成的阶段文字，不接收或输出 Codex 原始事件流。
// 不返回业务结果；只推进轮询修订，不保存任何发布或验收结论。
func (s *Service) SetVersionScanPhase(phase string) {
	// 阶段只描述当前活动，不把阶段到达当成候选已经可靠写盘。
	// 阶段和占用状态在同一锁下更新，界面不会观察到混合的新旧任务信息。
	s.Mu.Lock()
	defer s.Mu.Unlock()
	// 全生命周期只允许一轮扫描；收尾期间保持“正在终止”，避免界面跳回运行。
	if s.Scan == nil || !s.Scan.Running || s.Scan.CancelRequested || s.Closing {
		return
	}
	s.Scan.Phase = phase
	s.TrackTask("scan")
	// 阶段由应用产生，既不解析模型事件流，也不包含文件或日志正文。
	s.AIProgress = "版本 " + s.Scan.Version + " · " + phase
	s.Revision++
}

// ScanVersionInBackground 读取候选并统一完成成功或失败收尾。
// 参数 ctx：本轮独立取消上下文，手动终止和软件退出均可取消。
// 参数 snapshot：任务启动时的版本快照，用于限定名称和已有稳定 ID。
// 参数 config：固定的仓库与作者设置，不包含数据库凭据。
// 参数 runner：扫描实现，只返回结构化业务候选，不写版本文件。
// 不返回业务值；进度、错误和保存结果由 Store 状态反馈。
func (s *Service) ScanVersionInBackground(ctx context.Context, snapshot workspace.VersionFile, config workspace.Settings, runner ScanRunner) {
	// 不把未完成进程、空输出或无来源的结果当作扫描成功。
	started := time.Now()
	slog.Info("版本扫描开始", "version", snapshot.Version.Name, "author", config.GitAuthor)
	result, err := runner(ctx, snapshot, config)
	// 即使外部调用忽略取消并返回成功，也不消费它的迟到候选。
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	// CLI 成功退出仍不等于业务候选可保存，先经过协议检查。
	var candidate workspace.VersionFile
	if err == nil {
		s.SetVersionScanPhase("校验扫描结果")
		candidate, err = PrepareScanVersion(snapshot.Version, result)
	}
	// SQL 正文必须重新从指定作者的 Git 对象读取，不信任模型生成的脚本。
	if err == nil {
		s.SetVersionScanPhase("核对 SQL 来源")
		err = ResolveScanSQL(&candidate, config)
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	// 所有错误走同一收尾入口，避免中途返回遗留忙碌状态。
	// SQL 来源核对期间到达的取消也必须优先于结果保存。
	err = s.FinishVersionScan(snapshot, candidate, result, err)
	// 只记录结果状态和耗时，模型原始输出不进入应用日志。
	slog.Info("版本扫描结束", "version", snapshot.Version.Name, "success", err == nil, "count", len(candidate.Items), "duration_ms", time.Since(started).Milliseconds())
}

// FinishVersionScan 在数据仍匹配时增量保存候选并释放后台状态。
// 参数 snapshot：启动快照，用于识别扫描期间的外部写入。
// 参数 candidate：通过协议与来源检查的版本候选，错误时不会使用。
// 参数 result：模型摘要与待确认项，不能作为实际上线证据。
// 参数 scanErr：扫描或校验错误；非 nil 时保留原版本文件。
// 返回：最终扫描或保存错误；同时通过 Scan、Notice 和 Revision 回显结果。
func (s *Service) FinishVersionScan(snapshot, candidate workspace.VersionFile, result ScanResult, scanErr error) error {
	// 成功和失败都释放占用，用户可以修正条件后重试。
	s.Mu.Lock()
	defer s.Mu.Unlock()
	// 各种返回分支都保存最终摘要；先于解锁执行，不能混入下一轮任务。
	defer s.TrackTask("scan")
	// 结束后释放本轮句柄；直到这里才释放共用 AI 占用。
	if s.ScanCancel != nil {
		s.ScanCancel()
		s.ScanCancel = nil
	}
	s.AIBusy = false
	// 先标记结束，失败也必须让轮询知道本轮不再运行。
	s.Scan.Running, s.Scan.Finished = false, workspace.Stamp()
	// 人工终止不是失败或部分成功；不保存候选、不写“合并成功”计数。
	if s.Scan.CancelRequested || errors.Is(scanErr, context.Canceled) || s.Closing {
		// 用户明确停止时丢弃候选，不能把已扫描部分计为一次成功导入。
		s.Scan.Cancelled = true
		s.Scan.Phase = "已终止"
		s.Scan.Error = ""
		// 主动停止不属于工具故障，界面使用独立终止状态而非错误警告。
		s.Scan.Summary = "扫描已终止，已有版本记录未修改。"
		s.Scan.Counts = map[string]int{}
		// 没有任何候选落盘，数量必须为零，避免用户以为部分版本已经更新。
		s.Scan.Warnings = []string{}
		s.AIProgress = "版本 " + snapshot.Version.Name + " 扫描已终止，已有记录保持不变"
		s.Notice = s.AIProgress
		// 推进修订后即可在保留版本文件的同时让前台关闭等待状态。
		s.Revision++
		return context.Canceled
	}
	if scanErr == nil {
		// 保存使用当前人工状态，后台启动快照只限定目标名称。
		scanErr = s.SaveScanCandidate(snapshot.Version.Name, candidate)
	}
	// 校验失败不写半批数据，摘要与待确认项仍可在扫描面板查看。
	s.Scan.Summary, s.Scan.Warnings = result.Summary, result.Warnings
	// 模型输出仅供审阅，始终按文本回显而不执行其中内容。
	s.Scan.Counts = map[string]int{}
	// 失败面板不能展示“合并成功”数量，避免误以为部分结果已写入。
	if scanErr != nil {
		// 只有真正的扫描或保存异常才显示错误，不与用户停止混用。
		s.Scan.Phase = "扫描失败"
		s.Scan.Error = scanErr.Error()
		s.AIProgress = "版本扫描未写入：" + scanErr.Error()
	} else {
		s.Scan.Phase = "扫描完成"
		// 统计本轮候选，而非包含旧记录的最终总量。
		for _, item := range result.Items {
			s.Scan.Counts[item.Kind]++
		}
		s.AIProgress = fmt.Sprintf("版本 %s 扫描完成，合并 %d 项", snapshot.Version.Name, len(result.Items))
	}
	s.Notice = s.AIProgress
	// 结束也推进修订，让收起面板的用户能看到最终提示。
	s.Revision++
	return scanErr
}

// SaveScanCandidate 核对外部文件后合并可信状态并原子保存。
// 参数 name：扫描所属版本的精确名称。
// 参数 candidate：已经检查协议与 SQL 来源的完整候选。
// 返回：保存成功返回 nil；外部变动、合并冲突或落盘失败时保持原文件。
// 调用方必须持有 Store.Mu，确保核对与内存替换属于同一临界区。
func (s *Service) SaveScanCandidate(name string, candidate workspace.VersionFile) error {
	// 版本可能在关闭或恢复流程中消失，不能生成幽灵记录。
	current, exists := s.Versions[name]
	if !exists {
		return errors.New("扫描期间版本已删除，结果未写入")
	}
	// 文件监听在扫描期间暂停，先检测其他 AI 对本地文件的修改。
	path := filepath.Join(s.Dir, "版本文件", name+".json")
	content, err := os.ReadFile(path)
	// 即使修改后 JSON 仍合法，只要摘要变化也不能悄悄覆盖另一位写入者。
	if err != nil || identity.Digest(string(content)) != s.Hashes[path] {
		return errors.New("扫描期间版本文件被其他程序修改，已保留外部内容，请刷新后重试")
	}
	// 发布元信息由人工维护，扫描只替换或新增有证据的事项内容。
	candidate.Version = current.Version
	// 合并失败时不调用 save，旧业务快照与备份索引均保持完整。
	candidate, err = workspace.MergeExternalVersion(current, candidate)
	if err != nil {
		return err
	}
	// 统一保存会先备份旧文件，再原子替换，执行审计独立保留。
	return s.Save(candidate)
}

// PrepareScanVersion 将受限字段候选转换成完整协议，拒绝模型写入状态。
// 参数 version：目标版本元信息，模型无权改变其发布和归档状态。
// 参数 result：模型提供的事项、字段与来源证据；不能省略摘要或证据。
// 返回：返回合法待办候选；未知字段、重复字段、缺少来源或协议错误时返回错误。
func PrepareScanVersion(version workspace.Version, result ScanResult) (workspace.VersionFile, error) {
	// 每项要求可追溯来源，零项仍需摘要说明扫描范围和原因。
	v := workspace.VersionFile{Format: "release-desk-version", Schema: 1, Version: version, Items: []workspace.Item{}}
	if strings.TrimSpace(result.Summary) == "" {
		return v, errors.New("扫描结果缺少范围摘要")
	}
	for _, source := range result.Items {
		// 每项只允许属于一种业务类型，避免混入其他表格字段。
		definition, known := workspace.Fields[source.Kind]
		// 类型来自协议白名单，不能用未识别类型绕过字段检查。
		if !known {
			return v, errors.New("扫描结果包含未知类型")
		}
		// 只允许本业务类型字段和 Git 来源，状态与验证结果没有模型写入口。
		allowed := map[string]bool{"source_path": true, "source_commit": true, "source_author": true}
		for _, field := range definition {
			// 实际验证结果必须来自独立验收流程，代码扫描不能赋值。
			if field[0] != "result" {
				allowed[field[0]] = true
			}
		}
		data := map[string]string{}
		// 先检查重复再赋值，防止同名字段的后一个值悄悄覆盖前一个。
		for _, field := range source.Fields {
			if _, duplicate := data[field.Name]; duplicate || !allowed[field.Name] {
				return v, fmt.Errorf("扫描字段重复或不允许：%s", field.Name)
			}
			data[field.Name] = field.Value
		}
		// 空字符串不算来源；留空的真实频率和分区数仍可作为待确认内容。
		evidence := []string{}
		for _, value := range source.Evidence {
			// 去掉空白证据，仍保留完整来源文本便于用户核对。
			if strings.TrimSpace(value) != "" {
				evidence = append(evidence, value)
			}
		}
		if len(evidence) == 0 {
			return v, fmt.Errorf("事项缺少来源证据：%s", source.ID)
		}
		// 来源以普通字符串保存，不作为路径打开或工具调用指令执行。
		data["source_evidence"] = strings.Join(evidence, "\n")
		// 人工状态全部由统一规范化和合并入口决定，不接收模型状态。
		v.Items = append(v.Items, workspace.Item{ID: source.ID, Kind: source.Kind, Action: source.Action, Service: source.Service, Data: data, Updated: workspace.Stamp()})
	}
	// 复用统一协议校验，包含 ID 唯一性、必填项和 SQL 正文类型识别。
	err := workspace.ValidateVersion(&v)
	// 显式先完成规范化，再返回候选，确保默认状态也写入最终对象。
	return v, err
}

// ResolveScanSQL 核对 SQL 来源并用作者提交中的原文替换模型文本。
// 参数 candidate：待保存候选指针，仅改写其中 SQL 的可信来源和正文。
// 参数 config：本次固定的 SQL 仓库与作者设置。
// 返回：合法返回 nil；不属于指定版本作者的路径或读取失败时拒绝整批保存。
func ResolveScanSQL(candidate *workspace.VersionFile, config workspace.Settings) error {
	// 没有 SQL 时不访问 SQL 仓库，不能因仓库离线阻断纯业务扫描。
	needed := false
	for _, item := range candidate.Items {
		needed = needed || item.Kind == "sql"
	}
	if !needed {
		return nil
	}
	git := gitrepo.New(config.SQLRepository, config.GitAuthor)
	// 固定读取当前本地 Git 引用，不为扫描触发 fetch 或切换分支。
	scripts, err := git.Scripts(candidate.Version.Name)
	// 作者与完整版本范围由 Git 模块复核，不相信模型的来源声明。
	if err != nil {
		return err
	}
	// 只有后端重新列出的作者提交才可导入，模型提供的 SHA 不视为授权。
	byPath := map[string]gitrepo.GitScript{}
	// 目录只用于定位来源路径；SQL 类型在正文归一后重新解析。
	for _, script := range scripts {
		byPath[script.Path] = script
	}
	for i := range candidate.Items {
		// 其他四类的字段保持原候选，不借 SQL 来源覆盖业务说明。
		item := &candidate.Items[i]
		if item.Kind != "sql" {
			continue
		}
		script, exists := byPath[item.Data["source_path"]]
		// 模型返回不存在的路径时整批拒绝，不能退回使用生成的 SQL。
		if !exists {
			return fmt.Errorf("SQL 不属于指定版本的作者提交：%s", item.Data["name"])
		}
		content, err := git.Content(script)
		// 内容读取再次核对作者清单；任一脚本错误都不保存部分候选。
		if err != nil {
			return err
		}
		// 类型始终以原文重新识别，模型不能改变所属端、脚本正文或来源摘要。
		item.ID = "git-" + identity.Digest(script.Path)[:24]
		// 使用与手动 Git 导入相同的稳定 ID，重复扫描不会重复新增脚本。
		item.Data["sql"], item.Data["side"] = content, script.Side
		item.Data["source_commit"], item.Data["source_author"] = script.Commit, script.Author
		// 保存可信原文摘要供后续刷新识别人工编辑冲突。
		item.Data["source_digest"] = identity.Digest(content)
	}
	// 来源归一可能使两个候选合为同一脚本，最终再次检查 ID 唯一性。
	return workspace.ValidateVersion(candidate)
}
