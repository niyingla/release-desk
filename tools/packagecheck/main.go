// packagecheck 检查真实归档的路径清单，防止本机数据和业务示例进入公开制品。
package main

import (
	"archive/zip"
	"fmt"
	"os"
	"path"
	"strings"
)

// main 逐个检查构建脚本传入的源码包和安装包，任何不允许的条目都阻止发布。
// 无业务入参；归档路径来自构建脚本的位置参数，不解压文件或读取生产数据。
func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "需要指定待检查的 ZIP")
		os.Exit(1)
	}
	for _, file := range os.Args[1:] {
		// 只读取中央目录，不执行归档内容或展开到用户文件系统。
		if err := checkArchive(file); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	fmt.Println("公开包检查通过：不包含本机配置、业务示例或历史制作脚本")
}

// checkArchive 检查一个已经完成写入的公开 ZIP。
// 参数 file：构建生成的受控 ZIP 路径；不存在或损坏时返回错误。
// 返回：全部条目可公开返回 nil；存在不允许的路径时拒绝，不输出文件正文。
func checkArchive(file string) error {
	archive, err := zip.OpenReader(file)
	if err != nil {
		return err
	}
	// 统一释放文件句柄，后续复制发布包不会被检查器自身占用。
	defer archive.Close()
	for _, entry := range archive.File {
		if reason := privatePathReason(entry.Name); reason != "" {
			return fmt.Errorf("%s 含不允许的条目 %s：%s", path.Base(file), entry.Name, reason)
		}
	}
	return nil
}

// privatePathReason 以同一规则检查源码与安装包中的公开路径。
// 参数 name：ZIP 中的相对条目名称，目录项允许以斜杠结尾。
// 返回：允许时为空；否则返回不包含文件正文的拒绝原因。
func privatePathReason(name string) string {
	clean := path.Clean(strings.ReplaceAll(name, `\`, "/"))
	// 不接受绝对路径和目录穿越，即使检查器自身不会解压。
	if strings.HasPrefix(clean, "/") || clean == ".." || strings.HasPrefix(clean, "../") {
		return "不是受控相对路径"
	}
	for _, prefix := range []string{".work/", ".tools/", ".git/", "node_modules/", "tools/examples/"} {
		if strings.HasPrefix(clean, prefix) {
			return "本机数据、依赖或历史制作目录"
		}
	}
	// 只发布通用模板；真实业务版本即使没有凭据，也不能当成公共示例。
	if strings.HasPrefix(clean, "examples/") && clean != "examples/template.json" {
		return "非公开业务示例"
	}
	base := strings.ToLower(path.Base(clean))
	if base == "settings.json" || base == "ui-preferences.json" || base == ".env" || strings.HasPrefix(base, ".env.") || strings.HasSuffix(base, ".log") || strings.HasPrefix(base, "connection") && strings.HasSuffix(base, ".json") {
		return "本机设置、凭据或日志文件"
	}
	return ""
}
