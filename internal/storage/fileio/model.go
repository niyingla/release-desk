package fileio

import (
	"encoding/json"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"

	"release-desk/internal/identity"
)

// AtomicWrite 在同目录内原子替换完整文件。
// 参数 path：本方法要读取、写入或打开的受控本机路径；不得由业务正文拼接任意系统路径。
// 参数 payload：待落盘的完整字节内容；只有写入与同步都成功才替换旧文件。
// 返回：成功返回 nil；任一创建、写入、同步或替换失败时返回错误并清理临时文件。
func AtomicWrite(path string, payload []byte) error {
	// 临时文件与目标文件位于同一目录。
	// 先创建父目录，首次使用无需手工准备。
	// 使用唯一临时名，避免并发请求互相覆盖。
	// 数据先写完整再同步到磁盘。
	// 写入或同步失败时不替换原文件。
	// Windows 使用带替换语义的原子移动。
	// 失败临时文件在返回时清理。
	// 目标文件不会暴露只写了一半的内容。

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp := path + "." + identity.NewID() + ".tmp"

	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)

	if err != nil {
		return err
	}
	defer os.Remove(tmp)

	if _, err = f.Write(payload); err != nil {
		f.Close()
		return err
	}

	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}

	src, _ := windows.UTF16PtrFromString(tmp)

	dst, _ := windows.UTF16PtrFromString(path)
	return windows.MoveFileEx(src, dst, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

// WriteJSON 以可读格式原子保存 JSON 对象。
// 参数 path：本方法要读取、写入或打开的受控本机路径；不得由业务正文拼接任意系统路径。
// 参数 value：参与操作的业务值；要求与当前有限集合或快照类型一致。
// 返回：成功返回 nil；编码或原子写入失败时返回错误且保留原文件。
func WriteJSON(path string, value any) error {
	// 统一使用可读缩进格式，方便 AI 与用户复核。
	// 序列化成功后才调用原子文件替换。

	b, e := json.MarshalIndent(value, "", "  ")

	if e != nil {
		return e
	}
	return AtomicWrite(path, b)
}
