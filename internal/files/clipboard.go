package fileservice

import (
	"release-desk/internal/platform/desktop"
	"release-desk/internal/storage/workspace"
)

// CopyClipboard 处理单项复制请求。
// 参数 body：界面传入的 UTF-8 JSON 请求；已通过本机能力令牌鉴权，仍需进行业务字段校验。
// 返回：成功返回 nil 结果与 nil 错误；请求校验、记录定位或后续职责处理失败时返回错误，未返回业务成功凭证。
func (a *Service) CopyClipboard(body []byte) (any, error) {
	// 界面请求已经通过本机能力令牌鉴权。
	// 复制正文不进入应用日志。

	// 请求体只用于剪贴板，不写入记录、日志或网络服务。
	req, err := workspace.Decode[struct {
		Text string `json:"text"`
	}](body)

	if err != nil {
		return nil, err
	}
	return nil, desktop.WriteClipboard(req.Text)
}
