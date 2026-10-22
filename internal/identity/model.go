package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
)

// NewID 生成不含业务隐私的随机标识。
// 无业务入参；使用接收对象或本机启动上下文，不隐式触发外部写操作。
// 返回：返回 16 字节随机值的十六进制文本；系统熵源失败时中止生成。
func NewID() string {
	// 随机 ID 用于记录与批次定位，不编码任何业务隐私。
	// 熵源失败时不能继续生成可能冲突的标识。

	b := make([]byte, 16)

	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// Digest 计算内容稳定摘要。
// 参数 s：参与摘要计算的完整文本，不作日志正文输出。
// 返回：返回完整文本的 SHA-256 十六进制摘要，用于内容一致性判断。
func Digest(s string) string {
	// 摘要用于比较内容是否改变。

	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
