package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"release-desk/internal/storage/workspace"
)

// TestLogConnectionSettingsKeepCredentialsPrivate 验证日志设置可从软件保存，密钥仅加密落盘且不回填页面。
// 参数 t：隔离数据目录与虚构凭据；不调用火山 API、不读取用户已有密钥。
func TestLogConnectionSettingsKeepCredentialsPrivate(t *testing.T) {
	s, err := workspace.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &App{Store: s}
	save, get := a.handlers()["logs/save"], a.handlers()["logs/settings"]
	if save == nil || get == nil {
		t.Fatal("日志页尚未提供软件内连接和密钥配置入口")
	}
	request := []byte(`{"endpoint":"https://tls-cn-guangzhou.volces.com","region":"cn-guangzhou","projects":{"prod":"production-fixture","test":"test-fixture"},"access_key":"QA_ACCESS_VALUE_NOT_REAL","secret_key":"QA_SECRET_VALUE_NOT_REAL"}`)
	if _, err = save(request); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(s.Dir, "log-connection.json"))
	if err != nil || bytes.Contains(raw, []byte("QA_ACCESS_VALUE_NOT_REAL")) || bytes.Contains(raw, []byte("QA_SECRET_VALUE_NOT_REAL")) {
		t.Fatal("日志连接未持久化，或将密钥以明文保存")
	}
	result, err := get(nil)
	public, _ := json.Marshal(result)
	if err != nil || bytes.Contains(public, []byte("QA_ACCESS_VALUE_NOT_REAL")) || bytes.Contains(public, []byte("QA_SECRET_VALUE_NOT_REAL")) || !bytes.Contains(public, []byte(`"configured":true`)) {
		t.Fatal("设置响应泄露密钥或没有已配置状态")
	}
	if _, err = save([]byte(`{"endpoint":"https://tls-cn-guangzhou.volces.com","region":"cn-guangzhou","projects":{"prod":"production-fixture"}}`)); err != nil {
		t.Fatal("留空没有保留已有密钥", err)
	}
	guide, err := a.handlers()["ai/onboarding"]([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(guide.(map[string]string)["guide"])
	if err != nil || strings.Contains(string(content), "QA_SECRET_VALUE_NOT_REAL") || strings.Contains(string(content), "QA_ACCESS_VALUE_NOT_REAL") {
		t.Fatal("接入指引读取了日志凭据")
	}
	if _, err = save([]byte(`{"endpoint":"https://outside.example.invalid","region":"cn-guangzhou","projects":{"prod":"production-fixture"}}`)); err == nil {
		t.Fatal("未允许的主机仍能接收日志密钥")
	}
}
