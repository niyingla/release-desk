package app

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"release-desk/internal/identity"
	"release-desk/internal/platform/desktop"
	"release-desk/internal/storage/workspace"
)

func TestNativeTrayMinimizeRestoreAndDirectExit(t *testing.T) {
	// 真正创建 Windows 窗口并发送关闭消息，不调用 Codex 或数据库。
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	class, _ := windows.UTF16PtrFromString("STATIC")
	title, _ := windows.UTF16PtrFromString("ReleaseDesk-Tray-QA-" + identity.NewID())
	create := desktop.DesktopUser32.NewProc("CreateWindowExW")
	hwnd, _, err := create.Call(0, uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(title)), 0x00cf0000, 100, 100, 320, 160, 0, 0, 0, 0)
	if hwnd == 0 {
		t.Fatalf("测试窗口创建失败：%v", err)
	}
	destroy := desktop.DesktopUser32.NewProc("DestroyWindow")
	defer destroy.Call(hwnd)
	closed := false
	tray, err := desktop.InstallDesktopTray(hwnd, func() { closed = true })
	if err != nil {
		t.Fatal(err)
	}
	defer tray.Close()
	send := desktop.DesktopUser32.NewProc("SendMessageW")
	isWindow := desktop.DesktopUser32.NewProc("IsWindow")
	visible := desktop.DesktopUser32.NewProc("IsWindowVisible")
	desktop.DesktopUser32.NewProc("ShowWindow").Call(hwnd, windows.SW_SHOWNOACTIVATE)
	send.Call(hwnd, 0x0112, 0xf020, 0)
	if valid, _, _ := isWindow.Call(hwnd); valid == 0 {
		t.Fatal("最小化销毁了原窗口")
	}
	if shown, _, _ := visible.Call(hwnd); shown != 0 {
		t.Fatal("点击最小化未收进系统托盘")
	}
	if !tray.Registered {
		t.Fatal("隐藏窗口时没有托盘图标")
	}
	// 用户可能同时开着正式程序；全局查找可以合法返回正式窗口。
	// 此用例只核对自己窗口的隐藏后标识，避免测试依赖用户软件是否退出。
	property, _ := windows.UTF16PtrFromString(desktop.DesktopWindowProperty)
	if marked, _, _ := desktop.DesktopUser32.NewProc("GetPropW").Call(hwnd, uintptr(unsafe.Pointer(property))); marked == 0 {
		t.Fatal("收进托盘后原窗口丢失专属标识")
	}
	// 通过真实 Windows 消息模拟点击托盘图标，不创建第二个窗口。
	send.Call(hwnd, desktop.TrayCallbackMessage, 0, 0x0202)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		shown, _, _ := visible.Call(hwnd)
		if shown != 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if shown, _, _ := visible.Call(hwnd); shown == 0 {
		t.Fatal("点击托盘没有恢复原窗口")
	}
	// × 直接走退出流程，不因为任务运行而隐藏或保持进程占用。
	send.Call(hwnd, 0x0010, 0, 0)
	if !closed {
		t.Fatal("关闭没有通知应用停止后台任务")
	}
	if valid, _, _ := isWindow.Call(hwnd); valid != 0 {
		t.Fatal("点击 × 未销毁窗口")
	}
	if tray.Registered {
		t.Fatal("退出后遗留托盘图标")
	}
}

func TestNamedInstanceMutexBlocksAndReleases(t *testing.T) {
	// 使用独立测试对象，不能占用用户正在运行的桌面窗口锁。
	name, _ := windows.UTF16PtrFromString(`Local\ReleaseDesk-QA-` + identity.NewID())
	first, err := desktop.CreateInstanceMutex(name)
	if err != nil {
		t.Fatal(err)
	}
	second, err := desktop.CreateInstanceMutex(name)
	if second != 0 || !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		t.Fatal("重复启动未被拒绝")
	}
	if err = windows.CloseHandle(first); err != nil {
		t.Fatal(err)
	}
	third, err := desktop.CreateInstanceMutex(name)
	if err != nil {
		t.Fatalf("退出后锁未释放：%v", err)
	}
	windows.CloseHandle(third)
}

func TestThemePersistsAcrossApplicationRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := workspace.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Theme != workspace.DefaultTheme {
		t.Fatal("首次启动默认主题错误")
	}
	a := &App{Store: s}
	if _, err = a.services().Settings.SaveTheme([]byte(`{"theme":"forest"}`)); err != nil {
		t.Fatal(err)
	}
	restarted, err := workspace.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Theme != "forest" {
		t.Fatal("主题未持久化")
	}
	if _, err = a.services().Settings.SaveTheme([]byte(`{"theme":"<script>"}`)); err == nil {
		t.Fatal("未知主题未拒绝")
	}
	if workspace.LoadTheme(dir) != "forest" {
		t.Fatal("无效请求覆盖已有偏好")
	}
	if err = os.WriteFile(filepath.Join(dir, "ui-preferences.json"), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if workspace.LoadTheme(dir) != workspace.DefaultTheme {
		t.Fatal("损坏偏好未安全回退")
	}
}
