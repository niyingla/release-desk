package logquery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/volcengine/volc-sdk-golang/service/tls"
	"release-desk/internal/platform/secrets"
	"release-desk/internal/policy"
	"release-desk/internal/storage/fileio"
)

// QueryScope 固定单轮生产查询范围；整个对象加密，不向模型公开密钥。
type QueryScope struct {
	Config         Config   `json:"config"`
	Environment    string   `json:"environment"`
	StartMS        int64    `json:"start_ms"`
	EndMS          int64    `json:"end_ms"`
	ExpiresMS      int64    `json:"expires_ms"`
	AllowedDomains []string `json:"allowed_domains"`
}

// QueryRequest 只允许调整条件和采样量，不能改变环境、Topic 或时间。
type QueryRequest struct {
	Action string `json:"action"`
	Query  string `json:"query"`
	Limit  int    `json:"limit"`
}

// PrepareAccess 创建本轮加密查询入口，未配置直连时保留已有 MCP 使用方式。
// 参数 dir：当前数据目录；参数 runDir：软件创建的本轮隔离目录。
// 参数 start：用户确认的北京时间起点；参数 end：用户确认的北京时间终点。
// 参数 allowed：本轮固定的精确日志主机，不允许模型扩大范围。
// 返回：访问文件和公开说明；没有直连凭据时均为空，配置异常则停止验证。
func PrepareAccess(dir, runDir, start, end string, allowed []string) (string, string, error) {
	// 本轮连接从软件受控目录加载，不从版本说明或模型文本中读取凭据路径。
	// 没有直连配置与配置损坏分别处理，后者必须提示修复，不能静默换数据源。
	// 固定生产资源后再创建访问入口，缺少映射不会产生可扩大环境的空白授权。
	// 时间始终按北京时间解析，避免同事电脑的本地时区影响生产检索范围。
	// 连接、项目、Topic 和窗口一起快照，后续修改设置不影响已经运行的任务。
	// 文件有效期只覆盖一次模型调用，任务结束时还会由调用方主动撤销。
	// 整个访问对象使用当前用户加密，文件位置本身不能用来取得密钥原文。
	// 模型只调用专用子命令，没有导出配置或任意 HTTP 请求的操作入口。
	// 指令中的路径按 PowerShell 单引号规则转义，不执行路径文本中的元字符。
	// 调整关键词是允许的，环境、主题和时间窗不随模型重试发生改变。
	config, err := Load(dir)
	if err != nil {
		return "", "", err
	}
	// 没有直连配置仍可使用已有 MCP，不再依赖个人技能文件。
	if config.AccessKey == "" && config.SecretKey == "" {
		return "", "", nil
	}
	if err = Validate(config, allowed); err != nil {
		return "", "", err
	}
	// 生产资源缺失必须停止，不借用测试或 UAT 项目完成验证。
	if config.Projects["prod"] == "" || config.Topics["prod"] == "" {
		return "", "", errors.New("请先配置生产后端日志的项目与 Topic")
	}
	zone := time.FixedZone("Asia/Shanghai", 8*60*60)
	from, err := time.ParseInLocation("2006-01-02T15:04", start, zone)
	if err != nil {
		return "", "", errors.New("日志访问开始时间无效")
	}
	to, err := time.ParseInLocation("2006-01-02T15:04", end, zone)
	if err != nil || !to.After(from) {
		return "", "", errors.New("日志访问时间范围无效")
	}
	// 固定连接、资源和窗口，运行中编辑设置不能改变当前已授权任务。
	scope := QueryScope{Config: config, Environment: "prod", StartMS: from.UnixMilli(), EndMS: to.UnixMilli(), ExpiresMS: time.Now().Add(policy.CodexTimeoutMinutes * time.Minute).UnixMilli(), AllowedDomains: allowed}
	raw, err := json.Marshal(scope)
	if err != nil {
		return "", "", errors.New("日志访问范围编码失败")
	}
	ciphertext, err := secrets.Protect(string(raw), false)
	if err != nil {
		return "", "", errors.New("日志访问范围加密失败")
	}
	path := filepath.Join(runDir, "log-access.json")
	// 磁盘只有当前用户可解密的密文，模型只获得可执行入口和文件位置。
	if err = fileio.WriteJSON(path, map[string]string{"ciphertext": ciphertext}); err != nil {
		return "", "", errors.New("日志访问范围准备失败")
	}
	executable, err := os.Executable()
	if err != nil {
		_ = os.Remove(path)
		return "", "", errors.New("无法定位内置日志查询程序")
	}
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
	// PowerShell 路径按单引号转义，中文、空格或引号不形成额外命令。
	// Windows PowerShell 默认管道编码可能丢失中文，当前查询进程显式使用无 BOM 的 UTF-8。
	command := `[Console]::OutputEncoding = $OutputEncoding = [System.Text.UTF8Encoding]::new($false); '{"action":"search","query":"替换为真实日志检索条件","limit":20}' | & ` + quote(executable) + " --query-logs --log-scope " + quote(path)
	return path, fmt.Sprintf("本轮已配置软件内置 TLS，只读查询必须优先使用以下 PowerShell 调用。不得读取个人技能、密钥文件或加密访问文件正文，不自行改用其他项目或 Topic：\n%s\n可将 action 改为 describe 查看真实资源和固定窗口。软件固定生产后端 Topic 及用户确认的北京时间 %s 至 %s。请求仅支持 action/query/limit，不接受时间、环境、Topic 参数；最多 %d 条采样。区分 hit_count、sample_count 和 result_status，查询失败不能当成零命中。结果已脱敏，调整关键词仍保持同一窗口。", command, start, end, policy.MaxLogSampleRows), nil
}

// loadScope 校验短期文件仍存在、可解密且在当前任务有效期内。
// 参数 path：软件生成的加密访问文件位置，不从查询 JSON 接受替代路径。
// 返回：固定范围或安全错误；失败不回退全局连接或任意时间窗。
func loadScope(path string) (QueryScope, error) {
	// 访问文件被撤销后立即拒绝新查询，不再尝试全局连接或其他历史授权。
	// 先检查大小再读取，意外文件不能被当作无限制的访问上下文。
	// 文件必须由当前用户解密，普通连接文件与业务 JSON 都不能充当查询授权。
	// 有效期与查询窗口含义不同，历史时间窗仍可查询，但授权到期后不可重用。
	// 当前验证只读生产后端日志，其他环境必须由软件的明确流程重新授权。
	var scope QueryScope
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > policy.MaxLogScopeBytes {
		return scope, errors.New("日志访问已结束或文件无效，请从软件重新发起验证")
	}
	raw, err := os.ReadFile(path)
	var envelope struct {
		Ciphertext string `json:"ciphertext"`
	}
	// 损坏文件不能被当作没有约束的新请求，原始错误也不能回显密文。
	if err != nil || json.Unmarshal(raw, &envelope) != nil || envelope.Ciphertext == "" {
		return scope, errors.New("日志访问文件无效")
	}
	plain, err := secrets.Protect(envelope.Ciphertext, true)
	if err != nil || json.Unmarshal([]byte(plain), &scope) != nil {
		return scope, errors.New("日志访问文件无法解密")
	}
	// 当前入口仅用于生产验收；到期、逆向时间或其他环境均拒绝。
	if scope.ExpiresMS <= time.Now().UnixMilli() || scope.StartMS >= scope.EndMS || scope.Environment != "prod" {
		return scope, errors.New("日志访问范围过期或无效")
	}
	return scope, Validate(scope.Config, scope.AllowedDomains)
}

// RunCLI 执行内置只读查询子命令，不启动桌面或恢复业务记录。
// 参数 scopePath：本轮加密访问文件；参数 input：只接受有界的单份请求 JSON。
// 参数 output：只输出脱敏结果，不保存原始日志和凭据。
// 返回：协议、范围或云端读取错误，均不包含 SDK 原始响应。
func RunCLI(scopePath string, input io.Reader, output io.Writer) error {
	// 子命令不打开桌面数据工作区，避免影响主窗口单实例及任务恢复逻辑。
	// 只消费一份 JSON；未知字段不能成为扩大时间窗或指定其他 Topic 的入口。
	// 读取失败只返回安全错误，不将 SDK 的原始认证信息写入 stdout 或 stderr。
	// 输出属于本轮取证结果，不在软件目录另存一份原始生产日志。
	scope, err := loadScope(scopePath)
	if err != nil {
		return err
	}
	var request QueryRequest
	// 先检查完整请求大小，不能通过大量尾部空白把第二份 JSON 藏在读取上限之外。
	body, err := io.ReadAll(io.LimitReader(input, policy.MaxLogRequestBytes+1))
	if err != nil || len(body) > policy.MaxLogRequestBytes {
		return errors.New("日志查询请求超出大小限制")
	}
	// 拒绝未知字段和多份 JSON，模型无法通过旧参数扩大当前授权范围。
	decoder := json.NewDecoder(bytes.NewReader(body))
	// 查询正文只作为数据解析，不提供文件路径、HTTP 方法或其他执行能力。
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("日志查询仅接受 action、query、limit 的单份 JSON")
	}
	ctx, cancel := context.WithTimeout(context.Background(), policy.LogRequestSeconds*time.Second)
	defer cancel()
	// 子进程仅运行一次有界读取，主任务停止时仍由现有进程树管理统一结束。
	result, err := executeQuery(newTLSClient(ctx, scope.Config), scope, request)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(result)
}

// executeQuery 仅在软件固定环境、Topic 与时间窗中检索。
// 参数 client：当前官方客户端；参数 scope：软件固定的生产日志访问快照。
// 参数 request：查询条件与采样量，不允许写操作或任意资源参数。
// 返回：脱敏的真实资源和采样状态；资源或权限异常时明确报错。
func executeQuery(client tls.Client, scope QueryScope, request QueryRequest) (any, error) {
	// 入口只暴露两种只读能力，不把官方 SDK 的其他管理操作交给模型选择。
	// 默认采样量与最大上限分开控制，模型需要更多证据时应调整业务检索条件。
	// 项目归属校验发生在读取日志正文之前，缓存 ID 不能证明账号资源未变化。
	// describe 只帮助核对当前已固定资源，不能返回任意项目的主题清单。
	// 空查询不自动变成全量扫描，避免缺少关键词时无意采集大量业务日志。
	// 请求时间使用固定毫秒值，不依赖 SDK 或云端默认窗口。
	// 必须区分总命中、返回样本和查询完成状态，截断样本不足以证明全部正常。
	// 日志与分析结果走同一脱敏路径，不允许分析模式绕过凭据保护。
	// 返回结果保留真实资源和时间信息，便于后续复核本轮究竟查询了什么。
	if request.Action != "" && request.Action != "search" && request.Action != "describe" {
		return nil, errors.New("内置日志入口仅支持 search 或 describe，其他操作已拒绝")
	}
	if request.Limit < 0 || request.Limit > policy.MaxLogSampleRows || len(request.Query) > policy.MaxLogRequestBytes {
		return nil, errors.New("日志检索条件或采样上限超出范围")
	}
	// 真实云端归属优先于缓存映射，禁止跨项目查询同名 Topic。
	topic, err := checkTopic(client, scope.Config, scope.Environment)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"environment": scope.Environment, "project_id": topic.ProjectID, "topic_id": topic.TopicID, "topic_name": topic.TopicName, "start_ms": scope.StartMS, "end_ms": scope.EndMS}
	if request.Action == "describe" {
		// 资源名称也只返回脱敏副本，不让云端自定义元数据绕过相同保护。
		return redactValue(result, scope.Config), nil
	}
	if strings.TrimSpace(request.Query) == "" {
		return nil, errors.New("请填写实际日志检索条件")
	}
	limit := request.Limit
	if limit == 0 {
		limit = policy.DefaultLogSampleRows
	}
	// 毫秒时间由软件产生，准确总量和完成开关避免将默认窗口或未完成样本当结论。
	complete, accurate := true, true
	response, err := client.SearchLogsV2(&tls.SearchLogsRequest{TopicID: topic.TopicID, Query: request.Query, StartTime: scope.StartMS, EndTime: scope.EndMS, Limit: limit, Sort: "desc", MustComplete: &complete, AccurateQuery: &accurate})
	if err != nil {
		return nil, safeError(err)
	}
	result["result_status"], result["hit_count"], result["sample_count"] = response.Status, response.HitCount, len(response.Logs)
	result["list_over"], result["analysis"], result["analysis_result"] = response.ListOver, response.Analysis, response.AnalysisResult
	result["logs"] = response.Logs
	// 先转为统一 JSON 树再递归脱敏，SDK 新增结果字段也不能跳过同一边界。
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, errors.New("日志结果编码失败")
	}
	var plain any
	if json.Unmarshal(raw, &plain) != nil {
		return nil, errors.New("日志结果解析失败")
	}
	return redactValue(plain, scope.Config), nil
}

var bearerPattern = regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9._~+/-]+`)
var tokenPattern = regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)
var phonePattern = regexp.MustCompile(`\b1[3-9]\d{9}\b`)
var sensitivePattern = regexp.MustCompile(`(?i)(password|passwd|secret[_-]?key|access[_-]?key|token|authorization|cookie)["\s]*[:=]["\s]*[^\s,;"}]+`)

// redactValue 屏蔽样本中的凭据和常见个人信息，限制单段正文大小。
// 参数 value：通用 JSON 日志或分析值；参数 config：只用于去除本轮 AK/SK 的完整匹配。
// 返回：可供模型使用的脱敏副本，不改变命中总量、采样数量或时间范围。
func redactValue(value any, config Config) any {
	// 同时处理嵌套结构与字符串日志，不能假设日志始终使用一种固定编码。
	// 结构化敏感字段直接屏蔽整值，数组中的每个样本也使用相同规则。
	// 当前 AK/SK 的完整值优先移除，其他日志字段中的同类凭据再通过规则处理。
	// Bearer 和 JWT 单独处理，带空格或复合结构的 Token 不能只截掉一部分。
	// 手机号码不属于业务验证必须展示的原文，保留必要的非敏感业务关联信息即可。
	// 截断只作用于样本正文，不改变云端返回的命中量或查询完成状态。
	// 截断提示明确展示，模型不能将有限采样认作整段完整生产原文。
	// 处理后的树仅用于本次输出，原始 SDK 响应不会写入发布记录或任务文件。
	switch current := value.(type) {
	case map[string]any:
		for key, child := range current {
			// 结构化凭据字段整值移除，不依赖正文正则是否能识别嵌套内容。
			normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(key))
			if normalized == "token" || normalized == "password" || normalized == "authorization" || normalized == "cookie" || normalized == "secretkey" || normalized == "accesskey" {
				current[key] = "[已脱敏]"
			} else {
				current[key] = redactValue(child, config)
			}
		}
	case []any:
		for index, child := range current {
			current[index] = redactValue(child, config)
		}
	case string:
		// 完整密钥先替换再截断，防止长度边界残留半段凭据。
		for _, secret := range []string{config.AccessKey, config.SecretKey} {
			if secret != "" {
				current = strings.ReplaceAll(current, secret, "[已脱敏]")
			}
		}
		current = bearerPattern.ReplaceAllString(current, "Bearer [已脱敏]")
		current = tokenPattern.ReplaceAllString(current, "[Token已脱敏]")
		current = sensitivePattern.ReplaceAllString(current, "$1=[已脱敏]")
		current = phonePattern.ReplaceAllString(current, "[手机号已脱敏]")
		// 截断明确标识，模型不能把采样正文当作完整日志原文。
		runes := []rune(current)
		if len(runes) > policy.MaxLogSampleRunes {
			return string(runes[:policy.MaxLogSampleRunes]) + "…[样本已截断]"
		}
		return current
	}
	return value
}
