package workspace

import (
	"encoding/json"
	"errors"
)

// Decode 解码本机接口的 JSON 请求。
// 参数 body：界面传入的 UTF-8 JSON 请求；已通过本机能力令牌鉴权，仍需进行业务字段校验。
// 返回：返回目标类型 T 的解码对象及 JSON 结构错误；不完成业务必填项校验。
func Decode[T any](body []byte) (T, error) {
	// 解码对象仅来自已完成能力令牌鉴权的本机请求。
	// JSON 解码只确认结构，必填项和状态边界由所属业务处理器继续校验。
	// 调用方负责校验业务必填项和取值范围。

	var v T
	e := json.Unmarshal(body, &v)
	return v, e
}

type ItemRequest struct {
	Version string `json:"version"`
	Item    Item   `json:"item"`
	ID      string `json:"id"`
}

// EnsureIdle 拒绝与运行任务冲突的记录修改。
// 无业务入参；使用接收对象或本机启动上下文，不隐式触发外部写操作。
// 返回：成功返回 nil 结果与 nil 错误；请求校验、记录定位或后续职责处理失败时返回错误，未返回业务成功凭证。
func (a *Store) EnsureIdle() error {
	// 运行中的 SQL 和验证任务使用稳定快照。
	// 修改或恢复不能让同一任务前后读取不同业务内容。

	if a.SQLActive() || a.AIBusy {
		return errors.New("请等待 SQL 执行或 Codex 验证完成后再修改记录")
	}
	return nil
}
