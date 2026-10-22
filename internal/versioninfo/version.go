// Package versioninfo 管理软件自身版本，与工作台中的业务发布版本独立。
package versioninfo

import (
	_ "embed"
	"encoding/json"
)

// 构建脚本通过链接器注入；直接 go run 明确标为开发构建。
var BuildID = "development"
var BuiltAt = ""
var SourceSHA256 = ""

//go:embed releases.json
var catalog []byte

type Release struct {
	Version string   `json:"version"`
	Date    string   `json:"date"`
	Summary string   `json:"summary"`
	Changes []string `json:"changes"`
}

type Info struct {
	Current      string    `json:"current"`
	Versioning   string    `json:"versioning"`
	Releases     []Release `json:"releases"`
	BuildID      string    `json:"build_id"`
	BuiltAt      string    `json:"built_at"`
	SourceSHA256 string    `json:"source_sha256"`
}

// Read 返回内嵌的更新记录及本次构建标识。
// 无业务入参；仅读取 EXE 内嵌数据，不访问用户版本文件或凭据。
// 返回：独立版本信息；内嵌记录格式错误时返回错误，不假称有效发布。
func Read() (Info, error) {
	// 每次返回独立切片，接口编码不会修改共享版本目录。
	var info Info
	if err := json.Unmarshal(catalog, &info); err != nil {
		return Info{}, err
	}
	// 软件版本来自单一目录，构建号区分同版本的多次测试包。
	info.BuildID, info.BuiltAt, info.SourceSHA256 = BuildID, BuiltAt, SourceSHA256
	// 链接器字段不参与业务 5.x 版本和数据库匹配。
	return info, nil
}
