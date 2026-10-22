// Package taskcenter 为统一任务历史提供查询和精确停止入口。
package taskcenter

import (
	"errors"

	"release-desk/internal/storage/workspace"
)

type Service struct{ Store *workspace.Store }

// Detail 查询任务快照，不重新执行 SQL 或日志查询。
// 参数 body：含历史任务精确 id 的 JSON 请求。
// 返回：完整任务及逐项明细；不存在时返回错误。
func (s *Service) Detail(body []byte) (any, error) {
	// 详情查询只读已保存记录，不要求对应业务版本仍然存在。
	// 版本被归档或删除后，任务证据依然按任务 ID 独立定位。
	// 返回值由仓库复制，HTTP 编码不能改变后台任务结果。
	// ID 只在已经加载的目录中查找，不拼接用户提供的任意文件路径。
	req, err := workspace.Decode[struct {
		ID string `json:"id"`
	}](body)
	if err != nil {
		return nil, err
	}
	if s.Store.TaskHistory == nil {
		return nil, errors.New("任务历史尚未初始化")
	}
	record, ok := s.Store.TaskHistory.Get(req.ID)
	// 历史查询只返回副本，不能激活已完成的工作或改变状态。
	if !ok {
		return nil, errors.New("任务记录不存在")
	}
	return record, nil
}

// Cancel 统一转交当前任务的取消请求。
// 参数 body：含本轮稳定 id 的 JSON，不按标题或版本模糊取消。
// 返回：请求接受返回 nil；旧任务、已完成任务或持久化错误时返回错误。
func (s *Service) Cancel(body []byte) (any, error) {
	// 接受请求只表示已发出停止信号，不能据此显示数据库已经回滚。
	// 请求经过通用鉴权后仍需解码与检查当前任务身份。
	req, err := workspace.Decode[struct {
		ID string `json:"id"`
	}](body)
	if err != nil {
		return nil, err
	}
	// 真正释放占用由各执行器收尾负责，取消接口不伪造完成结果。
	return nil, s.Store.StopTask(req.ID)
}
