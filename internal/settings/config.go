package settingsservice

import (
	"path/filepath"

	"release-desk/internal/storage/fileio"
	"release-desk/internal/storage/workspace"
)

// GetSettings 向设置界面返回非凭据配置。
// 无业务入参；使用接收对象或本机启动上下文，不隐式触发外部写操作。
// 返回：返回设置页所需的业务路径、作者和数据库规则，不含凭据。
func (a *Service) GetSettings(_ []byte) (any, error) {
	// 设置界面只读取业务路径和规则。

	// 业务配置不包含数据库密码，可以直接交给设置界面编辑。
	return a.Store.Config(), nil
}

// SaveSettings 保存并应用已校验的本机设置。
// 参数 body：界面传入的 UTF-8 JSON 请求；已通过本机能力令牌鉴权，仍需进行业务字段校验。
// 返回：成功返回 nil 结果与 nil 错误；请求校验、记录定位或后续职责处理失败时返回错误，未返回业务成功凭证。
func (a *Service) SaveSettings(body []byte) (any, error) {
	// 先校验完整配置，再进行磁盘写入。
	// 仓库路径和作者改变会影响下一次导入。
	// 运行中的数据库和验证任务不接受配置切换。
	// 记录锁与配置锁职责不同，按固定顺序获取。
	// 持久化成功后才替换运行时配置。
	// 匹配规则整体更新，不能暴露半套新旧值。
	// Git 同步或扫描期间使用仓库互斥锁。
	// 修改仓库后重置读取引用为当前 HEAD。
	// 本操作不会自动 fetch 或合并任何分支。

	config, err := workspace.Decode[workspace.Settings](body)

	if err != nil {
		return nil, err
	}
	if err = workspace.ValidateSettings(config); err != nil {
		return nil, err
	}
	// 执行中的任务固定使用原配置，禁止在运行期间改变数据库范围与仓库。

	a.Store.Mu.Lock()
	defer a.Store.Mu.Unlock()

	if err = a.Store.EnsureIdle(); err != nil {
		return nil, err
	}
	if err = fileio.WriteJSON(filepath.Join(a.Store.Dir, "settings.json"), config); err != nil {
		return nil, err
	}

	a.Store.ConfigMu.Lock()
	a.Store.Settings = config

	a.Store.ConfigMu.Unlock()
	a.Git.Configure(config.SQLRepository, config.GitAuthor)
	return nil, nil
}
