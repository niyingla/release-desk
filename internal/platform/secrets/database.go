package secrets

import (
	"encoding/base64"
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Protect 通过 Windows DPAPI 加密或解密凭据。
// 参数 text：待处理的完整原文；保留中文和换行，不输出到应用日志。
// 参数 decrypt：是否解密；false 为当前 Windows 用户加密，true 为解密。
// 返回：加密时返回 Base64 DPAPI 密文，解密时返回原文；系统或编码失败时返回错误。
func Protect(text string, decrypt bool) (string, error) {
	// 保护范围绑定当前 Windows 用户。
	// 密文不能直接在另一账号下复用。
	// 空密码按空值处理，不额外产生虚假密文。
	// 解密前先恢复 DPAPI 所需的二进制数据。
	// 明文只存在于调用进程内存。
	// 不依赖应用源码内置的固定加密密钥。
	// DPAPI 错误向用户提示重新配置，不输出原值。
	// 系统分配的解密结果必须释放。
	// 输出密文使用 Base64 便于 JSON 安全保存。

	raw := []byte(text)

	var err error
	if decrypt {
		raw, err = base64.StdEncoding.DecodeString(text)
		if err != nil {
			return "", err
		}
	}

	if len(raw) == 0 {
		return "", nil
	}

	in := windows.DataBlob{Size: uint32(len(raw)), Data: &raw[0]}
	out := windows.DataBlob{}

	if decrypt {
		err = windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	} else {
		err = windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	}

	if err != nil {
		return "", errors.New("本机凭据解密失败，请重新设置测试数据库连接")
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))

	bytes := unsafe.Slice(out.Data, int(out.Size))

	if decrypt {
		return string(bytes), nil
	}
	return base64.StdEncoding.EncodeToString(bytes), nil
}
