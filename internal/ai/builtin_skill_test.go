package aiservice

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"release-desk/internal/identity"
	"release-desk/internal/storage/workspace"
	"release-desk/internal/versioninfo"
)

// TestScanPromptDoesNotRequirePersonalSkill 验证新电脑没有安装个人技能时，提示词仍带有完整内置规则。
// 参数 t：测试上下文；用户目录与数据目录均隔离，不运行真实 Codex。
func TestScanPromptDoesNotRequirePersonalSkill(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	prompt, err := VersionScanPrompt(workspace.VersionFile{Version: workspace.Version{Name: "qa-builtin"}}, workspace.DefaultSettings())
	if err != nil || strings.Contains(prompt, filepath.Join(home, ".codex", "skills", "release-desk-version-scan")) || !strings.Contains(prompt, "## 合并和状态") {
		t.Fatalf("仍然依赖用户单独安装技能，或缺少增量规则：%v", err)
	}
	if _, err = os.Stat(filepath.Join(home, ".codex")); !os.IsNotExist(err) {
		t.Fatal("扫描提示词修改了用户的个人技能目录")
	}
}

// TestBuiltinSkillsRefreshSoftwareCopiesOnly 验证更新会替换软件管理副本，并保留个人技能和版本数据。
// 参数 t：使用独立用户和数据目录模拟旧副本，不访问当前用户配置或凭据。
func TestBuiltinSkillsRefreshSoftwareCopiesOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	personal := filepath.Join(home, ".codex", "skills", "release-desk-version-scan", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(personal), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(personal, []byte("PERSONAL_RULES_DO_NOT_OVERWRITE"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := workspace.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{Store: store}
	paths, err := s.SyncBuiltinSkills()
	if err != nil || len(paths) != 3 {
		t.Fatalf("未导出三份内置规则：%v", err)
	}
	for _, path := range paths {
		if err = os.WriteFile(path, []byte("STALE_SOFTWARE_COPY"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	paths, err = s.SyncBuiltinSkills()
	if err != nil {
		t.Fatal(err)
	}
	info, err := versioninfo.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		content, readErr := os.ReadFile(path)
		if readErr != nil || strings.Contains(string(content), "STALE_SOFTWARE_COPY") || len(content) == 0 {
			t.Fatalf("旧规则未随程序资源刷新：%v", readErr)
		}
		raw, readErr := os.ReadFile(filepath.Join(filepath.Dir(path), "version.json"))
		var metadata map[string]string
		if readErr != nil || json.Unmarshal(raw, &metadata) != nil || metadata["software_version"] != info.Current || metadata["sha256"] != identity.Digest(string(content)) {
			t.Fatal("技能版本或摘要与实际资源不一致")
		}
	}
	content, err := os.ReadFile(personal)
	if err != nil || string(content) != "PERSONAL_RULES_DO_NOT_OVERWRITE" || len(store.Versions) != 0 {
		t.Fatal("技能同步覆盖了个人规则或导入了示例版本")
	}
	rules, _, err := verificationSkillRules(filepath.Join(home, "missing-personal-skill.md"))
	if err != nil || !strings.Contains(rules, "内置环境路由规则") || !strings.Contains(rules, "内置日志查询与代码复核") {
		t.Fatalf("没有个人技能时不能使用内置日志规则：%v", err)
	}
}

// TestTemplateExportsBuiltinSkill 验证生成的学习材料自带技能，且不会把示例导入真实版本目录。
// 参数 t：隔离存储与文件断言；不读取用户已有凭据或个人技能。
func TestTemplateExportsBuiltinSkill(t *testing.T) {
	s, err := workspace.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	folder, err := (&Service{Store: s}).Templates()
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(folder, "skills", "release-desk-version-scan", "SKILL.md"))
	if err != nil || !strings.Contains(string(content), "PostReleaseManualRun") {
		t.Fatalf("模板未附带完整内置技能：%v", err)
	}
	if len(s.Versions) != 0 {
		t.Fatal("生成接入材料时导入了示例版本")
	}
}
