package aiservice

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	gitrepo "release-desk/internal/git/repository"
	"release-desk/internal/storage/workspace"
)

// RefreshItemCode 将用户确认的一条代码更新加入扫描通道，不触发生产验证。
// 参数 body：精确 version、id，不接受代码路径、远端拉取选项或命令。
// 返回：后台已接收返回 nil；工具、目标或占用校验失败时返回错误。
func (s *Service) RefreshItemCode(body []byte) (any, error) {
	// 用类型化请求限定更新对象，前端不能提交任意 Git 命令。
	req, err := workspace.Decode[struct {
		Version string `json:"version"`
		ID      string `json:"id"`
	}](body)
	// 请求损坏时立即返回，不能以空 ID 意外扫描整个版本。
	if err != nil {
		return nil, err
	}
	// 只发现已安装工具，不读取或复制用户登录凭据。
	exe := FindCodex()
	// CLI 缺失应在任务登记前提示，避免留下无法处理的运行记录。
	if exe == "" {
		return nil, errors.New("未找到 Codex CLI，请先安装并登录")
	}
	// 本地引用读取在后台执行；取消使用本轮扫描上下文，不连接 Git 远端。
	err = s.StartItemCodeRefresh(req.Version, req.ID, func(ctx context.Context, v workspace.VersionFile, config workspace.Settings) (ScanResult, error) {
		s.SetVersionScanPhase("读取指定版本本地 Git 引用")
		// 仓库来自启动时的配置快照；事项中的路径文本没有命令执行权限。
		revision, err := gitrepo.New(config.CodeRepository, config.GitAuthor).VersionRevision(ctx, v.Version.Name)
		// 引用缺失就提示，不借用当前其他分支的工作区实现继续生成。
		if err != nil {
			return ScanResult{}, err
		}
		// 引用固定为 SHA 后才启动模型，避免扫描期间新增提交导致判断范围漂移。
		return s.invokeScan(ctx, exe, v, config, func(snapshot workspace.VersionFile, settings workspace.Settings) (string, error) {
			return itemCodeRefreshPrompt(snapshot, settings, revision)
		})
	})
	// 入口日志只记录范围，不输出 Git 地址、源代码或原验证证据。
	if err == nil {
		slog.Info("单项代码更新已接收", "version", req.Version, "item_id", req.ID, "source", "local_git_refs")
	}
	return nil, err
}

// StartItemCodeRefresh 复用扫描生命周期，同时强制候选只能修改选中的验证项。
// 参数 version：已保存版本名；参数 id：已有验证事项的稳定 ID，不能为空。
// 参数 runner：只读模型适配器，返回内容还须经过协议和范围检查。
// 返回：后台接收成功返回 nil；越界候选在保存前整批拒绝，旧内容和历史不变。
func (s *Service) StartItemCodeRefresh(version, id string, runner ScanRunner) error {
	// 空 ID 不能退化为整版扫描，防止旧弹窗或错误请求扩大更新范围。
	if strings.TrimSpace(id) == "" {
		return errors.New("请选择需要更新代码的事项")
	}
	// 占用与目标定位由统一启动器原子完成，连续点击不能开出重复模型进程。
	return s.startScan(version, id, func(ctx context.Context, v workspace.VersionFile, settings workspace.Settings) (ScanResult, error) {
		result, err := runner(ctx, v, settings)
		// 取消或模型故障原样交给扫描收尾，不拼接半份候选。
		if err != nil {
			return result, err
		}
		// 不能只依赖提示词；未知 ID、多项结果或其他类型都没有写入权限。
		if len(result.Items) != 1 || result.Items[0].ID != id || result.Items[0].Kind != "verify" {
			return ScanResult{}, errors.New("单项代码更新返回了其他事项或空结果，已有内容未修改")
		}
		return result, nil
	})
}

// itemCodeRefreshPrompt 为当前事项补充固定提交与严格的单项范围。
// 参数 version：仅含当前验证项的启动快照；参数 config：仓库与作者配置。
// 参数 revision：后台已读取的精确发布引用与 SHA，不根据工作区 HEAD 猜测。
// 返回：完整只读提示词；模板或编码失败时返回错误，不启动不完整的模型请求。
func itemCodeRefreshPrompt(version workspace.VersionFile, config workspace.Settings, revision gitrepo.VersionRevision) (string, error) {
	// 复用现有核心目标、作者归因与自动/手动分类约束，不维护第二套标准。
	// 上下文清理和发布基线规则由扫描入口统一完成，历史只用于需求定位。
	base, err := VersionScanPrompt(version, config)
	if err != nil {
		return "", err
	}
	// 固定引用以结构化数据附加，避免分支名被当作附加指令。
	payload, err := json.Marshal(revision)
	if err != nil {
		return "", err
	}
	// 沿用扫描的可观察性及核心目标标准，但不新增、不拆分、不删掉选中事项。
	return base + "\n本轮是单项代码更新，更新范围以本段为准：只返回上下文中这一条 verify，保持相同 ID。禁止整理其他事项或新增其他类型记录。固定目标引用及提交为：" + string(payload) +
		"\n必须先核对实际已发布基线与该 SHA 的业务净差异，再把来源提交前后变化作为开发演进线索；上次扫描和同版本未上线的第一版不能作为发布基线。新需求在旧版不存在时按从无到有更新，不伪造线上旧 bug。核对后续覆盖、修复或撤销；当前工作区或其他分支不是目标。更新 change、steps、expected、keywords、source_commit、source_path 和来源证据，并重新评估自动/手动验证。核心业务目标及用户明确范围优先，不能根据注释自行扩大验收。没有代码变化也返回该项和来源说明；已撤销则说明现状和人工确认方式。summary 仅总结此项，不声称重新扫描了整版。不得查询生产日志或生成实际通过/失败结论。", nil
}
