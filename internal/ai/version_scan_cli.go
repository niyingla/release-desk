package aiservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"release-desk/internal/identity"
	"release-desk/internal/platform/process"
	"release-desk/internal/policy"
	"release-desk/internal/storage/fileio"
	"release-desk/internal/storage/workspace"
	"release-desk/web"
)

// ReleaseScanSkillPath 返回当前用户的版本扫描技能入口。
// 无业务入参；以 Windows 用户目录定位已安装的个人技能。
// 返回：技能文件绝对路径，不读取或返回 Codex 登录凭据。
func ReleaseScanSkillPath() string {
	// 用户目录来自系统，迁移电脑时无需硬编码用户名。
	return filepath.Join(os.Getenv("USERPROFILE"), ".codex", "skills", "release-desk-version-scan", "SKILL.md")
}

// InvokeVersionScan 使用已有 Codex CLI 登录态执行只读代码扫描。
// 参数 parent：只取消本轮扫描的上下文，不能复用全局取消句柄作为手动停止。
// 参数 exe：由本机工具发现逻辑确认的 CLI 可执行文件。
// 参数 version：当前版本与已有事项快照，供模型复用稳定 ID。
// 参数 config：固定的仓库和作者范围，不含数据库连接信息。
// 返回：结构化候选或进程、超时、技能、结果格式错误；不会自行保存版本。
func (s *Service) InvokeVersionScan(parent context.Context, exe string, version workspace.VersionFile, config workspace.Settings) (ScanResult, error) {
	s.SetVersionScanPhase("准备 Codex 与扫描协议")
	// 必须使用与独立对话相同的技能规则，技能缺失时明确提示安装路径。
	var result ScanResult
	if _, err := os.Stat(ReleaseScanSkillPath()); err != nil {
		return result, errors.New("版本扫描技能缺失：" + ReleaseScanSkillPath())
	}
	runDir := filepath.Join(s.Dir, "Codex扫描", identity.NewID())
	// 每次调用隔离结果路径，失败重试不能误读上一次输出。
	if err := os.MkdirAll(runDir, 0700); err != nil {
		return result, err
	}
	// Schema 内嵌到 EXE，业务记录不能改变输出结构或指定输出位置。
	schema, err := web.Assets.ReadFile("prompts/version-scan-schema.json")
	if err != nil {
		return result, err
	}
	schemaPath, outputPath := filepath.Join(runDir, "schema.json"), filepath.Join(runDir, "result.json")
	// 先完整写好 Schema，再启动外部进程；准备失败不消耗账号调用。
	if err = fileio.AtomicWrite(schemaPath, schema); err != nil {
		return result, err
	}
	prompt, err := VersionScanPrompt(version, config)
	if err != nil {
		return result, err
	}
	// CLI 只读源代码；软件自己负责合并保存，不授予模型文件写入能力。
	ctx, cancel := context.WithTimeout(parent, policy.CodexTimeoutMinutes*time.Minute)
	// 超时由应用控制，CLI 没有输出时也能释放任务占用。
	defer cancel()
	// 通过参数数组传值而非拼 shell，中文路径和版本备注都不会被解释为命令。
	cmd := process.Command(ctx, exe, "exec", "--sandbox", "read-only", "--ephemeral", "--color", "never", "--output-schema", schemaPath, "-o", outputPath, "-C", config.CodeRepository, "-")
	cmd.Stdin = strings.NewReader(prompt)
	// 复用当前登录与默认模型，不为用户选择另一个收费模型。
	// 不保存原始事件流，避免源码中的敏感值进入应用日志。
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	s.SetVersionScanPhase("Codex 正在读取代码与 Git")
	// 进度先回显再等待外部进程，模型慢响应时界面仍能说明正在做什么。
	if err = process.RunManagedCommand(cmd); err != nil {
		// 手动终止直接传递取消语义，不误报成登录或仓库故障。
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, fmt.Errorf("Codex 扫描未完成：%w；请检查登录及仓库路径", err)
	}
	// 只读进程退出后再消费候选，落盘由应用自己的受控入口负责。
	return ReadVersionScanResult(outputPath)
}

// ReadVersionScanResult 读取一次调用的有界结构化候选。
// 参数 path：本轮软件创建的 result.json 路径，不接受模型指定的任意路径。
// 返回：返回完整候选；过大、格式错误、未知字段或多份 JSON 时返回错误。
func ReadVersionScanResult(path string) (ScanResult, error) {
	var result ScanResult
	// 限制结果大小，异常输出不能占满工作台内存。
	info, err := os.Stat(path)
	if err != nil || info.Size() > policy.MaxVersionFileBytes {
		return result, errors.New("Codex 未生成有效大小的扫描结果")
	}
	// 独立路径不存在不能回退至旧目录，否则会把历史结果当成本次完成。
	content, err := os.ReadFile(path)
	if err != nil {
		return result, err
	}
	// CLI 输出仍是不可信候选，业务字段和 SQL 来源由保存前流程再次检查。
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	// 外层拒绝未声明字段；业务字段白名单在 prepareScanVersion 继续校验。
	if err = decoder.Decode(&result); err != nil {
		return result, errors.New("Codex 扫描结果格式无效")
	}
	// 拒绝多段结果，避免“先合法 JSON、后附加指令”的歧义输出。
	if err = decoder.Decode(new(any)); err != io.EOF {
		return result, errors.New("Codex 扫描结果包含多份 JSON")
	}
	// JSON 后只允许空白；拼接的第二份结果不具备明确归属。
	return result, nil
}

// VersionScanPrompt 组装当前版本的只读扫描边界与上下文。
// 参数 version：待扫描的现有版本快照，只作为模型读取的业务数据。
// 参数 config：仓库、作者配置，不传入数据库凭据或环境密钥。
// 返回：完整提示词；内嵌模板读取或上下文编码失败时返回错误。
func VersionScanPrompt(version workspace.VersionFile, config workspace.Settings) (string, error) {
	// 明确传入作者和仓库，不依赖模型猜测当前工作目录的用途。
	payload, err := json.MarshalIndent(map[string]any{"version": version, "code_repository": config.CodeRepository, "sql_repository": config.SQLRepository, "git_author": config.GitAuthor}, "", "  ")
	// 只序列化非凭据配置，数据库地址与密码不进入扫描上下文。
	if err != nil {
		return "", err
	}
	source, err := web.Assets.ReadFile("prompts/version-scan-prompt.txt")
	// 模板读取失败就停止，不让模型在缺少执行边界时自行猜测。
	if err != nil {
		return "", err
	}
	// 数据作为独立 JSON 附加；版本备注中的文字不改变工具与环境边界。
	prompt := strings.ReplaceAll(string(source), "__SCAN_SKILL__", ReleaseScanSkillPath())
	return strings.ReplaceAll(prompt, "__CONTEXT__", string(payload)), nil
}
