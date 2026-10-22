package process

import (
	"context"
	"os/exec"
	"syscall"
)

// Command 创建不经过 shell 的后台辅助进程。
// 参数 ctx：辅助进程或数据库操作的生命周期上下文，用于超时或取消。
// 参数 exe：已经发现的本机可执行文件路径，不包含用户业务指令。
// 参数 args：传给辅助程序的独立参数；不拼接成 shell 命令。
// 返回：返回绑定上下文的隐藏控制台命令对象；调用方决定 Start 或 Run。
func Command(ctx context.Context, exe string, args ...string) *exec.Cmd {
	// 子进程参数直接传递给系统，不经 shell 拼接。
	// 后台辅助进程隐藏控制台，避免界面闪窗。

	cmd := exec.CommandContext(ctx, exe, args...)

	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}
