package settingsservice

import (
	"errors"
	"path/filepath"
	"slices"

	"release-desk/internal/storage/fileio"
	"release-desk/internal/storage/workspace"
)

// SaveTheme 保存用户明确选择的内置主题。
// 参数 body：含 theme 的 JSON 对象，仅接受内置颜色方案标识。
// 返回：保存成功返回主题名称；未知值或写盘失败时返回错误且保留旧偏好。
func (a *Service) SaveTheme(body []byte) (any, error) {
	// 使用有限集合，偏好不能向 HTML 注入任意属性或脚本。
	req, err := workspace.Decode[workspace.UiPreferences](body)
	if err != nil {
		return nil, err
	}
	// 校验在获取锁和写盘前完成，无效请求不会改变原有偏好。
	if !slices.Contains(workspace.Themes, req.Theme) {
		return nil, errors.New("主题不存在")
	}
	// 与版本配置分开保存，切换颜色不受 SQL 或扫描任务影响。
	s := a.Store
	s.Mu.Lock()
	defer s.Mu.Unlock()
	// 先原子写盘，失败时 UI 会恢复原主题，而不报告保存成功。
	if err = fileio.WriteJSON(filepath.Join(s.Dir, "ui-preferences.json"), req); err != nil {
		return nil, err
	}
	// 落盘成功后才更新内存；下次随机端口启动仍从同一文件恢复。
	s.Theme = req.Theme
	// 偏好返回值不含仓库内容和数据库连接信息。
	return req.Theme, nil
}
