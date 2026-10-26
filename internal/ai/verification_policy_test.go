package aiservice

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"release-desk/internal/storage/workspace"
)

// TestNeutralObservationRequiresActualLogQuery 防止无查询或网络失败伪装成中立观察。
// 参数 t：测试上下文；只校验临时结果，不查询真实环境。
func TestNeutralObservationRequiresActualLogQuery(t *testing.T) {
	for _, tc := range []struct {
		queried       bool
		blocker, want string
	}{
		{true, "无适用样本", "未发现异常"}, {false, "无适用样本", "证据不足"}, {true, "取证受阻", "证据不足"},
	} {
		// 相同结论仅改变成功查询标记和阻断原因，区分中立观察与工具故障。
		result := workspace.VerifyResult{Status: "未发现异常", Confirmation: "未确认", Environment: "生产", LogQuerySucceeded: tc.queried, Blocker: tc.blocker, Summary: "未观察到适用异常，未确认关键结果", Evidence: []string{"QA 查询范围与观察结果"}}
		body, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(t.TempDir(), "neutral.json")
		if err = os.WriteFile(file, body, 0600); err != nil {
			t.Fatal(err)
		}
		got, err := ReadVerificationResult(file)
		if err != nil || got.Status != tc.want {
			t.Fatalf("中立边界错误：%s %v", got.Status, err)
		}
	}
}

// TestReadVerificationConfidence 验证务实结论与明显缺口的边界，兼容旧结果。
// 参数 t：测试上下文；只读取临时结构化结果，不作生产结论。
func TestReadVerificationConfidence(t *testing.T) {
	for _, tc := range []struct {
		confirmation, blocker, want string
		invalid                     bool
	}{
		{"高概率确认", "", "通过", false}, {"直接确认", "", "通过", false}, {"", "", "通过", false},
		{"未确认", "关键证据缺失", "证据不足", false}, {"高概率确认", "取证受阻", "证据不足", false}, {"猜测", "", "", true},
	} {
		// 仅改变确认字段，其余可读证据固定，避免靠关键词决定结论。
		result := workspace.VerifyResult{Status: "通过", Environment: "生产", Summary: "本轮核验", Evidence: []string{"QA证据"}, Confirmation: tc.confirmation, Blocker: tc.blocker}
		body, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(t.TempDir(), "result.json")
		if err = os.WriteFile(file, body, 0600); err != nil {
			t.Fatal(err)
		}
		got, err := ReadVerificationResult(file)
		if (err != nil) != tc.invalid || !tc.invalid && got.Status != tc.want {
			t.Fatalf("确认边界错误：%+v %v", got, err)
		}
	}
}

// TestVerificationPermissionsRejectInjection 验证域名可配且只有日志主机获得网络权限。
// 参数 t：测试上下文；只构造参数，不修改全局 Codex 配置或运行命令。
func TestVerificationPermissionsRejectInjection(t *testing.T) {
	config := workspace.DefaultSettings()
	args, err := VerificationPermissionArguments(config)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, "\n")
	// 只读基线和代理强制生效，不允许危险完全访问或旧 sandbox 开关覆盖配置。
	if !strings.Contains(joined, `extends=":read-only"`) || !strings.Contains(joined, "network_proxy=true") || strings.Contains(joined, "danger-full-access") || strings.Contains(joined, "--sandbox") {
		t.Fatal("验证权限失去只读边界")
	}
	for _, domain := range []string{"https://host.com", "*.volces.com", "host.com:443", "host.com\"=true", "host..com"} {
		config.VerificationLogDomains = []string{domain}
		if _, err = VerificationPermissionArguments(config); err == nil {
			t.Fatalf("未拒绝非法主机 %q", domain)
		}
	}
}
