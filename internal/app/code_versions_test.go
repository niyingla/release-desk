package app

import (
	"os/exec"
	"reflect"
	"strings"
	"testing"

	gitrepo "release-desk/internal/git/repository"
	"release-desk/internal/storage/workspace"
)

// TestNewVersionCandidatesUseCodeReleaseBranches 验证新建版本只读取代码库发布分支，不混入 SQL 目录或其他分支。
// 参数 t：两个隔离仓库及本机接口断言，不访问远端，不修改真实设置。
func TestNewVersionCandidatesUseCodeReleaseBranches(t *testing.T) {
	code, sql := t.TempDir(), t.TempDir()
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("测试仓库操作失败：%v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	for _, dir := range []string{code, sql} {
		git(dir, "init")
		git(dir, "config", "user.name", "qa")
		git(dir, "config", "user.email", "qa@example.invalid")
		git(dir, "commit", "--allow-empty", "-m", "fixture")
	}
	for _, branch := range []string{"release-5.8.0", "release-5.8.1", "release-5.10.0", "feature/5.9.0", "hotfix-5.9.0", "test-5.9.0"} {
		git(code, "branch", branch)
	}
	git(code, "update-ref", "refs/remotes/origin/release-5.8.0", "HEAD")
	git(code, "update-ref", "refs/remotes/origin/release-5.9.0", "HEAD")
	git(code, "update-ref", "refs/remotes/origin/feature/5.11.0", "HEAD")
	git(sql, "branch", "release-99.0.0")
	before := git(code, "symbolic-ref", "HEAD")
	s, err := workspace.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.Settings.CodeRepository, s.Settings.SQLRepository = code, sql
	app := &App{Store: s, Git: gitrepo.New(sql, "qa")}
	handler := app.handlers()["git/code-versions"]
	if handler == nil {
		t.Fatal("新增版本尚未提供独立的代码库发布分支来源")
	}
	result, err := handler(nil)
	if err != nil {
		t.Fatal(err)
	}
	data := result.(map[string]any)
	list := data["versions"].([]gitrepo.GitVersion)
	var names []string
	for _, item := range list {
		names = append(names, item.Name)
	}
	if !reflect.DeepEqual(names, []string{"5.10.0", "5.9.0", "5.8.1", "5.8.0"}) || data["path"] != code {
		t.Fatalf("版本来源、前缀过滤、去重或倒序错误：%v", data)
	}
	if git(code, "symbolic-ref", "HEAD") != before {
		t.Fatal("列举候选切换了工作分支")
	}
}
