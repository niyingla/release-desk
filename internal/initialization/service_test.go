package initialization

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"release-desk/internal/storage/workspace"
)

// TestSendUsesFreshTokenAndDoesNotPersistIt 验证用户手动单次调用、临时 Token、响应脱敏及完成状态不自动推断。
// 参数 t：Go 测试上下文；仅访问本机 httptest 服务，绝不请求用户的真实环境。
func TestSendUsesFreshTokenAndDoesNotPersistIt(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/spapi/maintenance/rebuild" || r.Header.Get("Authorization") != "Bearer fake-per-call-token" {
			t.Error("路由或 Authorization 发生变化")
		}
		payload, _ := io.ReadAll(r.Body)
		if string(payload) != `{"scope":"demo"}` {
			t.Error("请求参数被修改")
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"echo":"fake-per-call-token","success":false}`))
	}))
	defer server.Close()
	s, err := workspace.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	config := s.Config()
	config.InitializationBaseURLs["test"]["服务商端"] = server.URL + "/spapi"
	s.Settings = config
	item := workspace.Item{ID: "call", Kind: "init_api", Data: map[string]string{"name": "QA 初始化", "side": "服务商端", "method": "POST", "url": "/maintenance/rebuild", "parameter_mode": "必填参数", "body": `{"scope":""}`}}
	v := workspace.VersionFile{Format: "release-desk-version", Schema: 1, Version: workspace.Version{Name: "qa-call"}, Items: []workspace.Item{item}}
	if err = s.Save(v); err != nil {
		t.Fatal(err)
	}
	svc := &Service{Store: s}
	input := Request{Version: "qa-call", ID: "call", Environment: "test", Token: "fake-per-call-token", Body: `{"scope":"demo"}`, Digest: workspace.VerificationHash(s.Versions["qa-call"].Items[0]), BaseURL: server.URL + "/spapi"}
	data, _ := json.Marshal(input)
	response, err := svc.Send(data)
	if err != nil {
		t.Fatal(err)
	}
	result := response.(Result)
	if calls.Load() != 1 || result.Status != 200 || strings.Contains(result.Body, input.Token) || s.Versions["qa-call"].Items[0].Status != "待处理" {
		t.Fatal("重复发送、Token 回显或自动标记了完成")
	}
	input.Token = ""
	data, _ = json.Marshal(input)
	if _, err = svc.Send(data); err == nil || calls.Load() != 1 {
		t.Fatal("未重新填写 Token 仍可调用")
	}
	// 记录或域名配置变化后旧确认不能继续发送，且不触及目标服务器。
	input.Token = "fake-per-call-token"
	input.BaseURL = "https://previous.example.invalid"
	data, _ = json.Marshal(input)
	if _, err = svc.Send(data); err == nil || calls.Load() != 1 {
		t.Fatal("环境入口变更后未重新确认")
	}
	input.BaseURL = server.URL + "/spapi"
	input.Digest = "previous-item-snapshot"
	data, _ = json.Marshal(input)
	if _, err = svc.Send(data); err == nil || calls.Load() != 1 {
		t.Fatal("事项变更后旧弹窗仍可发送")
	}
	// 参数及 Token 仅存在请求内存，逐个检查真正落盘的临时文件。
	if err = filepath.WalkDir(s.Dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		if strings.Contains(string(b), "fake-per-call-token") {
			t.Error("Token 被保存到本机记录")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestSendDoesNotFollowRedirects 验证单次手动请求不会跟随服务端跳转重放初始化动作。
// 参数 t：Go 测试上下文；只访问本机模拟服务器。
func TestSendDoesNotFollowRedirects(t *testing.T) {
	var calls atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		payload, _ := io.ReadAll(r.Body)
		if r.URL.RawQuery != "" || len(payload) != 0 {
			t.Error("无参调用发送了残留参数")
		}
		w.Header().Set("Location", server.URL+"/destination")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()
	store, err := workspace.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store.Settings.InitializationBaseURLs["test"]["租户端"] = server.URL
	item := workspace.Item{ID: "redirect", Kind: "init_api", Data: map[string]string{"name": "QA redirect", "side": "租户端", "method": "GET", "url": "/redirect", "parameter_mode": "无参"}}
	if err = store.Save(workspace.VersionFile{Format: "release-desk-version", Schema: 1, Version: workspace.Version{Name: "qa-redirect"}, Items: []workspace.Item{item}}); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(Request{Version: "qa-redirect", ID: "redirect", Environment: "test", Token: "fake-redirect-token", Params: `{"stale":"must-not-send"}`, Body: `{"stale":true}`, Digest: workspace.VerificationHash(store.Versions["qa-redirect"].Items[0]), BaseURL: server.URL})
	response, err := (&Service{Store: store}).Send(input)
	if err != nil || response.(Result).Status != http.StatusFound || calls.Load() != 1 {
		t.Fatalf("跟随了跳转或自动重发：%v / calls=%d", err, calls.Load())
	}
}
