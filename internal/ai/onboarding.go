package aiservice

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"release-desk/internal/storage/fileio"
	"release-desk/internal/storage/workspace"
	"release-desk/web"
)

// AIOnboarding 生成当前电脑可交给独立 AI 的接入指引与可复制指令。
// 参数 body：可选版本名，仅用于限定后续扫描目标，不接收任何连接凭据。
// 返回：指引、内置技能和模板位置；生成失败返回错误，不修改设置或发布记录。
func (s *Service) AIOnboarding(body []byte) (any, error) {
	// 与版本编辑共用名称规则，避免复制出无法被软件读取的目标文件说明。
	req, err := workspace.Decode[workspace.Version](body)
	if err != nil || req.Name != "" && !workspace.ValidName.MatchString(req.Name) {
		return nil, fmt.Errorf("接入指引的版本名称无效")
	}
	// 准备材料只写软件管理的 AI模板，不导入示例，也不安装个人技能。
	folder, err := s.Templates()
	if err != nil {
		return nil, err
	}
	paths, err := s.SyncBuiltinSkills()
	// 导出失败不能继续交付引用旧技能或缺失文件的接入指令。
	if err != nil {
		return nil, err
	}
	// 指引模板内嵌随软件更新，不从某个开发者电脑复制旧文件。
	source, err := web.Assets.ReadFile("prompts/ai-onboarding.txt")
	if err != nil {
		return nil, err
	}
	protocolPath := filepath.Join(folder, "AI填写说明.txt")
	// 指引和软件协议来自同次模板生成，避免复制过时的字段清单。
	protocol, err := os.ReadFile(protocolPath)
	if err != nil {
		return nil, err
	}
	// 配置快照来自显式非敏感字段，未来新增凭据字段也不会自动进入复制文本。
	preview, err := onboardingSettingsPreview(s.Config())
	if err != nil {
		return nil, err
	}
	guidePath := filepath.Join(folder, "AI接入指引.txt")
	// 模板留在学习材料目录，不会因为接入操作创建一个示例业务版本。
	templatePath := filepath.Join(folder, "版本记录模板.json")
	// 每个目录都来自当前 Store，支持用户自定义 data-dir，不固定用户名或盘符。
	replacer := strings.NewReplacer("__DATA_DIR__", s.Dir, "__SETTINGS_FILE__", filepath.Join(s.Dir, "settings.json"), "__VERSION_DIR__", filepath.Join(s.Dir, "版本文件"), "__SCAN_SKILL__", paths["release-desk-version-scan"], "__LOG_SKILL__", paths["bamboo-tls-query"], "__ENV_SKILL__", paths["release-desk-env-routing"], "__PROTOCOL_FILE__", protocolPath, "__TEMPLATE_FILE__", templatePath, "__SETTINGS_PREVIEW__", preview, "__FILE_PROTOCOL__", string(protocol))
	if err = fileio.AtomicWrite(guidePath, []byte(replacer.Replace(string(source)))); err != nil {
		// 原子替换失败时保留已有材料，不返回一个未成功写入的文件路径。
		return nil, err
	}
	// 指令只让 AI 读取本机说明；点击生成或复制都不会启动模型或进行业务调用。
	name := req.Name
	// 尚未选择版本时让 AI 核实当前发布分支，不能默认使用开发者的示例版本。
	if name == "" {
		name = "从当前本地发布分支确认的版本"
	}
	// 中文引用保留 Windows 路径原文，复制后无需手动撤销 Go 字符串转义。
	prompt := fmt.Sprintf("请接入本机 Release Desk：先读取「%s」，再按指引核对并填写软件非敏感设置，扫描「%s」的真实改动并增量写入六类发布记录。数据目录为「%s」；扫描、日志查询及环境路由技能已由软件内置并导出，无需单独安装个人技能。保留人工状态和历史，不读取凭据，不执行 SQL、调用接口、查询生产、部署或推送；信息缺失时只询问必要内容，完成后给出写入结果及待确认项。", guidePath, name, s.Dir)
	// 返回学习材料而非执行结果，生成成功不代表设置已经保存或版本已经扫描。
	return map[string]string{"prompt": prompt, "guide": guidePath, "skill": paths["release-desk-version-scan"], "log_skill": paths["bamboo-tls-query"], "routing_skill": paths["release-desk-env-routing"], "template": templatePath, "folder": folder}, nil
}

// onboardingSettingsPreview 仅序列化接入配置需要的非敏感设置。
// 参数 config：当前保存的软件设置快照，不能传入连接文件或用户登录对象。
// 返回：便于 AI 阅读的 JSON；编码失败时不产生不完整指引。
func onboardingSettingsPreview(config workspace.Settings) (string, error) {
	// 使用显式字段边界，不将整个共享存储或未来新增字段直接交给外部对话。
	data := map[string]any{"code_repository": config.CodeRepository, "sql_repository": config.SQLRepository, "git_author": config.GitAuthor, "log_skill": config.LogSkill, "verification_concurrency": config.VerificationConcurrency, "verification_log_domains": config.VerificationLogDomains, "initialization_base_urls": config.InitializationBaseURLs, "scope_rules": config.ScopeRules}
	// JSON 保留路径和字符串的转义语义，不把字段值拼成可执行命令。
	content, err := json.MarshalIndent(data, "", "  ")
	// 编码失败时不以空快照冒充本机设置，调用方停止生成接入材料。
	if err != nil {
		return "", err
	}
	return string(content), nil
}
