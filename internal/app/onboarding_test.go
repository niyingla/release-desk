package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"release-desk/internal/storage/workspace"
)

// TestAIOnboardingPreparesPortableGuidesWithoutSecrets 验证接入操作只生成本机学习材料，不填写配置或泄露独立凭据。
// 参数 t：隔离目录中的文件与端点断言，不运行模型、Git 网络或数据库请求。
func TestAIOnboardingPreparesPortableGuidesWithoutSecrets(t *testing.T) {
	s, err := workspace.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(s.Dir, "settings.json")
	before, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(s.Dir, "connection.json"), []byte(`{"password":"NEVER_COPY_CONNECTION_PASSWORD"}`), 0600); err != nil {
		t.Fatal(err)
	}
	handler := (&App{Store: s}).handlers()["ai/onboarding"]
	if handler == nil {
		t.Fatal("未提供 AI 接入指引端点")
	}
	result, err := handler([]byte(`{"name":"qa-portable"}`))
	if err != nil {
		t.Fatal(err)
	}
	data := result.(map[string]string)
	if !strings.Contains(data["prompt"], s.Dir) || data["skill"] == "" || data["guide"] == "" {
		t.Fatalf("缺少实际目录与接入入口：%v", data)
	}
	guide, err := os.ReadFile(data["guide"])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(guide)+data["prompt"], "NEVER_COPY_CONNECTION_PASSWORD") {
		t.Fatal("接入指引读取或复制了连接凭据")
	}
	after, err := os.ReadFile(settingsPath)
	if err != nil || !bytes.Equal(before, after) || len(s.Versions) != 0 {
		t.Fatal("生成指引修改了软件设置或发布记录")
	}
	for _, field := range []string{"code_repository", "sql_repository", "git_author", "init_api", "service_path"} {
		if !strings.Contains(string(guide), field) {
			t.Errorf("指引缺少设置或发布字段说明：%s", field)
		}
	}
}
