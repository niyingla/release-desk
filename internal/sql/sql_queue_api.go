package sqlservice

import (
	"errors"
	"time"

	"release-desk/internal/policy"
	"release-desk/internal/storage/workspace"
)

// DBBatchPlan 读取勾选项并生成有序、逐脚本的只读执行预览。
// 参数 body：版本与勾选 ID 列表；前端不能传入 SQL 正文或目标库。
// 返回：保存于后端的一次性批量预览，任一来源错误时不启动执行。
func (a *Service) DBBatchPlan(body []byte) (any, error) {
	// 先验证完整选择，查询目标时也不执行任何业务 SQL。
	req, err := workspace.Decode[struct {
		workspace.SelectionRequest
		Environment string `json:"environment"`
	}](body)
	// 环境只参与 SQL 规划，通用选择器仍只校验版本与事项归属。
	if err != nil {
		return nil, err
	}
	items, err := a.Store.SelectedItems(req.SelectionRequest, "sql")
	// 选择错误在访问目标环境数据库前结束，不读取无关版本的目标库。
	if err != nil {
		return nil, err
	}
	preview, err := BuildSQLQueuePreview(req.Version, items, func(id string) (Plan, error) { return a.Plan(req.Version, id, false, req.Environment, false) })
	// 网络发现与语法解析均完成后，才发布一份完整、可确认的预览。
	if err != nil {
		return nil, err
	}
	// 目标与 SQL 只从后端缓存取回，确认界面无法篡改或扩充执行对象。
	a.PlanMu.Lock()
	defer a.PlanMu.Unlock()
	if a.QueuePlans == nil {
		// 首次预览按需建立私有缓存，旧启动入口无需预先初始化。
		a.QueuePlans = map[string]SQLQueuePreview{}
	}
	// 预览在后台保持不可变，保留不经过 JSON 的内部生成时间以校验有效期。
	a.QueuePlans[preview.Token] = preview
	// 返回序列化视图，之后的执行只凭 token 回到这份可信计划。
	return preview, nil
}

// DBBatchExecute 消费用户勾选确认的批量预览并启动执行。
// 参数 body：一次性 token 与布尔 confirm，不接受文字确认或客户端 SQL。
// 返回：后台接收成功返回 nil；未确认、过期、来源变化时整批不执行。
func (a *Service) DBBatchExecute(body []byte) (any, error) {
	// 确认前不消费预览，取消勾选不能让用户误失去已检查的对象。
	req, err := workspace.Decode[struct {
		Token   string `json:"token"`
		Confirm bool   `json:"confirm"`
	}](body)
	if err != nil {
		return nil, err
	}
	if !req.Confirm {
		// 未勾选确认只返回错误，不消费用户已经核对过的预览。
		return nil, errors.New("请勾选确认全部脚本和目标库")
	}
	a.PlanMu.Lock()
	// 查找与删除处于同一临界区，两个并发请求只能有一个取得令牌。
	preview, exists := a.QueuePlans[req.Token]
	// 不接收客户端计划数组，目标、顺序和原文全部来自后端缓存。
	delete(a.QueuePlans, req.Token)
	a.PlanMu.Unlock()
	// 令牌仅能使用一次，避免连续点击造成整批 DML 重复执行。
	if !exists || time.Since(preview.Created) > policy.PlanExpiryMinutes*time.Minute {
		// 过期后重新识别范围，不能长期复用旧的数据库集合。
		return nil, errors.New("批量预览已过期，请重新预览")
	}
	config, err := a.LoadDB(preview.Environment)
	// 凭据只在后端解密，批次请求和返回值均不携带密码。
	if err != nil {
		return nil, err
	}
	// 获取令牌后仍核验整批来源，不会跳过 SQL 摘要和所选环境范围检查。
	return nil, a.StartSQLQueue(preview, config, RunDatabase)
}

// CancelSQLBatch 请求停止匹配的 SQL 队列并保留已产生的执行审计。
// 参数 body：当前弹窗持有的队列 id，不能用旧 ID 停止新批次。
// 返回：发出停止请求返回 nil；ID 不匹配时不触及其他执行。
func (a *Service) CancelSQLBatch(body []byte) (any, error) {
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
