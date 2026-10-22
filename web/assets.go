// Package web 统一嵌入界面和 AI 协议资源，运行时不依赖外部前端文件。
package web

import "embed"

// Assets 只包含产品资源，不把 Go 源码、构建工具或本机数据装入 EXE。
//
//go:embed index.html scripts/* styles/* prompts/*
var Assets embed.FS
