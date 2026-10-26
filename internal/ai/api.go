package aiservice

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"release-desk/internal/storage/workspace"
)

// AITemplate 生成模板并返回可复制的当前版本扫描指令。
// 参数 body：当前版本 name，可为空，此时指令按当前发布分支推导。
// 返回：模板、协议绝对路径与可复制指令；文件创建失败时返回错误。
func (a *Service) AITemplate(body []byte) (any, error) {
	// 该操作只生成说明与示例文件。
	// 模板不放到自动导入目录。
	// 目录打开与生成分开，界面能显示实际文件和可复制指令。
	req, e := workspace.Decode[workspace.Version](body)
	if e != nil {
		return nil, e
	}
	// 当前选择只用于组装指令，不修改模板协议中的合法示例。
	folder, e := a.Templates()
	if e != nil {
		return nil, e
	}
	// 未选择版本时让独立对话按当前发布分支推导，不固定示例版本号。
	name := req.Name
	if name == "" {
		name = "当前发布版本"
	}
	prompt := fmt.Sprintf("使用 $release-desk-version-scan 扫描 %s，只整理设置中作者的改动。数据目录：%s。读取 settings.json 的仓库与作者，保留人工状态与历史，将有来源证据的五类事项增量写入版本文件。", name, a.Store.Dir)
	return map[string]string{"folder": folder, "template": filepath.Join(folder, "版本记录模板.json"), "guide": filepath.Join(folder, "AI填写说明.txt"), "prompt": prompt}, nil
}

// AIVerify 启动选中事项的 AI 自主只读验证。
// 参数 body：界面传入的 UTF-8 JSON 请求；已通过本机能力令牌鉴权，仍需进行业务字段校验。
// 返回：成功返回 nil 结果与 nil 错误；请求校验、记录定位或后续职责处理失败时返回错误，未返回业务成功凭证。
func (a *Service) AIVerify(body []byte) (any, error) {
	// 请求只包含版本、事项和查询时间窗。
	// 实际查询与结果校验交由 Codex 模块。

	req, e := workspace.Decode[VerifyRequest](body)

	if e != nil {
		return nil, e
	}
	// 单项入口统一入队，后续独立事项可以并发，仍保持扫描与 SQL 的互斥。
	exe := FindCodex()
	if exe == "" {
		return nil, errors.New("未找到 Codex CLI，请先安装并登录")
	}
	return a.EnqueueVerification(req, func(ctx context.Context, request VerifyRequest, item workspace.Item, config workspace.Settings) (workspace.VerifyResult, error) {
		// 每项仍使用同一只读验证协议，结果按自己的版本、事项和窗口保存。
		return a.InvokeCodex(ctx, exe, request, item, config)
	})
}

// AIPrompt 返回可复制到独立对话的验证提示词。
// 参数 body：界面传入的 UTF-8 JSON 请求；已通过本机能力令牌鉴权，仍需进行业务字段校验。
// 返回：返回包含代码目标和可选线索的自主验证提示词；记录不存在时返回错误。
func (a *Service) AIPrompt(body []byte) (any, error) {
	// 根据选中事项生成独立对话提示词。
	// 记录归属仍由版本和稳定 ID 确定。
	// 读取时保持快照一致，避免拼入一半新旧内容。
	// 查询窗口由用户场景提供，可在新对话中调整。
	// 该入口只返回文本，不启动 Codex。

	req, e := workspace.Decode[VerifyRequest](body)
	if e != nil {
		return nil, e
	}

	s := a.Store
	s.Mu.Lock()

	defer s.Mu.Unlock()
	version, item, _, e := s.Find(req.Version, req.ID)

	if e != nil {
		return nil, e
	}
	return VerificationPrompt(req.Version, item, req.Start, req.End, s.Config(), version.Version), nil
}
