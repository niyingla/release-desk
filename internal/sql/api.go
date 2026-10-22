package sqlservice

import (
	"errors"
	"log/slog"
	"time"

	"release-desk/internal/policy"
	"release-desk/internal/storage/workspace"
)

// DBSettings 返回所选环境不含密码的连接摘要。
// 参数 body：仅含 environment 的请求；空标识兼容旧测试入口。
// 返回：返回主机、端口、账号和是否已配置，不返回已保存密码。
func (a *Service) DBSettings(body []byte) (any, error) {
	// 界面只获取连接定位信息，不获取已保存密码。
	// 没有合法配置时返回未配置状态。
	// 先解析环境再加载连接，缺失 UAT 不会查询测试配置。

	req, e := workspace.Decode[struct {
		Environment string `json:"environment"`
	}](body)
	if e != nil {
		return nil, e
	}
	// 缺失标识仅兼容旧测试入口，未知非空标识不能作为环境默认值。
	option, ok := workspace.LookupSQLEnvironment(req.Environment)
	if !ok {
		return nil, errors.New("不支持该 SQL 执行环境")
	}
	cfg, e := a.LoadDB(option.ID)

	if e != nil {
		// 摘要接口不暴露解密细节或文件正文，用户可在设置中重新填写。
		return map[string]any{"environment": option.ID, "host": "", "port": 3306, "user": "", "configured": false}, nil
	}
	// 只回填非敏感定位字段，绝不把旧密码发送给表单。
	return map[string]any{"environment": option.ID, "host": cfg.Host, "port": cfg.Port, "user": cfg.User, "configured": true}, nil
}

// DBSave 加密保存测试或 UAT 的独立数据库连接。
// 参数 body：界面传入的 UTF-8 JSON 请求；已通过本机能力令牌鉴权，仍需进行业务字段校验。
// 返回：成功返回 nil 结果与 nil 错误；请求校验、记录定位或后续职责处理失败时返回错误，未返回业务成功凭证。
func (a *Service) DBSave(body []byte) (any, error) {
	// 数据库连接只允许标注为受支持的执行环境。
	// 运行中的批次不能被切换到另一个连接。
	// 密码留空时复用本机加密凭据。
	// 修改主机与账号不自动验证有写入权限。
	// 本接口只保存配置，不执行用户 SQL。
	// 凭据统一由 DPAPI 加密后落盘。

	cfg, e := workspace.Decode[DBConfig](body)

	if e != nil {
		return nil, e
	}
	a.Store.Mu.Lock()

	defer a.Store.Mu.Unlock()

	if e = a.Store.EnsureIdle(); e != nil {
		return nil, e
	}

	if cfg.Password == "" {
		// 留空只复用同一环境的密码，UAT 尚未配置时绝不借用测试凭据。
		old, e := a.LoadDB(cfg.Environment)
		if e == nil {
			cfg.Password = old.Password
		}
	}
	return nil, a.SaveDB(cfg)
}

// DBPlan 生成单条 SQL 的逐库执行预览。
// 参数 body：界面传入的 UTF-8 JSON 请求；已通过本机能力令牌鉴权，仍需进行业务字段校验。
// 返回：返回一次性令牌、内容摘要、全部目标与跳过清单；无可执行目标时返回错误。
func (a *Service) DBPlan(body []byte) (any, error) {
	// 预览会实时查询当前账号可见的数据库。
	// 目标范围由所属端和配置规则共同决定。
	// 成功的相同 SQL 默认排除。
	// 失败重试模式只选择明确失败的目标。
	// 生成的令牌绑定这次完整预览。
	// 令牌保存在后端，前端不能临时添加目标库。

	req, e := workspace.Decode[struct {
		Version     string `json:"version"`
		ID          string `json:"id"`
		Retry       bool   `json:"retry"`
		VerifyOnly  bool   `json:"verify_only"`
		Environment string `json:"environment"`
	}](body)
	if e != nil {
		return nil, e
	}

	p, e := a.Plan(req.Version, req.ID, req.Retry, req.Environment, req.VerifyOnly)
	// 环境与目标服务器已绑定在后端计划中，确认请求只携带一次性令牌。

	if e != nil {
		return nil, e
	}
	a.PlanMu.Lock()

	a.Plans[p.Token] = p

	a.PlanMu.Unlock()
	return p, nil
}

// DBExecute 消费预览令牌并启动确认后的执行。
// 参数 body：界面传入的 UTF-8 JSON 请求；已通过本机能力令牌鉴权，仍需进行业务字段校验。
// 返回：成功返回 nil 结果与 nil 错误；请求校验、记录定位或后续职责处理失败时返回错误，未返回业务成功凭证。
func (a *Service) DBExecute(body []byte) (any, error) {
	// 只接受后端生成的预览令牌。
	// 令牌在读取时立即作废，防止重复点击执行。
	// 过期预览必须重新识别目标范围。
	// 勾选确认属于用户主动执行的最后一步，不要求重复输入文字。
	// 执行器还会再次核对 SQL 摘要和所属端。
	// 日志仅输出定位 ID、数量和耗时。

	req, e := workspace.Decode[struct {
		Token   string `json:"token"`
		Confirm bool   `json:"confirm"`
	}](body)
	if e != nil {
		return nil, e
	}

	a.PlanMu.Lock()
	p, ok := a.Plans[req.Token]

	delete(a.Plans, req.Token)
	a.PlanMu.Unlock()
	// 令牌一次性使用，确认对象绑定预览时的内容摘要和目标库列表。

	if !ok || time.Since(p.Created) > policy.PlanExpiryMinutes*time.Minute {
		return nil, errors.New("执行预览已过期，请重新预览")
	}
	if !req.Confirm {
		return nil, errors.New("请先勾选核对目标库和 SQL")
	}

	slog.Info("开始 SQL 批量执行", "environment", p.Environment, "version", p.Version, "item_id", p.ItemID, "database_count", len(p.Databases))
	return nil, a.Execute(p)
}
