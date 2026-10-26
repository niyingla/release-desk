package main

import (
	"go/ast"
	"regexp"
	"strings"
)

// documentationProblems 检查每个命名方法是否有说明、逐参数描述和返回语义。
// 参数 fn：静态解析的函数声明；匿名回调不作为独立方法处理。
// 返回：需要补充的说明列表；空列表表示通过，不修改任何源码。
func documentationProblems(fn *ast.FuncDecl) []string {
	doc := strings.TrimSpace(fn.Doc.Text())
	var problems []string
	// 方法概述必须独立存在，不能仅靠方法体中的注释冒充接口说明。
	if doc == "" {
		problems = append(problems, "缺少方法说明")
	} else if !strings.HasPrefix(doc, fn.Name.Name+" ") {
		// 以真实方法名开头，避免仅有参数列表或错贴别的方法说明仍被视为合格概述。
		problems = append(problems, "方法概述应以方法名开头")
	}
	for _, parameter := range fn.Type.Params.List {
		for _, name := range parameter.Names {
			// 空白标识符表示刻意未使用，不要求编造业务含义。
			if name.Name == "_" {
				continue
			}
			pattern := `(?:参数[^\S\r\n]+|@param[^\S\r\n]+)` + regexp.QuoteMeta(name.Name) + `[：:][^\S\r\n]*[^\s\r\n]`
			// 只出现名字或把多个参数合在一项不算逐一解释；冒号后必须有实际说明。
			if !regexp.MustCompile(pattern).MatchString(doc) {
				problems = append(problems, "缺少参数描述："+name.Name)
			}
		}
	}
	// 返回值存在时检查返回说明，错误边界由人工审查其业务语义是否充分。
	if fn.Type.Results != nil && !strings.Contains(doc, "返回") && !strings.Contains(doc, "@return") {
		problems = append(problems, "缺少返回值说明")
	}
	return problems
}
