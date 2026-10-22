package desktop

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const DesktopWindowTitle = "Release Desk · 版本发布工作台"

const DesktopInstanceName = `Global\ReleaseDesk.Desktop`

const DesktopActivationAttempts = 20

const DesktopActivationDelay = 100 * time.Millisecond

const DesktopWindowProperty = "ReleaseDesk.MainWindow"

var DesktopUser32 = windows.NewLazySystemDLL("user32.dll")

var EnumerateDesktopWindows = DesktopUser32.NewProc("EnumWindows")

var GetDesktopProperty = DesktopUser32.NewProc("GetPropW")

var RestoreDesktopWindow = DesktopUser32.NewProc("ShowWindowAsync")

var ForegroundDesktopWindow = DesktopUser32.NewProc("SetForegroundWindow")

var DesktopWindowIconic = DesktopUser32.NewProc("IsIconic")

// AcquireDesktopInstance 获取整台电脑共享的桌面窗口锁。
// 无业务入参；锁名不依赖 EXE 位置、数据目录或当前工作目录。
// 返回：首次启动获得句柄；重复启动返回 ERROR_ALREADY_EXISTS，其他错误原样返回。
func AcquireDesktopInstance() (windows.Handle, error) {
	// Global 命名空间覆盖同机 Windows 会话，避免换目录或复制 EXE 绕过限制。
	name, err := windows.UTF16PtrFromString(DesktopInstanceName)
	// 失败不能无锁继续，避免产生第二个执行入口。
	if err != nil {
		return 0, err
	}
	// 创建逻辑单独复用，可在测试中验证释放和重复启动行为。
	return CreateInstanceMutex(name)
}

// CreateInstanceMutex 创建保留到进程退出的命名互斥对象。
// 参数 name：内部生成的 UTF-16 对象名称，不能来自任意界面正文。
// 返回：新对象的持有句柄；已存在时关闭额外句柄并返回重复启动错误。
func CreateInstanceMutex(name *uint16) (windows.Handle, error) {
	// 句柄寿命覆盖窗口和后台任务，异常退出时操作系统自动释放。
	handle, err := windows.CreateMutex(nil, false, name)
	// CreateMutex 对已有对象仍可能返回句柄，因此要显式关闭第二份引用。
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) && handle != 0 {
		// 重复启动不能继续持有额外引用，避免原窗口退出后仍锁住应用。
		windows.CloseHandle(handle)
		return 0, err
	}
	return handle, err
}

// ActivateDesktopWindow 将重复启动转为显示已存在的窗口。
// 无业务入参；仅匹配本应用设置的专属属性，不读取其他窗口标题或内容。
// 返回：已找到并请求恢复窗口时为 true；无窗口时为 false，供启动流程判断退出交接。
func ActivateDesktopWindow() bool {
	// 首个进程可能刚获得互斥锁、尚未完成 WebView 初始化，短暂等待其窗口。
	for attempt := 0; attempt < DesktopActivationAttempts; attempt++ {
		hwnd := FindDesktopHandle()
		// 使用专属窗口标识，隐藏到托盘或标题发生变化后仍能准确定位。
		if hwnd != 0 {
			// 只有最小化窗口需要恢复，正常窗口保留原来的最大化与尺寸状态。
			mode := uintptr(windows.SW_SHOW)
			minimized, _, _ := DesktopWindowIconic.Call(hwnd)
			if minimized != 0 {
				mode = windows.SW_RESTORE
			}
			RestoreDesktopWindow.Call(hwnd, mode)
			// 恢复与置前分别处理，窗口原先最小化也可以回到可见状态。
			// 系统允许时置前；即使前台策略拒绝，也保持单窗口约束。
			ForegroundDesktopWindow.Call(hwnd)
			return true
		}
		// 等待只发生在重复启动进程，不阻塞已打开窗口及后台执行。
		time.Sleep(DesktopActivationDelay)
	}
	// 旧实例可能正在取消任务并退出，调用方需重新争取窗口锁而非静默丢失启动。
	return false
}

// FindDesktopHandle 查找设置了本应用专属属性的唯一主窗口。
// 无业务入参；只读取一个固定属性，不枚举用户窗口的业务内容。
// 返回：已标记主窗口句柄；窗口正在创建或已销毁时返回 0。
func FindDesktopHandle() uintptr {
	// 不依赖 FindWindow 按标题搜索，在隐藏窗口的系统环境下也保持一致。
	name, _ := windows.UTF16PtrFromString(DesktopWindowProperty)
	// 标识由主窗口创建入口设置，其他应用即使标题相同也不会被误激活。
	var result uintptr
	callback := windows.NewCallback(func(hwnd, _ uintptr) uintptr {
		// 查询仅检查标记是否存在，不取窗口名称、文本或其他用户信息。
		value, _, _ := GetDesktopProperty.Call(hwnd, uintptr(unsafe.Pointer(name)))
		if value != 0 {
			// 返回 0 立即停止枚举，整机互斥保证只有一个已标记的主窗口。
			result = hwnd
			return 0
		}
		return 1
	})
	// EnumWindows 同步调用，返回前回调已完成，不保存外部窗口引用。
	EnumerateDesktopWindows.Call(callback, 0)
	return result
}

// OpenPath 请求 Windows Shell 正常显示本应用生成的本地文件或目录。
// 参数 path：已经创建的受控本机路径，支持空格和中文，不接受命令行参数或 URL。
// 返回：Shell 接受打开请求时返回 nil；路径不存在、编码无效或系统拒绝打开时返回错误。
func OpenPath(path string) error {
	// 先检查实际目标，避免资源管理器悄悄退回默认主页而被误判为成功。
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("打开路径不存在或无法访问：%w", err)
	}
	absolute, err := filepath.Abs(path)
	// 绝对路径避免 Shell 按进程工作目录解释相对目录。
	if err != nil {
		return err
	}
	// 使用系统 Unicode 接口传递完整路径，中文和空格都不经过 shell 转义。
	file, err := windows.UTF16PtrFromString(absolute)
	if err != nil {
		return err
	}
	verb, _ := windows.UTF16PtrFromString("open")
	// 目录窗口是用户主动请求的交互界面，不能复用 Git/Codex 的隐藏进程启动方式。
	// Shell 会复用现有资源管理器，同时以正常可见方式展示目标目录。
	if err = windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL); err != nil {
		return fmt.Errorf("系统未能打开 %s：%w", absolute, err)
	}
	// 只记录受控路径，便于定位目录按钮问题，不记录任何文件正文。
	slog.Info("已请求打开本地路径", "path", absolute)
	return nil
}
