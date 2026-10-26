package desktop

// WM_SETICON 分别设置标题栏小图标与任务切换大图标，不修改其他窗口的类资源。
const (
	windowSetIconMessage = 0x0080
	windowSmallIcon      = 0
	windowLargeIcon      = 1
)

// SetWindowIcons 将主窗口图标与 EXE、桌面及托盘使用的应用资源保持一致。
// 参数 hwnd：本应用拥有的主窗口句柄；由所属 UI 线程在窗口销毁前调用。
// 参数 icon：借用的图标句柄；0 表示解绑，调用方必须在解绑后才释放拥有的图标。
// 不返回业务值；本方法不取得句柄所有权，也不销毁原先的系统共享图标。
func SetWindowIcons(hwnd, icon uintptr) {
	// WM_SETICON 返回原图标，不能把首次设置时返回 0 当作失败。
	send := DesktopUser32.NewProc("SendMessageW")
	// 小图标用于标题栏；仅打包 ICO 并不能为 WebView 创建的窗口设置它。
	send.Call(hwnd, windowSetIconMessage, windowSmallIcon, icon)
	// Alt+Tab 和任务栏优先使用大图标，与桌面展示复用同一个 EXE 资源。
	send.Call(hwnd, windowSetIconMessage, windowLargeIcon, icon)
}
