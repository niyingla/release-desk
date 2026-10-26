package desktop

import (
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// TestWindowIconsMatchApplication 验证主窗口大小图标共用传入的应用资源。
// 参数 t：测试上下文；只创建本测试拥有的不可见窗口，不操作用户窗口。
func TestWindowIconsMatchApplication(t *testing.T) {
	// 原生窗口的创建、消息与销毁留在同一线程，避免测试框架切换线程。
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	class, err := windows.UTF16PtrFromString("STATIC")
	if err != nil {
		t.Fatal(err)
	}
	// STATIC 为系统内置类；没有 WS_VISIBLE，不弹出窗口或占用桌面单实例。
	hwnd, _, createErr := DesktopUser32.NewProc("CreateWindowExW").Call(0, uintptr(unsafe.Pointer(class)), 0, 0, 0, 0, 16, 16, 0, 0, 0, 0)
	if hwnd == 0 {
		t.Fatal(createErr)
	}
	defer DesktopUser32.NewProc("DestroyWindow").Call(hwnd)
	// 测试 EXE 没有应用资源，使用系统共享图标验证句柄传递，不销毁共享资源。
	icon, _, _ := DesktopUser32.NewProc("LoadIconW").Call(0, 32512)
	if icon == 0 {
		t.Fatal("无法加载测试共享图标")
	}
	SetWindowIcons(hwnd, icon)
	for _, size := range []uintptr{0, 1} {
		// WM_GETICON 直接核对实际窗口，防止只修改托盘或返回预设结果。
		got, _, _ := DesktopUser32.NewProc("SendMessageW").Call(hwnd, 0x007f, size, 0)
		if got != icon {
			t.Fatalf("窗口图标未同步 size=%d", size)
		}
	}
	// 释放拥有的资源前必须允许解绑，不能让窗口引用已经销毁的图标。
	SetWindowIcons(hwnd, 0)
	for _, size := range []uintptr{0, 1} {
		got, _, _ := DesktopUser32.NewProc("SendMessageW").Call(hwnd, 0x007f, size, 0)
		if got != 0 {
			t.Fatalf("图标解绑失败 size=%d", size)
		}
	}
}
