package workspace

import (
	"errors"
)

type SelectionRequest struct {
	Version string   `json:"version"`
	IDs     []string `json:"ids"`
}

// SelectedItems 获取当前版本中明确勾选的单一业务类型快照。
// 参数 req：版本名称及稳定事项 ID，不允许空选择、重复项或跨版本推断。
// 参数 kind：此操作允许的唯一业务类型，SQL 与日志验证不能混选。
// 返回：按请求顺序返回独立快照；任一 ID 错误时整体拒绝，不处理半批。
func (s *Store) SelectedItems(req SelectionRequest, kind string) ([]Item, error) {
	// 读取在同一把锁中完成，前台选择不会拼接成多个版本的混合快照。
	s.Mu.Lock()
	defer s.Mu.Unlock()
	// 空列表不能默认为全部版本事项，批量写操作必须有明确选择。
	if len(req.IDs) == 0 {
		return nil, errors.New("请先勾选至少一项")
	}
	items := []Item{}
	seen := map[string]bool{}
	for _, id := range req.IDs {
		// 重复 SQL 会造成重复 DML，不能把重复选择视作用户要求执行两遍。
		if seen[id] {
			return nil, errors.New("不能重复选择同一事项")
		}
		_, item, _, err := s.Find(req.Version, id)
		// 精确按版本和稳定 ID 定位，不能按相似标题跨版本合并。
		if err != nil {
			return nil, err
		}
		if item.Kind != kind {
			// 其他业务类型即使具有相同字段，也不能进入 SQL 或日志操作。
			return nil, errors.New("所选事项类型与批量操作不一致")
		}
		seen[id] = true
		// map 和正文均使用副本，后台运行不会持有前台可变对象。
		items = append(items, Clone(item))
		// 保留用户选择顺序，SQL 特有的依赖排序由它自己的预览器完成。
	}
	// 全部 ID 都已通过校验，调用方才会收到可进入专用预览的完整选择。
	return items, nil
}

// SQLActive 检查单脚本和多脚本队列的整体占用状态。
// 无业务入参；调用方必须持有 Store.Mu，包含队列切换脚本的间隙。
// 返回：任一 SQL 任务仍在运行时为 true，防止中途编辑或插入另一批任务。
func (s *Store) SQLActive() bool {
	// 队列切换时当前 Batch 可能已结束，仍不能释放整个 SQL 工作区。
	return s.Batch != nil && s.Batch.Running || s.SQLQueue != nil && s.SQLQueue.Running
}
