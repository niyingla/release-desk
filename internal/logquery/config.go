// Package logquery 提供内置火山 TLS 的只读连接与查询，凭据不进入版本记录或模型提示词。
package logquery

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"release-desk/internal/platform/secrets"
	"release-desk/internal/storage/fileio"
	"release-desk/internal/storage/workspace"
)

const configFile = "log-connection.json"

// Config 仅在后端内存中包含凭据，持久化时整个对象经当前 Windows 用户加密。
type Config struct {
	Endpoint  string            `json:"endpoint"`
	Region    string            `json:"region"`
	Projects  map[string]string `json:"projects"`
	Topics    map[string]string `json:"topics"`
	AccessKey string            `json:"access_key,omitempty"`
	SecretKey string            `json:"secret_key,omitempty"`
}

// ConnectionRequest 允许用户测试尚未保存的精确主机范围；持久配置本身不复制主机白名单。
type ConnectionRequest struct {
	Config
	AllowedDomains []string `json:"allowed_domains,omitempty"`
}

// Service 管理本机日志连接；独立锁只覆盖设置更新，不阻塞其他业务任务。
type Service struct {
	Store *workspace.Store
	mu    sync.Mutex
}

// DefaultConfig 提供与现有日志网络范围一致的初始连接，不猜测任何环境项目。
// 无业务入参；默认地址集中来自现有策略，所有值都允许在本机页面修改。
// 返回：有团队环境映射、无密钥的初始配置；资源不可用时返回错误，不代表已经授权。
func DefaultConfig() (Config, error) {
	// 每套环境使用各自的项目和后端主题，不能复制生产默认值给测试或 UAT。
	// 团队默认项目来自单独资源，沿用用户当前映射，不把业务 ID 写死在代码分支。
	routes, err := ReadTeamRoutes()
	if err != nil {
		return Config{}, err
	}
	config := Config{Endpoint: routes.Endpoint, Region: routes.Region, Projects: map[string]string{}, Topics: map[string]string{}}
	// 项目映射可以预填，密钥始终留空，不能把当前开发者授权分享给其他电脑。
	for environment, route := range routes.Environments {
		config.Projects[environment], config.Topics[environment] = route.ID, route.DefaultTopic
	}
	return config, nil
}

// Load 读取单独的日志连接文件，不打开或恢复业务工作区。
// 参数 dir：当前数据目录，不能指向模型任意指定的凭据文件。
// 返回：仅供后端使用的完整连接；不存在时返回未配置默认值，损坏或解密失败时明确报错。
func Load(dir string) (Config, error) {
	// 不调用 OpenStore，后台日志子进程不会恢复或改写正在运行的业务任务。
	// 缺少文件与损坏文件分别处理，避免把损坏连接静默当成首次配置。
	// 整份配置都加密，复制软件或业务版本文件不会复制原电脑的访问授权。
	// 底层错误转换为固定提示，路径、密文和解密原文都不进入日志或页面。
	// 独立文件使 AI 学习设置及备份发布记录时不必接触访问密钥。
	config, err := DefaultConfig()
	if err != nil {
		return Config{}, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, configFile))
	if os.IsNotExist(err) {
		return config, nil
	}
	if err != nil {
		return Config{}, errors.New("日志连接读取失败，请检查本机数据目录")
	}
	// 外层仅保存密文，配置损坏不能静默降级为另一套数据源。
	var envelope struct {
		Ciphertext string `json:"ciphertext"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Ciphertext == "" {
		return Config{}, errors.New("日志连接文件损坏，请重新填写")
	}
	plain, err := secrets.Protect(envelope.Ciphertext, true)
	// DPAPI 的数据库提示不适用于日志，这里统一转换成对应业务的错误。
	if err != nil || json.Unmarshal([]byte(plain), &config) != nil {
		return Config{}, errors.New("日志密钥无法解密，请在当前 Windows 用户下重新填写")
	}
	return config, nil
}

// Settings 仅返回页面需要的非敏感摘要，不回填 AK 或 SK。
// 无业务入参；已有密钥只以 configured 布尔状态展示。
// 返回：地址、地域、环境项目及配置状态；文件损坏时返回错误，不输出密文或解密内容。
func (s *Service) Settings(_ []byte) (any, error) {
	// 展示已保存状态不代表云端已经鉴权，实际可用性由用户主动测试确认。
	// 损坏密文允许进入页面重新填写，默认映射伴随明确警告而非静默替换。
	// 当前用户的授权与可共享的团队资源信息分开，只有后者可以成为页面默认值。
	// 设置页可以反复打开，但不能借此取得原始凭据。
	config, err := Load(s.Store.Dir)
	loadError := ""
	if err != nil {
		// 损坏或跨用户复制的密文不能阻止用户进入设置修复，默认值必须伴随明确错误。
		loadError = err.Error()
		config, err = DefaultConfig()
		if err != nil {
			return nil, err
		}
	}
	// 返回可读元数据供首次打开时展示，实际连接测试和查询不会把缓存当成云端事实。
	routes, err := ReadTeamRoutes()
	if err != nil {
		return nil, err
	}
	// 显式字段白名单防止以后扩展 Config 时意外回传敏感字段。
	return map[string]any{"endpoint": config.Endpoint, "region": config.Region, "projects": config.Projects, "topics": config.Topics, "team_routes": routes, "credential_error": loadError, "configured": config.AccessKey != "" && config.SecretKey != ""}, nil
}

// resolve 合并页面留空的已有密钥，再校验当前明确允许的连接主机。
// 参数 body：本次设置或测试请求，允许输入凭据但不接受命令或本机文件路径。
// 返回：本次操作的内存配置；不持久化、不联网，也不输出原始输入。
func (s *Service) resolve(body []byte) (Config, error) {
	// 页面仅能提交连接字段，不能附加任意命令、脚本或本机输出路径。
	// 空密钥的保留语义由后端处理，不能通过把原值回填页面来实现。
	// 旧密文损坏时必须输入整对新凭据，不能混用未知旧值与半份新输入。
	// 省略旧主题字段与显式提供新映射不同，升级不能意外清空既有路由。
	// 所有校验都在持久化和网络调用之前完成，失败保留现有连接。
	request, err := workspace.Decode[ConnectionRequest](body)
	config := request.Config
	if err != nil {
		return Config{}, errors.New("日志连接格式无效")
	}
	// 留空只表示保留本机已有密钥，不会从其他用户目录读取或复制凭据。
	old, err := Load(s.Store.Dir)
	if err != nil && (config.AccessKey == "" || config.SecretKey == "") {
		// 旧密文损坏时必须重新输入整对密钥，不能把未知旧值与半份新凭据混用。
		return Config{}, err
	}
	if config.AccessKey == "" {
		config.AccessKey = old.AccessKey
	}
	if config.SecretKey == "" {
		config.SecretKey = old.SecretKey
	}
	// 旧客户端尚未传入主题映射时保留已有值；显式空映射不能猜测其他 Topic。
	if config.Topics == nil {
		config.Topics = old.Topics
	}
	// 普通地址可整理空白，SK 保留用户输入原文，不能悄悄改动签名凭据。
	config.Endpoint = strings.TrimRight(strings.TrimSpace(config.Endpoint), "/")
	config.Region, config.AccessKey = strings.TrimSpace(config.Region), strings.TrimSpace(config.AccessKey)
	allowed := workspace.LogDomains(s.Store.Config())
	// 用户可在同一表单明确测试新的精确主机；模型查询仍使用软件已保存的固定范围。
	if request.AllowedDomains != nil {
		// 此范围来自用户主动提交的设置表单，不是模型查询参数。
		// 仅允许精确主机名，禁止通配符、URL 或携带账户信息的文本。
		// 持久任务仍使用已保存设置的启动快照，不能借测试操作改变运行中授权。
		if err = workspace.ValidateLogDomains(request.AllowedDomains); err != nil {
			return Config{}, err
		}
		allowed = request.AllowedDomains
	}
	if err = Validate(config, allowed); err != nil {
		return Config{}, err
	}
	return config, nil
}

// Validate 检查 TLS 连接的协议、主机、地域及环境标识。
// 参数 config：候选连接，密钥仅用于非空校验，不在错误中展示。
// 参数 allowed：用户明确配置的精确日志主机，不由模型或重定向扩大。
// 返回：合法返回 nil；缺失字段、未知环境或不在允许范围的 Endpoint 返回中文错误。
func Validate(config Config, allowed []string) error {
	// Endpoint 是用户明确允许的接收方，不接受相对路径、用户信息或附加查询参数。
	// 地域参与请求签名，格式错误或缺少凭据时先提示用户，不发出无效请求。
	// 环境字段属于固定业务集合，拼错生产标识不能退回另一环境继续查询。
	endpoint, err := url.Parse(config.Endpoint)
	// 密钥只能交给用户明确允许的 HTTPS 主机，不能随 URL 携带用户名、查询参数或路径。
	if err != nil || endpoint.Scheme != "https" || endpoint.User != nil || endpoint.Host == "" || endpoint.Path != "" || endpoint.RawQuery != "" || endpoint.Fragment != "" || !slices.Contains(allowed, strings.ToLower(endpoint.Hostname())) {
		return errors.New("日志地址须为允许范围内的 HTTPS 主机，请在高级设置中核对域名")
	}
	if !regexp.MustCompile(`^[a-z][a-z0-9-]{1,63}$`).MatchString(config.Region) || config.AccessKey == "" || config.SecretKey == "" {
		return errors.New("请填写正确的地域以及 AK、SK；已有密钥可留空保留")
	}
	// 环境映射显式填写；不能因为缺少生产配置就使用测试项目。
	for environment, project := range config.Projects {
		if !slices.Contains([]string{"prod", "test", "uat"}, environment) || strings.ContainsAny(project, "\r\n\t") {
			return errors.New("日志环境项目映射无效")
		}
	}
	// 可编辑的主题映射遵循同一环境集合，不把高级配置当成任意命令或查询参数。
	for environment, topic := range config.Topics {
		if !slices.Contains([]string{"prod", "test", "uat"}, environment) || strings.ContainsAny(topic, "\r\n\t") {
			return errors.New("日志 Topic 映射无效")
		}
	}
	return nil
}

// Save 校验并加密保存本机日志连接，保存设置不会发起日志查询。
// 参数 body：页面输入，留空的密钥保留已有值；环境项目可稍后通过读取项目补齐。
// 返回：成功返回非敏感状态；校验、加密或原子写入失败时保留已有文件。
func (s *Service) Save(body []byte) (any, error) {
	// 保存连接仅更新取证能力，不说明日志已查到或业务已经通过验证。
	// 普通 settings.json、发布记录和任务历史均不参与本次凭据写入。
	// 加密失败保持旧文件，不能用半份输入替代仍完整的旧密钥对。
	// 损坏连接在用户明确重填后才允许修复，先保留原密文再进行替换。
	// 备份仍是密文，恢复需要原 Windows 用户授权，不制作明文迁移副本。
	// 并发保存通过独立锁串行化，扫描与 SQL 通道不承担凭据文件的互斥。
	// 并发保存不能各自从旧密钥快照开始，锁只覆盖这一份配置文件。
	s.mu.Lock()
	defer s.mu.Unlock()
	config, err := s.resolve(body)
	if err != nil {
		return nil, err
	}
	// 整个连接对象加密后才交给文件层，AK、SK 均不在磁盘出现明文。
	raw, err := json.Marshal(config)
	if err != nil {
		return nil, errors.New("日志连接编码失败")
	}
	ciphertext, err := secrets.Protect(string(raw), false)
	if err != nil {
		return nil, errors.New("日志密钥加密失败，设置未保存")
	}
	// 明确重新填写修复损坏配置时先保留原密文，不静默清空或销毁可能仍可恢复的文件。
	if _, oldErr := Load(s.Store.Dir); oldErr != nil {
		previous, readErr := os.ReadFile(filepath.Join(s.Store.Dir, configFile))
		if readErr != nil {
			return nil, errors.New("原日志连接无法备份，设置未覆盖")
		}
		if err = fileio.AtomicWrite(filepath.Join(s.Store.Dir, "log-connection.previous.json"), previous); err != nil {
			return nil, errors.New("原日志连接备份失败，设置未覆盖")
		}
	}
	// 原子替换失败时不会破坏旧连接，也不改变业务版本、任务历史或数据库连接。
	if err = fileio.WriteJSON(filepath.Join(s.Store.Dir, configFile), map[string]string{"ciphertext": ciphertext}); err != nil {
		return nil, errors.New("日志连接保存失败")
	}
	return map[string]bool{"configured": true}, nil
}
