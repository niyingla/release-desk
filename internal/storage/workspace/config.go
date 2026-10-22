package workspace

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"

	"release-desk/internal/policy"
	"release-desk/internal/storage/fileio"
)

// Settings 保存本机环境配置，凭据由独立的 DPAPI 文件保存。
type Settings struct {
	VerificationConcurrency int                 `json:"verification_concurrency"`
	SQLRepository           string              `json:"sql_repository"`
	CodeRepository          string              `json:"code_repository"`
	GitAuthor               string              `json:"git_author"`
	LogSkill                string              `json:"log_skill"`
	ScopeRules              map[string][]string `json:"scope_rules"`
}

// DefaultSettings 提供集中管理的初始本机配置。
// 无业务入参；使用接收对象或本机启动上下文，不隐式触发外部写操作。
// 返回：返回用户已指定的初始环境配置，不包含数据库密码。
func DefaultSettings() Settings {
	// 这里集中的是用户已经指定的初始环境约定。

	// 用户指定的初始路径集中在一个入口，换电脑后修改 settings.json 即可。
	return Settings{VerificationConcurrency: policy.VerificationConcurrency, SQLRepository: `D:\workCode\db`, CodeRepository: `D:\codeWork\qiyu-douyin`, GitAuthor: "huohuo", LogSkill: filepath.Join(os.Getenv("USERPROFILE"), ".codex", "skills", "bamboo-tls-query", "SKILL.md"), ScopeRules: map[string][]string{"租户端": {`^newlink_share.*$`, `^newlink\d*$`}, "服务商端": {`^sp\d*$`}, "平台端": {`^newlink_pf$`}}}
}

// LoadSettings 读取或初始化本机设置。
// 参数 dir：当前用户的数据根目录；目录中只管理本应用记录和配置。
// 返回：返回完整配置；首次创建失败或已有文件损坏时返回错误，不覆盖原文件。
func LoadSettings(dir string) (Settings, error) {
	// 首次启动生成可编辑的配置文件。
	// 已有文件损坏时不静默恢复默认值。
	// 保留原文件便于用户排查输入错误。
	// 缺失非凭据字段可用初始约定补全。

	config := DefaultSettings()

	path := filepath.Join(dir, "settings.json")
	// 缺少配置时生成文件，已有配置损坏时保留原文件并报错。
	b, err := os.ReadFile(path)

	if os.IsNotExist(err) {
		return config, fileio.WriteJSON(path, config)
	}
	if err != nil {
		return config, err
	}

	if err = json.Unmarshal(b, &config); err != nil {
		return config, errors.New("settings.json 格式错误，请修正后启动")
	}
	return config, ValidateSettings(config)
}

// ValidateSettings 检查路径、作者与数据库规则。
// 参数 config：已经校验的本机配置快照；调用期间保持一致。
// 返回：合法配置返回 nil；绝对路径、作者或任一端匹配规则无效时返回错误。
func ValidateSettings(config Settings) error {
	// 仓库要求绝对路径，避免启动目录改变后读错位置。
	// 作者不能为空，防止意外扩大到所有提交。
	// 三个业务端都必须有匹配规则。

	// 只检查必要值与规则合法性，不要求仓库必须联网。

	if !filepath.IsAbs(config.SQLRepository) || !filepath.IsAbs(config.CodeRepository) || config.GitAuthor == "" {
		return errors.New("仓库路径应为绝对路径，Git 作者不能为空")
	}
	// 零值兼容旧调用方，实际启动时使用集中默认值；非零偏好必须在受控范围。
	if config.VerificationConcurrency < 0 || config.VerificationConcurrency > policy.VerificationConcurrency {
		return errors.New("验证并发数超出允许范围")
	}

	for _, side := range []string{"租户端", "服务商端", "平台端"} {
		if len(config.ScopeRules[side]) == 0 {
			return errors.New("缺少数据库匹配规则：" + side)
		}
		for _, pattern := range config.ScopeRules[side] {
			if _, err := regexp.Compile(pattern); err != nil {
				return errors.New("数据库匹配规则无效：" + side)
			}
		}
	}
	return nil
}

// Config 返回线程安全的配置副本。
// 无业务入参；使用接收对象或本机启动上下文，不隐式触发外部写操作。
// 返回：返回独立的 Settings 副本，调用方修改不会改变运行配置。
func (s *Store) Config() Settings {
	// 配置使用单独的锁，与版本记录锁区分。

	// 单独的配置锁不与记录锁互相嵌套，查询任务可获得完整一致的配置快照。
	s.ConfigMu.RLock()
	defer s.ConfigMu.RUnlock()
	return Clone(s.Settings)
}
