package main

import "testing"

// TestPublicArchivePaths 验证公开模板可发布，本机业务与配置文件被拒绝。
// 参数 t：测试上下文；只比较虚构 ZIP 路径，不读取用户业务文件。
func TestPublicArchivePaths(t *testing.T) {
	for _, tc := range []struct {
		name    string
		allowed bool
	}{
		{"examples/", true}, {"examples/template.json", true}, {"AGENTS.md", true}, {"web/prompts/verification-schema.json", true},
		{"examples/5.7.1.json", false}, {"tools/examples/generate.cjs", false}, {".work/private.json", false}, {"settings.json", false}, {"nested/connection-test.json", false}, {"../outside", false},
	} {
		// 正反例共享入口，防止允许模板的同时放宽整个 examples 目录。
		if allowed := privatePathReason(tc.name) == ""; allowed != tc.allowed {
			t.Fatalf("公开路径分类错误：%s", tc.name)
		}
	}
}
