package process

import (
	"fmt"
	"os/exec"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const ManagedPipeCloseGrace = time.Second

// RunManagedCommand 运行随本次任务收尾的 CLI 进程树。
// 参数 cmd：由参数数组和可取消上下文创建的命令，不接受 shell 拼接正文。
// 返回：进程退出或 Windows 作业对象管理错误；返回前释放本轮派生进程。
func RunManagedCommand(cmd *exec.Cmd) error {
	// 作业对象只包含本次 CLI 及其子进程，不能终止用户其他 Codex 对话。
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("无法管理后台进程：%w", err)
	}
	defer windows.CloseHandle(job)
	// 作业句柄不传给子进程，当前调用结束时不会被子进程继续持有。
	// 关闭最后一个句柄时系统回收 CLI 派生的 MCP 等进程，避免孤儿进程占用。
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	// 限制设置成功后才允许启动，不能产生一个退出时无法回收的后台任务。
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return err
	}
	// 即使外部工具继承了输出管道，取消后也不能无限等待管道关闭。
	cmd.WaitDelay = ManagedPipeCloseGrace
	if err = cmd.Start(); err != nil {
		return err
	}
	// 命令使用窗口共享的取消上下文，× 退出能中止正在等待模型返回的 CLI。
	err = AssignCommandJob(job, uint32(cmd.Process.Pid))
	// 绑定失败不能继续留下无法取消的 CLI；先回收进程再返回管理错误。
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return fmt.Errorf("无法绑定后台进程：%w", err)
	}
	// 正常结束同样关闭作业句柄，本轮工具派生进程不应继续驻留。
	return cmd.Wait()
}

// AssignCommandJob 将本轮命令加入可整体回收的进程作业。
// 参数 job：已经设置退出回收限制的本轮作业句柄。
// 参数 pid：刚启动的 CLI 进程 ID，不枚举或接管用户其他进程。
// 返回：绑定成功返回 nil；权限或进程绑定失败时返回系统错误。
func AssignCommandJob(job windows.Handle, pid uint32) error {
	// 只申请绑定作业所需的配额与终止权限，不读取外部进程内存。
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return err
	}
	// 本地进程句柄不参与长期持有，绑定完成后由 job 管理整个进程树。
	defer windows.CloseHandle(process)
	// 只建立本轮生命周期关系，不修改系统级进程或机器配置。
	return windows.AssignProcessToJobObject(job, process)
}
