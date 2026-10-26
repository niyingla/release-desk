package sqlservice

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"release-desk/internal/identity"
	"release-desk/internal/platform/secrets"
	"release-desk/internal/policy"
	"release-desk/internal/sql/schema"
	sqlscript "release-desk/internal/sql/script"
	"release-desk/internal/storage/fileio"
	"release-desk/internal/storage/workspace"
)

type DBConfig struct {
	SchemaOnly  bool                `json:"-"`
	Host        string              `json:"host"`
	Port        int                 `json:"port"`
	User        string              `json:"user"`
	Password    string              `json:"password"`
	Environment string              `json:"environment"`
	ScopeRules  map[string][]string `json:"-"`
}

// LoadDB 读取并解密指定环境的独立连接配置。
// 参数 environment：test 或 uat；空值仅用于兼容旧测试客户端。
// 返回：仅供后端使用的配置；缺失、环境不符或解密失败时返回错误，不回退连接。
func (s *Service) LoadDB(environment string) (DBConfig, error) {
	// 连接文件独立于可供 AI 读取的版本文件。
	// 文件缺失时提示用户设置，不猜测服务器。
	// 文件名与内部环境必须一致，防止错误复制文件造成跨环境执行。
	// 密码在需要连接时才解密到内存。
	// 数据库匹配规则来自当前配置快照。

	var cfg DBConfig
	path, e := s.ConnectionPath(environment)
	// 环境先于文件读取校验，非法标识无法探测其他配置文件。
	if e != nil {
		return cfg, e
	}
	b, e := os.ReadFile(path)

	if e != nil {
		return cfg, errors.New("请先在连接与设置中配置所选环境的数据库")
	}
	if e = json.Unmarshal(b, &cfg); e != nil {
		return cfg, e
	}

	if cfg.Environment != workspace.EffectiveSQLEnvironment(environment) {
		return cfg, errors.New("连接文件的环境标识不一致，请重新配置")
	}
	// 仅验证属于所选环境后才解密，配置串放时直接拒绝连接。
	cfg.Password, e = secrets.Protect(cfg.Password, true)
	cfg.ScopeRules = s.Config().ScopeRules
	return cfg, e
}

// SaveDB 校验并加密保存所选环境的连接配置。
// 参数 cfg：test 或 uat 独立配置；密码只在进程内使用，不写日志。
// 返回：成功返回 nil；环境、端口、加密或文件写入失败时返回错误。
func (s *Service) SaveDB(cfg DBConfig) error {
	// 文件定位先校验环境，不允许外部标识成为任意路径。
	// 主机、账号和端口都要完整有效。
	// 密码先经本机 DPAPI 加密。

	path, e := s.ConnectionPath(cfg.Environment)
	if e != nil {
		return e
	}
	// 老客户端缺省仍归测试；独立文件的 environment 写入后必须明确。
	cfg.Environment = workspace.EffectiveSQLEnvironment(cfg.Environment)
	if strings.TrimSpace(cfg.Host) == "" || strings.TrimSpace(cfg.User) == "" || cfg.Port < 1 || cfg.Port > 65535 {
		return errors.New("请完整填写所选环境的主机、端口和账号")
	}

	enc, e := secrets.Protect(cfg.Password, false)
	if e != nil {
		return e
	}

	// 加密完成后再原子替换该环境文件，不修改其他环境的连接。
	cfg.Password = enc
	return fileio.WriteJSON(path, cfg)
}

// ConnectDB 创建限制范围和超时的数据库连接池。
// 参数 cfg：已校验且环境明确的连接配置；密码只在进程内使用，不写日志。
// 参数 name：本方法定位的名称或目标数据库名；所属范围由调用方校验。
// 返回：返回待使用的数据库连接池；配置或驱动初始化失败时返回错误，调用方负责 Close。
func ConnectDB(cfg DBConfig, name string) (*sql.DB, error) {
	// 连接只能来自明确标记的测试或 UAT 配置。
	// 使用驱动配置对象组装 DSN，不拼接包含密码的字符串。
	// 逐库使用独立数据库选择。
	// 字符集统一支持中文配置和注释。
	// 禁止驱动一次执行多条语句。
	// 连接建立与读写分别设置超时。
	// 每个库只使用一个连接，避免额外并发扩散。
	// 调用方负责关闭数据库句柄。
	// Open 只创建连接池，实际连通性由后续操作确认。

	if _, ok := workspace.LookupSQLEnvironment(cfg.Environment); !ok || cfg.Environment == "" {
		return nil, errors.New("仅支持明确标记的测试或 UAT 环境")
	}

	m := mysql.NewConfig()
	m.User = cfg.User

	m.Passwd = cfg.Password
	m.Net = "tcp"

	m.Addr = fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	m.DBName = name

	m.ParseTime = true
	m.Timeout = policy.DatabaseConnectSeconds * time.Second

	m.ReadTimeout = policy.DatabaseReadSeconds * time.Second
	m.WriteTimeout = policy.DatabaseWriteSeconds * time.Second

	m.Collation = "utf8mb4_general_ci"
	m.MultiStatements = false

	db, e := sql.Open("mysql", m.FormatDSN())
	if e != nil {
		return nil, e
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db, nil
}

// MatchDB 按默认业务规则匹配数据库。
// 参数 names：数据库账号实际可见的库名集合，不是 AI 推测的目标。
// 参数 side：所属业务端，只允许租户端、服务商端或平台端。
// 返回：返回命中默认规则的排序库名列表；未命中时返回空列表。
func MatchDB(names []string, side string) []string {
	// 此便利入口使用集中定义的业务默认规则。

	return MatchDBRules(names, side, workspace.DefaultSettings().ScopeRules)
}

// MatchDBRules 按指定端的正则规则筛选数据库。
// 参数 names：数据库账号实际可见的库名集合，不是 AI 推测的目标。
// 参数 side：所属业务端，只允许租户端、服务商端或平台端。
// 参数 rules：按业务端分组的数据库正则规则；保存配置时已经检查语法。
// 返回：返回命中该端任一有效规则的排序库名列表，每个输入库名最多加入一次。
func MatchDBRules(names []string, side string, rules map[string][]string) []string {
	// 每个名字只要命中该端任一规则就纳入目标。
	// 同一个数据库不会因为多条规则命中而重复加入。
	// 模板库通过相应规则明确包含。
	// view、points 等其他业务库不会被默认规则选中。
	// 无效正则不做宽泛回退。
	// 排序便于用户逐项核对执行清单。

	out := []string{}

	for _, n := range names {
		ok := false
		for _, pattern := range rules[side] {
			matched, err := regexp.MatchString(pattern, n)
			if err == nil && matched {
				ok = true
				break
			}
		}
		if ok {
			out = append(out, n)
		}
	}

	// 同前缀按数字尾号升序；服务商、独立租户和共享库使用同一规则。
	sort.SliceStable(out, func(i, j int) bool { return databaseNameLess(out[i], out[j]) })

	return out
}

// DiscoverDB 只读发现所选环境账号可见的目标库。
// 参数 cfg：已校验且环境明确的连接配置；密码只在进程内使用，不写日志。
// 参数 side：所属业务端，只允许租户端、服务商端或平台端。
// 返回：返回当前账号可见且命中该端规则的全部库名；连接或扫描失败时返回错误。
func DiscoverDB(cfg DBConfig, side string) ([]string, error) {
	// 只执行 SHOW DATABASES 获取账号可见范围。
	// 不读取业务数据，也不修改任何表。
	// 查询使用独立连接，结束后及时关闭。
	// 超时只影响目标发现，不触发业务执行。
	// 逐行扫描时保留驱动错误。
	// 读取全部完成后再套所属端规则。
	// 不能因为某一库有相同表名就猜测其所属端。
	// 规则来自连接时的本机配置快照。
	// 无配置规则的内部调用使用集中默认值。
	// 返回排序后的完整目标，不做采样截断。

	db, e := ConnectDB(cfg, "")
	if e != nil {
		return nil, e
	}

	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	rows, e := db.QueryContext(ctx, "SHOW DATABASES")

	if e != nil {
		return nil, e
	}
	defer rows.Close()

	names := []string{}

	for rows.Next() {
		var n string
		if e = rows.Scan(&n); e != nil {
			return nil, e
		}
		names = append(names, n)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}

	rules := cfg.ScopeRules

	if len(rules) == 0 {
		rules = workspace.DefaultSettings().ScopeRules
	}
	return MatchDBRules(names, side, rules), nil
}

type Plan struct {
	VerifyOnly       bool   `json:"verify_only,omitempty"`
	Environment      string `json:"environment"`
	connectionDigest string
	SchemaCheckOnly  []string  `json:"schema_check_only"`
	Token            string    `json:"token"`
	Version          string    `json:"version"`
	ItemID           string    `json:"item_id"`
	Digest           string    `json:"digest"`
	Server           string    `json:"server"`
	Databases        []string  `json:"databases"`
	Skipped          []string  `json:"skipped"`
	SQL              string    `json:"sql"`
	Side             string    `json:"side"`
	Created          time.Time `json:"-"`
}

// ServerID 形成不含凭据的执行环境标识。
// 参数 cfg：已校验且环境明确的连接配置；密码只在进程内使用，不写日志。
// 返回：返回 host:port 形式的无凭据连接标识，用于隔离不同服务器执行历史。
func ServerID(cfg DBConfig) string {
	// 执行历史按主机和端口区分环境连接。

	return cfg.Host + ":" + strconv.Itoa(cfg.Port)
}

// Plan 识别当前 SQL 尚需执行的目标并生成预览。
// 参数 version：所属版本的精确名称。
// 参数 id：该版本内 SQL 事项的稳定 ID。
// 参数 retry：是否仅重试已明确失败的目标；不会纳入成功或结果未确认的库。
// 参数 environment：本轮选定的 test 或 uat，不从版本记录或上次执行推断。
// 参数 verifyOnly：为 true 时只规划结构查询，包含待核验和已中断目标，不重放任何业务 SQL。
// 返回：返回绑定当前脚本和目标范围的 Plan；校验、发现目标或无可执行目标时返回错误。
func (s *Service) Plan(version, id string, retry bool, environment string, verifyOnly bool) (Plan, error) {
	// 结构脚本的成功历史需要重新查询元数据，不能永远相信上次执行状态。
	// 复核模式会随逐库审计保留，发现漂移后仍禁止重复初始化数据。
	// 结果未知的目标不自动重试，避免重放潜在已提交的变更。
	// 预览读取选中 SQL 的稳定快照。
	// 已有运行任务时不启动另一批执行。
	// 只有 SQL 类型可以进入该流程。
	// 脚本先做语法和作用范围检查。
	// 目标发现只使用所选环境连接。
	// 根据所属端实时识别所有可见数据库。
	// 记录摘要绑定完整 SQL 内容，换行变化也会形成新内容。
	// 服务器标识防止别的连接历史被误当作已执行。
	// 每个库只使用相同内容的最近一次结果。
	// 成功目标默认跳过，避免重复 DML。
	// 已中断或执行中结果不自动重试。
	// 网络不确定时必须先人工核对。
	// 普通执行纳入未执行或明确失败的库。
	// 失败重试模式排除未执行的库。
	// 跳过清单单独展示给用户。
	// 没有可执行目标时给出原因，不生成空批次。
	// 令牌带生成时间，避免长期复用过时预览。

	s.Mu.Lock()

	_, item, _, e := s.Find(version, id)

	busy := s.SQLActive() || s.AIBusy || s.Closing

	history := workspace.Clone(s.History)

	s.Mu.Unlock()
	if e != nil {
		return Plan{}, e
	}

	if busy {
		return Plan{}, errors.New("已有 SQL 执行任务，请等待完成")
	}

	if item.Kind != "sql" {
		return Plan{}, errors.New("只能执行 SQL 记录")
	}

	if _, e = sqlscript.SplitSQL(item.Data["sql"]); e != nil {
		return Plan{}, e
	}

	cfg, e := s.LoadDB(environment)

	if e != nil {
		return Plan{}, e
	}

	names, e := DiscoverDB(cfg, item.Data["side"])
	if e != nil {
		return Plan{}, e
	}

	p := Plan{Environment: cfg.Environment, connectionDigest: connectionDigest(cfg), Token: identity.NewID(), Version: version, ItemID: id, Digest: identity.Digest(item.Data["sql"]), Server: ServerID(cfg), SQL: item.Data["sql"], Side: item.Data["side"], Created: time.Now(), Databases: []string{}, Skipped: []string{}}

	p.VerifyOnly = verifyOnly
	latest := latestExecutions(p, history)
	// 旧成功历史只免除重复数据写入，结构语句仍需查询实际数据库。
	statements, _ := sqlscript.SplitSQL(item.Data["sql"])
	hasStructure := false
	for _, statement := range statements {
		hasStructure = hasStructure || schema.Parse(statement).Structural
	}
	// 纯数据脚本没有可复核结构，不能用空检查列表生成成功凭证。
	if verifyOnly && !hasStructure {
		return Plan{}, errors.New("当前 SQL 没有可核验的结构语句")
	}

	for _, name := range names {
		// 显式只读复核不重试 DDL，因此允许重新检查以前结果未确认的目标。
		if verifyOnly {
			p.Databases = append(p.Databases, name)
			p.SchemaCheckOnly = append(p.SchemaCheckOnly, name)
			continue
		}
		r, ok := latest[name]
		if ok && r.State == "成功" {
			if hasStructure {
				p.Databases = append(p.Databases, name)
				p.SchemaCheckOnly = append(p.SchemaCheckOnly, name)
				continue
			}
			p.Skipped = append(p.Skipped, name)
			continue
		}
		// 上次仅复核的库继续保持只读，结构漂移不能触发重复初始化数据。
		if ok && r.SchemaOnly {
			p.Databases = append(p.Databases, name)
			p.SchemaCheckOnly = append(p.SchemaCheckOnly, name)
			continue
		}
		if ok && (r.State == "已中断" || r.State == "执行中" || r.State == "待核验") {
			p.Skipped = append(p.Skipped, name+"（结果未确认，需核对）")
			continue
		}
		if retry && (!ok || (r.State != "失败" && r.State != "结构不一致")) {
			continue
		}
		p.Databases = append(p.Databases, name)
	}

	if len(p.Databases) == 0 {
		return p, ErrNoSQLTargets
	}
	return p, nil
}
