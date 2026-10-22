package desktop

import (
	"errors"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	GlobalMemoryMoveable   = 0x0002
	ClipboardUnicodeText   = 13
	clipboardOpenTimeout   = time.Second
	clipboardRetryInterval = 25 * time.Millisecond
)

// 同一进程的复制请求串行进入原生临界区，避免连续双击互相争用。
var clipboardMu sync.Mutex

var ClipboardAPI = struct{ open, close, empty, set, allocate, lock, unlock, free, copy *windows.LazyProc }{
	windows.NewLazySystemDLL("user32.dll").NewProc("OpenClipboard"),
	windows.NewLazySystemDLL("user32.dll").NewProc("CloseClipboard"),
	windows.NewLazySystemDLL("user32.dll").NewProc("EmptyClipboard"),
	windows.NewLazySystemDLL("user32.dll").NewProc("SetClipboardData"),
	windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalAlloc"),
	windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalLock"),
	windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalUnlock"),
	windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalFree"),
	windows.NewLazySystemDLL("kernel32.dll").NewProc("RtlMoveMemory"),
}

// WriteClipboard 复制完整 Unicode 文本到 Windows 剪贴板。
// 参数 text：待处理的完整原文；保留中文和换行，不输出到应用日志。
// 返回：成功返回 nil；剪贴板占用、内存分配或写入失败时返回可重试错误。
func WriteClipboard(text string) error {
	// Windows 剪贴板由打开它的线程持有，关闭前不能让 Go 把协程调度到另一线程。
	clipboardMu.Lock()
	defer clipboardMu.Unlock()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// 复制的是单个字段的完整原文。
	// 不把表格省略显示当作实际复制值。
	// Windows 使用 UTF-16 表示中文和换行。
	// 嵌入零字符不能写为完整文本，因此明确报错。
	// 剪贴板可能短暂被其他程序占用，进行有界重试。
	// 重试不会无限阻塞界面。
	// 内存由应用申请，只有写入成功才转交系统。
	// 锁定全局内存后才能写入内容。
	// 申请或锁定失败需释放应用持有的资源。
	// 写入前清空旧格式，保证其他程序读取 Unicode 文本。

	// UTF-16 包含末尾零字符，完整保留中文、换行和 SQL 中的特殊字符。

	value, err := syscall.UTF16FromString(text)
	if err != nil {
		return errors.New("内容中包含无法复制的零字符")
	}

	opened := false
	deadline := time.Now().Add(clipboardOpenTimeout)
	for time.Now().Before(deadline) {
		result, _, _ := ClipboardAPI.open.Call(0)
		if result != 0 {
			opened = true
			break
		}
		time.Sleep(clipboardRetryInterval)
	}

	if !opened {
		return errors.New("剪贴板正被其他程序占用，请重试")
	}

	// 关闭必须先于解除线程绑定与释放本进程复制锁，避免留下跨请求的原生占用。
	defer ClipboardAPI.close.Call()

	// 剪贴板采用可移动全局内存；成功交给系统后由系统负责释放。
	handle, _, _ := ClipboardAPI.allocate.Call(GlobalMemoryMoveable, uintptr(len(value)*2))

	if handle == 0 {
		return errors.New("剪贴板内存申请失败")
	}
	pointer, _, _ := ClipboardAPI.lock.Call(handle)

	if pointer == 0 {
		ClipboardAPI.free.Call(handle)
		return errors.New("剪贴板内存锁定失败")
	}

	// 原生内存复制避免把 Win32 句柄转换成 Go 指针，源切片保持存活至调用结束。
	ClipboardAPI.copy.Call(pointer, uintptr(unsafe.Pointer(&value[0])), uintptr(len(value)*2))
	runtime.KeepAlive(value)
	ClipboardAPI.unlock.Call(handle)

	ClipboardAPI.empty.Call()
	result, _, _ := ClipboardAPI.set.Call(ClipboardUnicodeText, handle)

	if result == 0 {
		ClipboardAPI.free.Call(handle)
		return errors.New("写入剪贴板失败")
	}
	return nil
}
