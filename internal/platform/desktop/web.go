package desktop

import (
	"errors"
	"net/url"

	"golang.org/x/sys/windows"
)

// OpenWebURL 通过默认浏览器打开软件更新的 GitHub HTTPS 页面。
// 参数 target：内部更新服务验证后的发布页或 EXE 下载链接；不接受其他协议、主机或账号信息。
// 返回：Shell 接收成功返回 nil；空链接、跨站或系统打开失败返回错误。
func OpenWebURL(target string) error {
	// 外部协议可能启动程序，限定为 GitHub HTTPS，不让业务请求变成任意 Shell 命令。
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil {
		return errors.New("没有可用的 GitHub 更新链接")
	}
	// Unicode 系统接口按完整 URL 传递，不做命令字符串拼接或参数解释。
	file, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	verb, _ := windows.UTF16PtrFromString("open")
	// 用户点击下载后由浏览器处理，不终止运行中的 SQL、AI 任务或替换现用 EXE。
	// Shell 失败沿调用链提示，不将“请求打开”误报成已经完成安装。
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}
