package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestDocumentationRequiresEveryParameter 验证缺少某个参数或仅写名称会阻止构建。
// 参数 t：测试上下文；只解析内存 Go 片段，不修改项目源文件。
func TestDocumentationRequiresEveryParameter(t *testing.T) {
	for _, tc := range []struct {
		comment string
		want    int
	}{
		{"", 4},
		{"// Sample 组装结果。\n// 返回：处理结果。\n", 2},
		{"// Sample 组装结果。\n// 参数 first、second：输入。\n// 返回：处理结果。\n", 2},
		{"// Sample 组装结果。\n// 参数 first：\n// 参数 second：第二个候选。\n// 返回：处理结果。\n", 1},
		{"// Sample 组装结果。\n// 参数 first：第一个候选。\n// 参数 second：第二个候选。\n// 返回：处理结果。\n", 0},
	} {
		// 同一签名只改变说明，确保检测不会因方法体注释数量增加而漏过。
		file, err := parser.ParseFile(token.NewFileSet(), "qa.go", "package qa\n"+tc.comment+"func Sample(first,second string) string { return first+second }", parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		got := documentationProblems(file.Decls[0].(*ast.FuncDecl))
		if len(got) != tc.want {
			t.Fatalf("参数说明检测错误：%v，期望 %d 项", got, tc.want)
		}
	}
}
