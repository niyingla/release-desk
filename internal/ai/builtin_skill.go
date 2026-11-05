package aiservice

import (
	"errors"
	"path/filepath"
	"strings"

	"release-desk/internal/identity"
	"release-desk/internal/storage/fileio"
	"release-desk/internal/versioninfo"
	"release-desk/web"
)

// builtinSkillContent 读取当前 EXE 随包分发的技能规则。
// 参数 asset：本软件代码中固定的 prompts 资源文件名，不接受用户业务文本指定路径。
// 返回：UTF-8 技能内容；资源缺失或为空时拒绝生成不完整提示词。
func builtinSkillContent(asset string) (string, error) {
	// 每次从本次构建资源读取，升级程序后不会继续沿用旧电脑上的个人规则。
	content, err := web.Assets.ReadFile("prompts/" + asset)
	if err != nil || strings.TrimSpace(string(content)) == "" {
		return "", errors.New("软件内置技能不可用，请重新下载完整安装包")
	}
	// 技能正文保持单一来源，软件扫描和独立对话导出的规则不会分别维护。
	return string(content), nil
}

// SyncBuiltinSkills 刷新软件管理的技能副本与版本标识，供独立 AI 对话读取。
// 无业务入参；仅写入当前数据目录的 AI模板，不安装或覆盖个人 Codex 技能。
// 返回：技能名到实际文件位置的映射；资源、版本或原子写入失败时返回错误。
func (s *Service) SyncBuiltinSkills() (map[string]string, error) {
	// 版本标识取本次 EXE 内嵌目录，不把工作台里的业务发布版本当作软件版本。
	info, err := versioninfo.Read()
	if err != nil {
		return nil, err
	}
	// 发布扫描、日志查询与环境路由来自同一软件版本，不在用户个人目录安装文件。
	assets := map[string]string{"release-desk-version-scan": "release-scan-skill.txt", "bamboo-tls-query": "log-query-skill.txt", "release-desk-env-routing": "env-routing-skill.txt"}
	paths := map[string]string{}
	for name, asset := range assets {
		// 每个技能都有自己的摘要，部分副本写入失败不会把旧副本标记成新版本。
		// 副本只是给独立对话读取；软件调用仍直接读取 EXE，避免旧副本反向覆盖内置规则。
		content, err := builtinSkillContent(asset)
		if err != nil {
			return nil, err
		}
		folder := filepath.Join(s.Dir, "AI模板", "skills", name)
		path := filepath.Join(folder, "SKILL.md")
		// 先完成原子替换，再发布对应版本标识，失败时不谎报所有技能已经更新。
		if err = fileio.AtomicWrite(path, []byte(content)); err != nil {
			return nil, err
		}
		// 摘要仅表示副本来源，不包含凭据，也不代表业务扫描或验收已经完成。
		metadata := map[string]string{"software_version": info.Current, "sha256": identity.Digest(content)}
		if err = fileio.WriteJSON(filepath.Join(folder, "version.json"), metadata); err != nil {
			return nil, err
		}
		// 只有技能与版本标识都写入成功，才把路径交给接入指引引用。
		paths[name] = path
	}
	// 返回实际文件位置，接入指引无需根据用户名猜测目录。
	return paths, nil
}

// verificationSkillRules 组装内置日志和环境规则，不读取个人技能目录。
// 参数 configPath：仅兼容旧调用签名，当前不读取或引用个人技能目录。
// 返回：完整规则、补充入口说明及资源错误；不查询日志、不读取凭据或安装个人技能。
func verificationSkillRules(configPath string) (string, string, error) {
	// 日志查询和环境路由随同一 EXE 发布，缺失规则不能凭空生成验证结论。
	logs, err := builtinSkillContent("log-query-skill.txt")
	if err != nil {
		return "", "", err
	}
	routing, err := builtinSkillContent("env-routing-skill.txt")
	// 环境规则缺失时不根据工具名称猜测测试或生产，整轮提示词生成失败。
	if err != nil {
		return "", "", err
	}
	// 旧配置里的路径不再提供给模型，规则与连接都由当前软件入口负责。
	note := "仅使用本次提供的内置技能，无需配置个人技能目录；已有内置查询入口时优先使用，否则发现实际可用 MCP。"
	// 凭据配置只在设置页完成，规则正文不能授权模型读取密钥文件。
	return logs + "\n\n" + routing, note, nil
}
