package initialization

import (
	"context"
	"strings"
	"testing"

	"release-desk/internal/storage/workspace"
)

func TestMissingGatewayPathDoesNotFallBackToServicePathOrRoot(t *testing.T) {
	item := workspace.Item{Kind: "init_api", Data: map[string]string{"side": "租户端", "method": "POST", "service_path": "/init/records", "parameter_mode": "无参"}}
	req, _, err := buildRequest(context.Background(), item, Request{Environment: "test", Token: "fake-token"}, workspace.DefaultSettings())
	if req != nil || err == nil || !strings.Contains(err.Error(), "调用路径待确认") {
		t.Fatalf("缺失网关路径时形成了根地址或内部路径请求：%v %v", req, err)
	}
}

// TestRequiredTemplateCannotLoseParameter 验证必填参数不只是有任意输入，模板声明的字段必须完整提供。
// 参数 t：Go 测试上下文；仅构建内存请求，不联网。
func TestRequiredTemplateCannotLoseParameter(t *testing.T) {
	item := workspace.Item{Kind: "init_api", Data: map[string]string{"side": "服务商端", "method": "POST", "url": "/maintenance/rebuild", "parameter_mode": "必填参数", "params": `{"tenantId":""}`, "body": `{"scope":""}`}}
	config := workspace.DefaultSettings()
	for _, input := range []Request{
		{Environment: "test", Token: "temporary", Params: `{"other":"x"}`, Body: `{"scope":"demo"}`},
		{Environment: "test", Token: "temporary", Params: `{"tenantId":"1"}`, Body: `{"other":"x"}`},
		{Environment: "test", Token: "temporary", Params: `{"tenantId":"1"}`, Body: `{"scope":""}`},
	} {
		if _, _, err := buildRequest(context.Background(), item, input, config); err == nil {
			t.Fatal("缺失或未填写的必填字段仍可发送")
		}
	}
}

// TestInitializationRoutesAndParameterValues 验证环境及三端路由，服务商前缀不丢失，数字零和 false 不当作空。
// 参数 t：Go 测试上下文；只检查最终 HTTP 请求，不发送。
func TestInitializationRoutesAndParameterValues(t *testing.T) {
	for _, test := range []struct{ side, env, base string }{
		{"租户端", "test", "https://qyapitest.qiyucloud.com.cn"},
		{"服务商端", "test", "https://qyapitest.qiyucloud.com.cn/spapi"},
		{"平台端", "test", "https://qypfapitest.qiyucloud.com.cn"},
		{"租户端", "production", "https://qyapi.qiyucloud.com.cn"},
		{"服务商端", "production", "https://qyapi.qiyucloud.com.cn/spapi"},
		{"平台端", "production", "https://qypfapi.qiyucloud.com.cn"},
	} {
		side, env := test.side, test.env
		item := workspace.Item{Data: map[string]string{"side": side, "method": "POST", "url": "/maintenance/rebuild", "parameter_mode": "必填参数"}}
		r, _, err := buildRequest(context.Background(), item, Request{Environment: env, Token: "Bearer temporary", Params: `{"id":9007199254740993,"count":0,"enabled":false}`, Body: `{"count":0,"enabled":false}`}, workspace.DefaultSettings())
		if err != nil {
			t.Fatal(err)
		}
		want := test.base + "/maintenance/rebuild?"
		if !strings.HasPrefix(r.URL.String(), want) || r.URL.Query().Get("id") != "9007199254740993" || r.URL.Query().Get("enabled") != "false" || r.Header.Get("Authorization") != "Bearer temporary" {
			t.Fatalf("路由、鉴权或参数值错误：%s", r.URL.Redacted())
		}
	}
}
