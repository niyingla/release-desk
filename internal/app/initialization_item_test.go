package app

import (
	"encoding/json"
	"strings"
	"testing"

	aiservice "release-desk/internal/ai"
	fileservice "release-desk/internal/files"
	releaseservice "release-desk/internal/release"
	"release-desk/internal/storage/workspace"
)

// TestInitializationItemScanSaveMergeAndExport 验证初始化调用事项可从扫描进入清单，人工完成状态持久化并参与导出。
// 参数 t：Go 测试上下文；使用临时数据目录，仅操作记录，不请求接口或数据库。
func TestInitializationItemScanSaveMergeAndExport(t *testing.T) {
	s, err := workspace.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	result := aiservice.ScanResult{Summary: "上线后手动初始化", Items: []aiservice.ScanItem{{
		ID: "init-cache", Kind: "init_api", Action: "新增", Service: "example-service",
		Fields:   []aiservice.ScanField{{Name: "name", Value: "初始化演示缓存"}, {Name: "side", Value: "租户端"}, {Name: "method", Value: "POST"}, {Name: "url", Value: "/maintenance/rebuild"}, {Name: "body", Value: "{\"scope\":\"demo\"}"}, {Name: "prerequisites", Value: "先完成服务发布"}, {Name: "expected", Value: "核对初始化结果"}},
		Evidence: []string{"qa-sha MaintenanceController#rebuild"},
	}}}
	v, err := aiservice.PrepareScanVersion(workspace.Version{Name: "qa-init"}, result)
	if err != nil {
		t.Fatalf("扫描未支持初始化接口：%v", err)
	}
	if v.Items[0].Status != "待处理" {
		t.Fatal("扫描默认标记成已完成")
	}
	if err = s.Save(v); err != nil {
		t.Fatal(err)
	}
	item := workspace.Clone(v.Items[0])
	item.Status = "已完成"
	request, _ := json.Marshal(workspace.ItemRequest{Version: v.Version.Name, Item: item})
	if _, err = (&releaseservice.Service{Store: s}).SaveItem(request); err != nil {
		t.Fatal(err)
	}
	// 再次扫描更新参数时仍保留人工维护的完成状态。
	candidate := workspace.Clone(v)
	candidate.Items[0].Data["body"] = "{\"scope\":\"updated\"}"
	merged, err := workspace.MergeExternalVersion(s.Versions[v.Version.Name], candidate)
	if err != nil || merged.Items[0].Status != "已完成" || merged.Items[0].Data["body"] != "{\"scope\":\"updated\"}" {
		t.Fatal("扫描覆盖了完成状态或丢失调用参数")
	}
	if err = s.Save(merged); err != nil {
		t.Fatal(err)
	}
	reopened, err := workspace.OpenStore(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	got := reopened.Versions[v.Version.Name]
	if got.Items[0].Status != "已完成" {
		t.Fatal("完成状态未持久化")
	}
	report, err := fileservice.RenderReport(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"接口初始化", "初始化演示缓存", "POST", "/maintenance/rebuild", "先完成服务发布", "核对初始化结果", "已完成"} {
		if !strings.Contains(string(report), text) {
			t.Fatalf("导出丢失调用信息：%s", text)
		}
	}
}

func TestInitializationScanKeepsInternalPathWithoutInventingGatewayPath(t *testing.T) {
	result := aiservice.ScanResult{Summary: "维护接口路径待确认", Items: []aiservice.ScanItem{{
		ID: "init-pending-route", Kind: "init_api", Action: "新增", Service: "example-maintenance",
		Fields:   []aiservice.ScanField{{Name: "name", Value: "初始化历史记录"}, {Name: "side", Value: "租户端"}, {Name: "method", Value: "POST"}, {Name: "service_path", Value: "/init/records"}, {Name: "parameter_mode", Value: "无参"}},
		Evidence: []string{"qa-sha MaintenanceController#initialize；网关前缀待确认"},
	}}}
	v, err := aiservice.PrepareScanVersion(workspace.Version{Name: "qa-init-route"}, result)
	if err != nil || len(v.Items) != 1 || v.Items[0].Data["service_path"] != "/init/records" || v.Items[0].Data["url"] != "" {
		t.Fatalf("内部路径丢失或被误当作网关路径：%+v %v", v, err)
	}
}
