// commentcheck 统计业务 Go 方法体内的有效注释行，不把方法说明算入覆盖率。
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	// 工具链、测试和生成资源不纳入业务代码统计范围。
	files := []string{}
	// 业务源码统一在 cmd、internal、web，避免扫入工具链和历史测试产物。
	for _, root := range []string{"cmd", "internal", "web"} {
		if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() && strings.HasSuffix(path, ".go") {
				files = append(files, path)
			}
			return nil
		}); err != nil {
			panic(err)
		}
	}
	totalComments, totalCode := 0, 0
	violations := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		set := token.NewFileSet()
		file, err := parser.ParseFile(set, name, nil, parser.ParseComments)
		if err != nil {
			panic(err)
		}
		content, _ := os.ReadFile(name)
		lines := strings.Split(string(content), "\n")
		// 按方法分别统计，输出薄弱处，便于检查是否缺少业务边界说明。
		ast.Inspect(file, func(node ast.Node) bool {
			fn, ok := node.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				return true
			}
			begin, end := set.Position(fn.Body.Lbrace).Line, set.Position(fn.Body.Rbrace).Line
			commentLines := map[int]bool{}
			for _, group := range file.Comments {
				for _, comment := range group.List {
					first, last := set.Position(comment.Pos()).Line, set.Position(comment.End()).Line
					for line := first; line <= last; line++ {
						if line > begin && line < end {
							commentLines[line] = true
						}
					}
				}
			}
			code, comments := 0, 0
			for line := begin + 1; line < end; line++ {
				text := strings.TrimSpace(lines[line-1])
				if text == "" || text == "{" || text == "}" || text == "})" || text == "}()" {
					continue
				}
				if commentLines[line] {
					comments++
				} else {
					code++
				}
			}
			totalComments += comments
			totalCode += code
			ratio := 100.0
			if comments+code > 0 {
				ratio = 100 * float64(comments) / float64(comments+code)
			}
			if ratio < 30 {
				violations++
				fmt.Printf("LOW %s:%d %s comments=%d code=%d ratio=%.1f%%\n", name, begin, fn.Name.Name, comments, code, ratio)
			}
			return false
		})
	}
	// 覆盖率用注释 /（非空业务代码 + 注释），排除独立花括号与空行。
	fmt.Printf("TOTAL comments=%d code=%d coverage=%.2f%%\n", totalComments, totalCode, 100*float64(totalComments)/float64(totalComments+totalCode))
	// 逐方法未达标时让构建失败，不能只依赖整体平均值。
	if violations > 0 {
		os.Exit(1)
	}
}
