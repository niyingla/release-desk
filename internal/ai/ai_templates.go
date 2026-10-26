package aiservice

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"release-desk/internal/storage/fileio"
	"release-desk/internal/storage/workspace"
	"release-desk/web"
)

// Templates 生成 AI 学习模板和填写协议。
// 无业务入参；使用接收对象或本机启动上下文，不隐式触发外部写操作。
// 返回：返回模板目录绝对路径；模板或说明写入失败时返回错误，不打开失败目录。
func (s *Service) Templates() (string, error) {
	// 模板写入独立目录，不参与版本监听。
	// 示例不能自动成为用户的发布任务。
	// 字段定义与界面共用同一份元数据。

	// 模板与自动扫描目录分开，示例不会被误导入真实版本。
	folder := filepath.Join(s.Dir, "AI模板")
	version := NewTemplateVersion()

	if err := fileio.WriteJSON(filepath.Join(folder, "版本记录模板.json"), version); err != nil {
		return "", err
	}

	// 协议说明包含真实写入目录，AI 不需要猜测用户的本机路径。
	guide, err := web.Assets.ReadFile("prompts/ai-guide.txt")
	if err != nil {
		return "", err
	}

	content := strings.ReplaceAll(string(guide), "__VERSION_DIR__", filepath.Join(s.Dir, "版本文件"))
	if err = fileio.AtomicWrite(filepath.Join(folder, "AI填写说明.txt"), []byte(content)); err != nil {
		return "", err
	}
	return folder, nil
}

// NewTemplateVersion 创建包含五类事项的示例版本。
// 无业务入参；使用接收对象或本机启动上下文，不隐式触发外部写操作。
// 返回：返回完整且合法的五类事项示例，状态均未表示上线或验收成功。
func NewTemplateVersion() workspace.VersionFile {
	// 五种业务类型各提供一个合法示例。
	// 示例 ID 用于说明稳定标识的含义。
	// AI 应替换业务内容并保留已有项的 ID。
	// 示例值不代表真实发布版本。

	// 版本号为示例值，字段由统一元数据生成，避免模板与界面漂移。

	version := workspace.VersionFile{Format: "release-desk-version", Schema: 1, Version: workspace.Version{Name: "5.7.3", Status: "计划中"}, Items: []workspace.Item{}}

	for _, kind := range workspace.Kinds {
		data := map[string]string{}
		for _, field := range workspace.Fields[kind] {
			data[field[0]] = ""
		}

		// 必填项给出合法示例，SQL 示例仅为只读查询，不代表已经执行。
		data[workspace.Fields[kind][0][0]] = "示例，请替换"
		if kind == "sql" {
			data["phase"] = "初始化数据"
			data["sql"] = "SELECT 1;"
			data["side"] = "租户端"
			data["order"] = "1"
		}
		version.Items = append(version.Items, workspace.Item{ID: kind + "-stable-id", Kind: kind, Action: "新增", Status: "待处理", Data: data})
	}

	return version
}

// FindCodex 发现本机已安装的 Codex CLI。
// 无业务入参；使用接收对象或本机启动上下文，不隐式触发外部写操作。
// 返回：返回本机可执行文件路径；找不到时返回空字符串，不安装或登录程序。
func FindCodex() string {
	// 优先尊重用户已有 PATH 配置。
	// 检测过程不读取登录令牌。
	// 桌面安装目录可能随版本更新变化。

	// 优先使用用户已安装并加入 PATH 的 CLI，不安装或切换用户模型。
	if path, err := exec.LookPath("codex.exe"); err == nil {
		return path
	}

	root := filepath.Join(os.Getenv("LOCALAPPDATA"), "OpenAI", "Codex", "bin")
	files, _ := filepath.Glob(filepath.Join(root, "*", "codex.exe"))

	// 桌面应用会更换版本目录，按更新时间选择最近一次安装的可执行文件。

	sort.Slice(files, func(i, j int) bool {
		a, ea := os.Stat(files[i])
		b, eb := os.Stat(files[j])
		return ea == nil && (eb != nil || a.ModTime().After(b.ModTime()))
	})
	if len(files) > 0 {
		return files[0]
	}
	return ""
}

// VerificationPrompt 组装单项生产日志验收提示词。
// 参数 version：当前操作的版本标识或完整版本快照，用于限定事项归属。
// 参数 item：当前所选事项的独立快照，不接受模型修改执行或发布状态。
// 参数 start：日志查询开始时间，按北京时间解释，不代表发布已经成功。
// 参数 end：日志查询结束时间，必须晚于开始时间。
// 参数 config：已经校验的本机配置快照；调用期间保持一致。
// 参数 publication：可选的用户维护版本快照，仅提供发布背景，不能代替部署制品证据。
// 返回：返回包含代码定位、改动目标与参考线索的只读验证提示词，不含旧验证结论。
func VerificationPrompt(version string, item workspace.Item, start, end string, config workspace.Settings, publication ...workspace.Version) string {
	// 提示词来自独立资源，便于审阅只读边界。

	// 仓库与作者帮助 AI 定位目标改动，参考线索缺失不会限制其分析方法。
	// 旧结论不是本轮证据，且不得把数据库连接或登录凭据传入模型。
	data := map[string]any{"version": version, "action": item.Action, "service": item.Service, "change": workspace.VerificationData(item), "code_repository": config.CodeRepository, "sql_repository": config.SQLRepository, "git_author": config.GitAuthor, "start": start, "end": end, "timezone": "Asia/Shanghai"}
	if len(publication) > 0 && publication[0].Name == version {
		// 只传人工维护状态与日期，冗长备注和旧结论不能进入本轮推断。
		data["publication"] = map[string]string{"status": publication[0].Status, "date": publication[0].Date, "source": "用户维护的发布背景，不是已核实的部署 SHA"}
	}
	payload, _ := json.MarshalIndent(data, "", "  ")
	source, err := web.Assets.ReadFile("prompts/verification-prompt.txt")
	if err != nil {
		return fmt.Sprintf("验证模板不可用：%v", err)
	}

	// 技能路径从本机配置读取，便于迁移；业务字段仅作为 JSON 数据附加。
	text := strings.ReplaceAll(string(source), "__LOG_SKILL__", config.LogSkill)
	return strings.ReplaceAll(text, "__CONTEXT__", string(payload))
}
