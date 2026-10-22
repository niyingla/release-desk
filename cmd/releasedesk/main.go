// Release Desk 是离线优先的 Windows 版本发版工作台。
package main

import "release-desk/internal/app"

// main 将命令行和桌面生命周期交给应用层统一管理。
// 无参数与返回值；启动选项由应用层解析，失败会记录日志并提示。
func main() {
	// 入口不放业务逻辑，桌面、只读检查与界面测试共用同一个应用实现。
	app.Main()
}
