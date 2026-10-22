package aiservice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"release-desk/internal/identity"
	"release-desk/internal/policy"
	"release-desk/internal/storage/workspace"
)

type VerifyQueueRequest struct {
	Concurrency int      `json:"concurrency,omitempty"`
	Version     string   `json:"version"`
	IDs         []string `json:"ids"`
	Start       string   `json:"start"`
	End         string   `json:"end"`
}

type VerificationRunner func(context.Context, VerifyRequest, workspace.Item, workspace.Settings) (workspace.VerifyResult, error)

// StartVerifyQueue 校验用户选择并占用整个批量验收生命周期。
// 参数 req：同一版本勾选项及统一北京时间窗口，不能省略目标或时间。
// 参数 run：只读日志验收器；真实调用 CLI，测试使用模拟证据。
// 返回：后台接收成功返回 nil；选择、时间、占用条件不满足时不启动任何任务。
func (s *Service) StartVerifyQueue(req VerifyQueueRequest, run VerificationRunner) error {
	// 整批选择同时进入历史，未开始的条目不会因中途停止而消失。
	// 所有选择先整体确认类型，不能成功启动前几项后才发现跨类型项。
	if err := ValidateVerifyWindow(VerifyRequest{Start: req.Start, End: req.End}); err != nil {
		return err
	}
	// 旧请求省略并发数时继续使用默认六项，页面选值仍须由后端校验。
	concurrency := req.Concurrency
	if concurrency == 0 {
		concurrency = policy.VerificationConcurrency
	}
	if concurrency < 1 || concurrency > policy.VerificationConcurrency {
		return fmt.Errorf("验证并发数必须为 1～%d", policy.VerificationConcurrency)
	}
	items, err := s.SelectedItems(workspace.SelectionRequest{Version: req.Version, IDs: req.IDs}, "verify")
	// 选择只包含变更验证，不允许将 SQL 或配置记录当作日志验收条件。
	if err != nil {
		return err
	}
	s.Mu.Lock()
	defer s.Mu.Unlock()
	// 与扫描和 SQL 共用占用约束；逐项验收之间也不释放 AI 占用。
	if s.Closing || s.AIBusy || s.SQLActive() {
		return errors.New("请等待当前任务完成后再启动批量验证")
	}
	// 启动前记住用户明确选择的偏好；保存失败不启动任何外部模型。
	if err := s.RememberVerificationConcurrency(concurrency); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(s.TaskContext)
	// 批次取消与整个软件退出各有上下文，不会影响用户其他 Codex 对话。
	s.VerifyQueueCancel = cancel
	// 勾选项少于用户上限时只建立必要工作者，但仍记住用户原先的选择。
	queue := &workspace.VerifyQueue{ID: identity.NewID(), Version: req.Version, Running: true, Concurrency: min(concurrency, len(items)), Rows: []workspace.VerifyQueueRow{}}
	for _, item := range items {
		// 启动前登记全部事项，用户从第一项开始就能看到完整等待队列。
		queue.Rows = append(queue.Rows, workspace.VerifyQueueRow{ItemID: item.ID, Title: item.Data["title"], State: "等待中", Condition: VerificationCondition(VerifyRequest{Version: req.Version, Start: req.Start, End: req.End}, item)})
	}
	s.VerifyQueue = queue
	s.SingleVerify = nil
	// 保存整批范围后才能调用第一项，重启后仍可核对本轮实际选择。
	if err := s.SaveVerifyTask(); err != nil {
		queue.Running = false
		cancel()
		return err
	}
	// AI 占用在协程启动前建立，连续点击不能产生两份收费调用。
	s.AIBusy = true
	s.AIProgress = fmt.Sprintf("正在批量验证 %d 项变更，最多 %d 项并发…", len(items), queue.Concurrency)
	// 状态修订让收起面板的主窗口也能显示批量任务正在运行。
	s.Revision++
	// 后台持有固定条件，结果回填仍需比较当前事项摘要。
	// 历史逐项保存，后续某项失败不会回退已经完成的验收。
	go s.RunVerifyQueue(ctx, queue, req, items, s.Config(), run)
	return nil
}

// RunVerifyQueue 以固定工作者池核验勾选项，有空位立即补充下一项。
// 参数 ctx：批次取消上下文，停止按钮或退出软件都会取消。
// 参数 queue：已占用运行标记的批次对象，进度写入受记录锁保护。
// 参数 req：统一版本和时间窗口，不按条目猜测生产部署时间。
// 参数 items：启动时固定的事项快照，用于摘要匹配和逐项回填。
// 参数 config：固定的非凭据设置，包含代码仓库与日志技能位置。
// 参数 run：单项只读验收器，失败不当作业务未上线。
// 不返回业务值；每项真实结论与历史仍写入原版本事项。
func (s *Service) RunVerifyQueue(ctx context.Context, queue *workspace.VerifyQueue, req VerifyQueueRequest, items []workspace.Item, config workspace.Settings, run VerificationRunner) {
	// 子上下文同时覆盖所有工作者；本地保存失败也必须阻止后续收费调用。
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// 最后一个工作者退出后才释放占用，不能在停止请求刚发出时插入新批次。
	defer s.CompleteVerifyQueue(queue)
	// 日志只记录规模和并发度，不包含生产证据正文。
	slog.Info("批量 AI 验证开始", "version", req.Version, "count", len(items), "concurrency", queue.Concurrency)
	jobs := make(chan int)
	var workers sync.WaitGroup
	// 不为每条事项创建协程，待验证项只保留索引，避免长列表占满进程资源。
	for range queue.Concurrency {
		workers.Add(1)
		// 每个工作者只处理当前领取的事项，不共享 CLI 输出文件或可变业务快照。
		go func() {
			defer workers.Done()
			for index := range jobs {
				// 独立事项在锁外取证，只有进度和最终写回才持有工作区锁。
				if !s.RunVerifyQueueItem(ctx, queue, index, req, items[index], config, run) {
					// 单项持久化异常或取消需要广播给其余工作者，普通业务失败已在内部处理。
					cancel()
					return
				}
			}
		}()
	}
	// 无缓冲派发让可用工作者立即补位，停止后最多只取到一项未开始的索引。
dispatch:
	for index := range items {
		// 取消后即使同时有空位，工作者仍会在登记开始前复核上下文。
		select {
		case <-ctx.Done():
			break dispatch
		case jobs <- index:
		}
	}
	// 等待已开始的全部取证退出，才能把运行标记和任务历史转成终态。
	close(jobs)
	workers.Wait()
}

// UpdateVerifyQueue 更新一个验收项的可观察进度。
// 参数 queue：本轮队列对象，仅更新该队列所属的行。
// 参数 index：已经校验的队列索引，不用于跨版本定位。
// 参数 state：等待、执行或受限验收状态，不修改原事项发布标记。
// 参数 detail：可读摘要，不主动记录生产日志正文。
// 参数 result：已成功保存的本轮结论；开始、停止或保存失败时为 nil。
// 不返回业务值；调用方必须持有 Mu，轮询通过修订号取得独立快照。
func (s *Service) UpdateVerifyQueue(queue *workspace.VerifyQueue, index int, state, detail string, result *workspace.VerifyResult) {
	// 队列和版本文件各自承担进度与证据职责，进度变更不等于验收已保存。
	queue.Rows[index].State, queue.Rows[index].Detail = state, detail
	queue.Rows[index].Result = result
	// 并发完成顺序与行号无关，必须遍历真实状态计算已完成和运行中数量。
	done, active := 0, 0
	for _, row := range queue.Rows {
		// 正在查询不算完成；等待中的事项也不能被当成已处理。
		switch row.State {
		case "验证中":
			active++
		case "等待中":
		default:
			// 失败、不足和停止均已处理，但仍通过各自状态保留业务含义。
			done++
		}
	}
	// 状态与对应证据在一次锁内一起持久化，不展示“已通过但还没有结果”的半成品。
	s.TrackTask("verify")
	s.AIProgress = fmt.Sprintf("批量验证已处理 %d/%d · 运行中 %d · 最多 %d 并发", done, len(queue.Rows), active, queue.Concurrency)
	s.Revision++
	// 日志只记录事项 ID 和状态，不重复传播生产证据正文。
	slog.Info("批量验证事项状态", "version", queue.Version, "item_id", queue.Rows[index].ItemID, "state", state)
}

// CompleteVerifyQueue 释放占用并将未开始项保留为停止状态。
// 参数 queue：本轮批次对象，已经完成项的真实状态不变。
// 不返回业务值；人工状态与已有验收历史均不重置。
func (s *Service) CompleteVerifyQueue(queue *workspace.VerifyQueue) {
	// 收尾会落完整队列终态，重启后仍能区分未验证和已失败。
	// 结束时一次释放 AI 占用，不让其他任务插入两项验收之间。
	s.Mu.Lock()
	defer s.Mu.Unlock()
	// 尚未查询的项保持未验证语义，不为它们生成失败结论或验证历史。
	for i := range queue.Rows {
		if queue.Rows[i].State == "等待中" {
			queue.Rows[i].State = "未验证"
		}
	}
	queue.Running = false
	s.TrackTask("verify")
	// 批次终态与 AI 占用同锁更新，后续编辑不会观察到半完成状态。
	s.AIBusy = false
	if s.VerifyQueueCancel != nil {
		// 完成后移除取消器，旧弹窗只能看到结果，不能再取消新的任务。
		s.VerifyQueueCancel()
		s.VerifyQueueCancel = nil
	}
	// 简短提示放在工作台，完整证据仍从各事项结果查看。
	s.AIProgress = "批量验证已结束"
	s.Notice = s.AIProgress
	s.Revision++
	// 汇总日志只含版本与数量，不包含生产证据正文或登录信息。
	slog.Info("批量 AI 验证结束", "version", queue.Version, "count", len(queue.Rows))
}

// AIVerifyBatch 启动用户明确勾选的生产日志批量验收。
// 参数 body：多项 ID 和统一时间窗，不包含代码、命令或数据库凭据。
// 返回：后台接收成功返回 nil；工具缺失、请求或选择校验失败返回错误。
func (a *Service) AIVerifyBatch(body []byte) (any, error) {
	// 工具发现不读取登录令牌，未安装时在调用前提示。
	req, err := workspace.Decode[VerifyQueueRequest](body)
	// 结构先校验，参数不合法时不查工具、不读取日志、不启动模型。
	if err != nil {
		return nil, err
	}
	exe := FindCodex()
	// 路径发现仅复用已有工具，不安装 CLI 或修改用户模型配置。
	if exe == "" {
		return nil, errors.New("未找到 Codex CLI，请先安装并登录")
	}
	return nil, a.StartVerifyQueue(req, func(ctx context.Context, r VerifyRequest, item workspace.Item, cfg workspace.Settings) (workspace.VerifyResult, error) {
		// 每次调用按同一只读验收协议检查环境和证据，结果独立写回该事项。
		return a.InvokeCodex(ctx, exe, r, item, cfg)
	})
}

// CancelVerifyBatch 只停止用户当前查看的批次。
// 参数 body：包含 id 的批次请求，避免旧弹窗停止另一个新任务。
// 返回：已发出停止请求返回 nil；批次不匹配时不取消任何任务。
func (a *Service) CancelVerifyBatch(body []byte) (any, error) {
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
