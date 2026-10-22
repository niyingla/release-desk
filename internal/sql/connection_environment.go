package sqlservice

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"release-desk/internal/identity"
	"release-desk/internal/storage/workspace"
)

// ConnectionPath 定位环境专属的本机加密配置文件。
// 参数 environment：test 或 uat；空值兼容旧测试入口，其他标识拒绝。
// 返回：配置文件绝对路径或不支持环境的错误；不检查文件是否存在。
func (s *Service) ConnectionPath(environment string) (string, error) {
	// 旧测试连接沿用原文件，不迁移或覆盖用户已保存的凭据。
	option, ok := workspace.LookupSQLEnvironment(environment)
	// 校验通过前不拼接文件名，防止环境标识穿越应用目录。
	if !ok {
		return "", errors.New("不支持该 SQL 执行环境")
	}
	name := "connection.json"
	// UAT 文件独立保存；不存在时由调用方提示配置，禁止回退测试。
	if option.ID == "uat" {
		name = "connection-uat.json"
	}
	// 目录根由应用启动参数提供，和可供 AI 编辑的版本目录分开。
	return filepath.Join(s.Dir, name), nil
}

// connectionDigest 绑定预览时的完整连接快照，防止确认前修改账号或凭据。
// 参数 cfg：已解密的环境配置，仅在后端内存中计算；不会发送到前端。
// 返回：私有摘要；配置内容和摘要均不进入日志、预览 JSON 或执行历史。
func connectionDigest(cfg DBConfig) string {
	// JSON 确定字段边界，不使用可冲突的字符串分隔符拼接凭据。
	encoded, _ := json.Marshal(cfg)
	return identity.Digest(string(encoded))
}
