package workspace

import (
	"path/filepath"

	"release-desk/internal/storage/fileio"
)

// RememberVerificationConcurrency 保存已通过范围校验的批量验证偏好。
// 参数 count：用户本次选择的并发数，调用方须先校验范围并持有 Mu，不能在任务中途调用。
// 返回：设置原子落盘错误；失败不会改变内存设置，也不启动本轮任务。
func (s *Store) RememberVerificationConcurrency(count int) error {
	// 克隆完整非敏感设置，修改并发偏好不能覆盖仓库、作者或数据库匹配规则。
	config := s.Config()
	if config.VerificationConcurrency == count {
		return nil
	}
	config.VerificationConcurrency = count
	// 偏好与业务配置沿用同一校验入口，避免界面和启动参数出现两套允许范围。
	if err := ValidateSettings(config); err != nil {
		return err
	}
	// 先写盘再替换运行快照，保存失败时用户仍可重试而不会误以为已记住设置。
	if err := fileio.WriteJSON(filepath.Join(s.Dir, "settings.json"), config); err != nil {
		return err
	}
	// 配置读取使用独立锁，切换快照时不能让轮询读到半套新旧字段。
	s.ConfigMu.Lock()
	defer s.ConfigMu.Unlock()
	// 记录锁保证此处不会与设置页同时写入另一份配置。
	// 只记住用户设置，不因此改变已有批次或自动调用模型。
	s.Settings = config
	return nil
}
