package desktop

import (
	"fmt"
	"log/slog"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows 消息与通知区协议值集中定义，避免在窗口回调中散布魔数。
const (
	TrayCallbackMessage     = 0x8000 + 17
	WindowSystemCommand     = 0x0112
	WindowCloseMessage      = 0x0010
	WindowFinalDestroy      = 0x0082
	WindowMinimizeCommand   = 0xf020
	WindowNonClientLeftDown = 0x00a1
	WindowMinimizeHit       = 8
	TrayLeftButtonUp        = 0x0202
	TrayLeftDoubleClick     = 0x0203
	TrayRightButtonUp       = 0x0205
	TrayContextMenu         = 0x007b
	WindowNullMessage       = 0x0000
	TrayMenuRestore         = 1
	TrayMenuExit            = 2
	MenuReturnCommand       = 0x0100
	MenuRightButton         = 0x0002
	TrayAdd                 = 0
	TrayDelete              = 2
	TrayFlags               = 1 | 2 | 4
	TrayIconID              = 1
)

// notifyIconData 与 Windows NOTIFYICONDATAW 字段布局一致，使用 Unicode 提示文字。
type NotifyIconData struct {
	Size        uint32
	Window      uintptr
	ID          uint32
	Flags       uint32
	Message     uint32
	Icon        uintptr
	Tip         [128]uint16
	State       uint32
	StateMask   uint32
	Info        [256]uint16
	Version     uint32
	InfoTitle   [64]uint16
	InfoFlags   uint32
	GUID        windows.GUID
	BalloonIcon uintptr
}

type DesktopTray struct {
	Window                 uintptr
	Callback               uintptr
	TaskbarMessage         uintptr
	Data                   NotifyIconData
	Registered             bool
	OwnedIcon              bool
	OnExit                 func()
	CaptionMinimizePending bool
}

var TrayShell = windows.NewLazySystemDLL("shell32.dll")

var NotifyTrayIcon = TrayShell.NewProc("Shell_NotifyIconW")

var TrayControls = windows.NewLazySystemDLL("comctl32.dll")

var SetTraySubclass = TrayControls.NewProc("SetWindowSubclass")

var RemoveTraySubclass = TrayControls.NewProc("RemoveWindowSubclass")

var DefaultTraySubclass = TrayControls.NewProc("DefSubclassProc")

var ShowDesktopWindow = DesktopUser32.NewProc("ShowWindow")

// InstallDesktopTray 接管最小化消息并安装系统托盘入口。
// 参数 hwnd：本应用创建的主窗口句柄；必须在所属 UI 线程调用。
// 参数 onExit：用户点击 × 时请求取消后台任务的短回调，不阻塞窗口消息循环。
// 返回：托盘控制对象；窗口消息钩子安装失败时返回错误，不能隐藏无恢复入口的窗口。
func InstallDesktopTray(hwnd uintptr, onExit func()) (*DesktopTray, error) {
	// 子类化只扩展本应用窗口，不替换 WebView 自身的键盘、大小和绘制处理。
	t := &DesktopTray{Window: hwnd, OnExit: onExit}
	// 专属标识供第二次启动查找主窗口，不依赖可见状态或窗口标题。
	property, _ := windows.UTF16PtrFromString(DesktopWindowProperty)
	marked, _, markErr := DesktopUser32.NewProc("SetPropW").Call(hwnd, uintptr(unsafe.Pointer(property)), 1)
	if marked == 0 {
		return nil, fmt.Errorf("无法标记主窗口：%w", markErr)
	}
	// 回调由 Windows 持有，生命周期与主窗口一致，不能使用会失效的临时函数指针。
	t.Callback = windows.NewCallback(t.HandleMessage)
	// 钩子安装失败不能继续创建一个无法正确处理最小化与退出的隐藏窗口。
	result, _, err := SetTraySubclass.Call(hwnd, t.Callback, TrayIconID, 0)
	if result == 0 {
		return nil, fmt.Errorf("无法安装托盘窗口处理：%w", err)
	}
	// 通知区结构的字节数按本机架构计算，不能固定成 32 位或 64 位的魔数。
	// 图标优先使用 EXE 中已打包的应用图标，不依赖外部资源文件。
	t.Data = NotifyIconData{Window: hwnd, ID: TrayIconID, Flags: TrayFlags, Message: TrayCallbackMessage}
	t.Data.Size = uint32(unsafe.Sizeof(t.Data))
	t.Data.Icon, t.OwnedIcon = LoadTrayIcon()
	// 主窗口显式绑定同一份 EXE 图标，使标题栏和 Alt+Tab 与桌面、托盘一致。
	SetWindowIcons(hwnd, t.Data.Icon)
	slog.Debug("主窗口已同步应用图标")
	// 提示只说明操作用途，不把当前版本或数据库信息放到系统通知区。
	tip, _ := windows.UTF16FromString("Release Desk · 点击恢复；右键菜单可退出")
	copy(t.Data.Tip[:], tip)
	// Explorer 重启后会重新广播此消息，已隐藏的主窗口仍需恢复入口。
	name, _ := windows.UTF16PtrFromString("TaskbarCreated")
	t.TaskbarMessage, _, _ = DesktopUser32.NewProc("RegisterWindowMessageW").Call(uintptr(unsafe.Pointer(name)))
	// 注册失败仅影响托盘能力；窗口仍可保持普通任务栏最小化，不能直接消失。
	if !t.Add() {
		slog.Warn("托盘图标暂不可用，最小化将保留任务栏入口")
	}
	return t, nil
}

// LoadTrayIcon 加载内嵌应用图标或 Windows 默认图标。
// 无业务入参；只从当前 EXE 资源读取图标，不访问用户业务文件。
// 返回：图标句柄与是否由调用方负责释放的标记；共享默认图标不可销毁。
func LoadTrayIcon() (uintptr, bool) {
	// ExtractIcon 读取当前 EXE 第一组图标，资源重编号时仍能正确加载。
	exe, err := os.Executable()
	if err == nil {
		// 仅当前程序路径进入系统资源接口，不接受版本文件中的任意路径。
		path, _ := windows.UTF16PtrFromString(exe)
		icon, _, _ := TrayShell.NewProc("ExtractIconW").Call(0, uintptr(unsafe.Pointer(path)), 0)
		if icon > 1 {
			return icon, true
		}
		// 0 表示没有图标，1 表示目标并非可提取图标的模块，均使用系统回退。
	}
	// EXE 无图标时仍提供恢复入口；此系统共享句柄不归本应用释放。
	icon, _, _ := DesktopUser32.NewProc("LoadIconW").Call(0, 32512)
	return icon, false
}

// Add 向系统通知区注册恢复图标。
// 无业务入参；使用该主窗口固定的托盘数据。
// 返回：注册成功为 true；失败时禁止把窗口隐藏到不可达状态。
func (t *DesktopTray) Add() bool {
	// 已注册时无需重复添加，避免连续最小化生成重复图标。
	if t.Registered {
		return true
	}
	result, _, _ := NotifyTrayIcon.Call(TrayAdd, uintptr(unsafe.Pointer(&t.Data)))
	// 记录 Shell 的实际接受结果，隐藏动作必须以此为前提。
	t.Registered = result != 0
	// 调用方据此决定隐藏还是保留任务栏，不把发出请求当作注册成功。
	return t.Registered
}

// Close 移除托盘图标并释放本应用拥有的图标资源。
// 无业务入参；可在 WM_NCDESTROY 和退出收尾中重复调用。
// 不返回业务值；系统通知区图标和句柄在应用退出前清理。
func (t *DesktopTray) Close() {
	// 重复关闭保持幂等，原生销毁通知和 defer 都可以安全清理。
	if t.Registered {
		NotifyTrayIcon.Call(TrayDelete, uintptr(unsafe.Pointer(&t.Data)))
		t.Registered = false
	}
	// 系统共享图标不能销毁，只有 ExtractIcon 创建的句柄需要释放。
	if t.OwnedIcon {
		// 窗口与托盘共用该句柄，释放前先解绑，避免窗口引用已销毁的资源。
		SetWindowIcons(t.Window, 0)
		DesktopUser32.NewProc("DestroyIcon").Call(t.Data.Icon)
		t.OwnedIcon = false
		// 清除所有权标记，后续销毁消息不能再次释放同一个句柄。
	}
}

// ShowContextMenu 在当前鼠标位置显示恢复、退出菜单。
// 无业务入参；必须由托盘鼠标消息在窗口所属 UI 线程调用。
// 不返回业务值；退出复用 × 的关闭流程，保留后台任务取消和审计收尾。
func (t *DesktopTray) ShowContextMenu() {
	// 每次弹出独立菜单，选择或取消后统一释放，避免重复右键泄漏句柄。
	menu, _, err := DesktopUser32.NewProc("CreatePopupMenu").Call()
	if menu == 0 {
		// 系统资源不足时只记录原因，不让右键消息导致主窗口崩溃。
		slog.Warn("无法创建托盘菜单", "error", err)
		return
	}
	defer DesktopUser32.NewProc("DestroyMenu").Call(menu)
	// Unicode 菜单标签通过 UTF-16 传递，避免不同 Windows 区域设置造成乱码。
	// 菜单命令只接受本应用定义的编号，不把任意菜单消息解释为退出。
	for _, entry := range []struct {
		id    uintptr
		label string
	}{{TrayMenuRestore, "打开主窗口"}, {TrayMenuExit, "退出软件"}} {
		label, _ := windows.UTF16PtrFromString(entry.label)
		if ok, _, _ := DesktopUser32.NewProc("AppendMenuW").Call(menu, 0, entry.id, uintptr(unsafe.Pointer(label))); ok == 0 {
			// 构建不完整时不显示半个菜单，保留左击恢复和 × 退出入口。
			slog.Warn("无法添加托盘菜单项", "command", entry.id)
			return
		}
	}
	var point struct{ X, Y int32 }
	// 使用屏幕坐标而非客户区坐标，多显示器左侧屏幕允许负值。
	if ok, _, _ := DesktopUser32.NewProc("GetCursorPos").Call(uintptr(unsafe.Pointer(&point))); ok == 0 {
		// 未获得有效位置时不把菜单放在错误屏幕的原点。
		return
	}
	// 先激活所有者，点击菜单外部才能正确关闭原生弹出菜单。
	ForegroundDesktopWindow.Call(t.Window)
	// 返回命令模式由此处统一派发，取消菜单返回零，不应触发任何动作。
	command, _, _ := DesktopUser32.NewProc("TrackPopupMenu").Call(menu, MenuReturnCommand|MenuRightButton, uintptr(point.X), uintptr(point.Y), 0, t.Window, 0)
	// Windows 托盘菜单约定：投递空消息，确保取消后下次右键仍正常响应。
	DesktopUser32.NewProc("PostMessageW").Call(t.Window, WindowNullMessage, 0, 0)
	switch command {
	case TrayMenuRestore:
		// 复用原窗口与 WebView 会话，恢复后正在进行的任务仍属于同一实例。
		ShowDesktopWindow.Call(t.Window, windows.SW_RESTORE)
		ForegroundDesktopWindow.Call(t.Window)
	case TrayMenuExit:
		// 异步投递与 × 相同的消息，不直接杀进程或绕开保存流程。
		slog.Info("用户从托盘请求退出")
		DesktopUser32.NewProc("PostMessageW").Call(t.Window, WindowCloseMessage, 0, 0)
	}
}

// HandleMessage 按 Windows 消息区分托盘最小化、恢复和真正退出。
// 参数 hwnd：消息所属的本应用主窗口。
// 参数 message：Windows 消息标识，非本功能消息交回 WebView 原处理器。
// 参数 wParam：系统命令或图标标识，最小化时只比较标准命令部分。
// 参数 lParam：通知区回调中的鼠标事件，其他消息按原值透传。
// 参数 subclassID：系统保存的子类标识，仅用于卸载该钩子。
// 参数 reference：系统保留参数，本实现不使用、不解释为业务数据。
// 返回：已消费消息返回 0；其他消息返回原窗口处理器结果。
func (t *DesktopTray) HandleMessage(hwnd, message, wParam, lParam, subclassID, reference uintptr) uintptr {
	// 标题栏按钮由 DefSubclassProc 跟踪鼠标释放并同步发出系统命令。
	// 仅在该调用期间标记来源；移出按钮或取消点击后不能污染下一次任务栏操作。
	if message == WindowNonClientLeftDown && wParam == WindowMinimizeHit {
		t.CaptionMinimizePending = true
		defer func() { t.CaptionMinimizePending = false }()
		result, _, _ := DefaultTraySubclass.Call(hwnd, message, wParam, lParam)
		return result
	}
	// 右键只打开原生菜单，不能先恢复窗口或直接中断正在运行的任务。
	if message == TrayCallbackMessage && (lParam == TrayRightButtonUp || lParam == TrayContextMenu) {
		t.ShowContextMenu()
		return 0
	}
	// 只有标题栏“－”且图标注册成功才隐藏；任务栏或键盘最小化交回 Windows。
	if message == WindowSystemCommand && wParam&0xfff0 == WindowMinimizeCommand && t.CaptionMinimizePending && t.Add() {
		// 来源只消费一次，恢复后点击任务栏不得继续沿用旧标记。
		t.CaptionMinimizePending = false
		// SW_HIDE 去掉任务栏按钮；恢复入口保留在右下角通知区。
		ShowDesktopWindow.Call(hwnd, windows.SW_HIDE)
		slog.Info("主窗口已收进系统托盘")
		return 0
	}
	// 左击或双击通知区图标都恢复同一窗口，不创建新 WebView 或新进程。
	if message == TrayCallbackMessage && (lParam == TrayLeftButtonUp || lParam == TrayLeftDoubleClick) {
		// 恢复使用当前句柄，因此不会突破整机单窗口约束。
		ShowDesktopWindow.Call(hwnd, windows.SW_RESTORE)
		ForegroundDesktopWindow.Call(hwnd)
		return 0
	}
	// × 始终直接退出；取消请求先发出，让数据库和 Codex 有机会记录中断。
	if message == WindowCloseMessage && t.OnExit != nil {
		// 退出回调只发出取消信号，实际审计收尾在消息循环退出后执行。
		t.OnExit()
	}
	if message == WindowFinalDestroy {
		// 销毁时先移除图标再卸载钩子，避免残留一个点击无反应的托盘项。
		t.Close()
		// 销毁时清理标识，下一次启动只能找到新建的主窗口。
		property, _ := windows.UTF16PtrFromString(DesktopWindowProperty)
		DesktopUser32.NewProc("RemovePropW").Call(hwnd, uintptr(unsafe.Pointer(property)))
		RemoveTraySubclass.Call(hwnd, t.Callback, subclassID)
	}
	// Explorer 重建通知区时重新注册，避免已收起的主窗口失去图标。
	if t.TaskbarMessage != 0 && message == t.TaskbarMessage {
		// Explorer 重启后旧注册状态失效，必须真正重新调用 Shell 添加图标。
		t.Registered = false
		t.Add()
	}
	result, _, _ := DefaultTraySubclass.Call(hwnd, message, wParam, lParam)
	return result
}
