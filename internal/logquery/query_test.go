package logquery

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/volcengine/volc-sdk-golang/service/tls"
	"release-desk/internal/platform/secrets"
	"release-desk/internal/storage/fileio"
	"release-desk/internal/storage/workspace"
)

// TestQueryUsesFixedTopicWindowAndRedactsSamples 验证真实 SDK 请求保留固定毫秒窗口、项目归属与采样/总量区分。
// 参数 t：本地 HTTPS 服务与虚构凭据，不访问真实火山接口或业务日志。
func TestQueryUsesFixedTopicWindowAndRedactsSamples(t *testing.T) {
	var searches atomic.Int32
	var request map[string]any
	project := "project-prod"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "HMAC-SHA256 ") {
			t.Error("未使用官方 SDK 签名")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/DescribeTopic":
			_ = json.NewEncoder(w).Encode(map[string]any{"TopicId": "topic-prod", "ProjectId": project, "TopicName": "prod_后端服务日志"})
		case "/SearchLogs":
			searches.Add(1)
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ResultStatus": "complete", "HitCount": 25, "Count": 1, "ListOver": false, "Logs": []map[string]any{{"message": "处理完成 phone=13800138000 token=PRIVATE_TOKEN QA_AK_VALUE QA_SK_VALUE", "password": "PRIVATE_PASSWORD", "business_id": "business-123"}}})
		default:
			t.Error("调用了未预期或非只读的 TLS 接口", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	cfg := Config{Endpoint: server.URL, Region: "cn-guangzhou", AccessKey: "QA_AK_VALUE", SecretKey: "QA_SK_VALUE", Projects: map[string]string{"prod": "project-prod", "test": "project-test"}, Topics: map[string]string{"prod": "topic-prod", "test": "topic-test"}}
	client := newTLSClient(context.Background(), cfg)
	client.GetHttpClient().Transport = server.Client().Transport
	scope := QueryScope{Config: cfg, Environment: "prod", StartMS: 1700000000000, EndMS: 1700003600000}
	result, err := executeQuery(client, scope, QueryRequest{Action: "search", Query: "处理完成", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if request["StartTime"] != float64(1700000000000) || request["EndTime"] != float64(1700003600000) || request["TopicId"] != "topic-prod" || request["Limit"] != float64(20) || request["MustComplete"] != true {
		t.Fatalf("检索范围漂移：%v", request)
	}
	raw, _ := json.Marshal(result)
	for _, value := range []string{"13800138000", "PRIVATE_TOKEN", "PRIVATE_PASSWORD", "QA_AK_VALUE", "QA_SK_VALUE"} {
		if bytes.Contains(raw, []byte(value)) {
			t.Error("采样结果未完整脱敏")
		}
	}
	if !bytes.Contains(raw, []byte(`"hit_count":25`)) || !bytes.Contains(raw, []byte(`"sample_count":1`)) || !bytes.Contains(raw, []byte("business-123")) {
		t.Fatal("总量、采样量或非敏感业务证据丢失")
	}
	project = "another-project"
	if _, err = executeQuery(client, scope, QueryRequest{Action: "search", Query: "*"}); err == nil || searches.Load() != 1 {
		t.Fatal("项目不一致仍发起了日志检索")
	}
	if _, err = executeQuery(client, scope, QueryRequest{Action: "DeleteTopic", Query: "*"}); err == nil {
		t.Fatal("允许了查询以外的操作")
	}
}

// TestAccessScopeDoesNotExposeSecretsOrAllowWindowOverrides 验证短期入口加密凭据、固定北京时区，拒绝请求扩权及过期重用。
// 参数 t：临时工作区与加密文件，不运行模型或外部网络查询。
func TestAccessScopeDoesNotExposeSecretsOrAllowWindowOverrides(t *testing.T) {
	s, err := workspace.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{Store: s}
	cfg, err := DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.AccessKey, cfg.SecretKey = "QA_SCOPE_AK", "QA_SCOPE_SK"
	body, _ := json.Marshal(cfg)
	if _, err = service.Save(body); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	path, prompt, err := PrepareAccess(s.Dir, runDir, "2026-11-01T10:00", "2026-11-01T11:00", workspace.LogDomains(s.Config()))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || bytes.Contains(raw, []byte(cfg.AccessKey)) || bytes.Contains(raw, []byte(cfg.SecretKey)) || strings.Contains(prompt, cfg.AccessKey) || strings.Contains(prompt, cfg.SecretKey) {
		t.Fatal("访问入口或提示词泄露凭据")
	}
	scope, err := loadScope(path)
	if err != nil || scope.Environment != "prod" || scope.StartMS != time.Date(2026, 11, 1, 2, 0, 0, 0, time.UTC).UnixMilli() || scope.EndMS != time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC).UnixMilli() {
		t.Fatal("时间范围未按北京时间固定", err)
	}
	for _, request := range []string{`{"query":"*","start_ms":0}`, `{"query":"*","topic_id":"topic-test"}`, `{"query":"*"} {"query":"*"}`} {
		var output bytes.Buffer
		if err = RunCLI(path, strings.NewReader(request), &output); err == nil || output.Len() != 0 {
			t.Fatal("协议允许覆盖范围或消费多份请求")
		}
	}
	scope.ExpiresMS = time.Now().Add(-time.Minute).UnixMilli()
	plain, _ := json.Marshal(scope)
	ciphertext, err := secrets.Protect(string(plain), false)
	if err != nil {
		t.Fatal(err)
	}
	if err = fileio.WriteJSON(filepath.Join(runDir, "log-access.json"), map[string]string{"ciphertext": ciphertext}); err != nil {
		t.Fatal(err)
	}
	if _, err = loadScope(path); err == nil {
		t.Fatal("允许重用过期日志授权")
	}
}

// TestTLSClientDoesNotForwardCredentialsOnRedirect 验证官方 SDK 客户端仍遵守本软件的禁止重定向边界。
// 参数 t：两个本地 HTTPS 服务；计数确认第二个主机完全没有收到请求。
func TestTLSClientDoesNotForwardCredentialsOnRedirect(t *testing.T) {
	var forwarded atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Add(1) }))
	defer target.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer server.Close()
	client := newTLSClient(context.Background(), Config{Endpoint: server.URL, Region: "cn-guangzhou", AccessKey: "QA_AK", SecretKey: "QA_SK"})
	client.GetHttpClient().Transport = server.Client().Transport
	_, err := client.DescribeTopic(&tls.DescribeTopicRequest{TopicID: "topic"})
	if err == nil || forwarded.Load() != 0 {
		t.Fatal("TLS 请求跟随跳转或未报告拒绝结果")
	}
}
