package gitrepo

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestVersionRevisionReadsLocalRefsOnly 验证远端有新提交时仍只读本地引用，不改变工作分支。
// 参数 t：Go 测试上下文；仓库与远端均使用临时目录，不访问外部服务器。
func TestVersionRevisionReadsLocalRefsOnly(t *testing.T) {
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("临时 Git 操作失败：%v: %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	remote, writer, reader := t.TempDir(), t.TempDir(), t.TempDir()
	git(remote, "init", "--bare")
	git(writer, "init")
	git(writer, "checkout", "-b", "release-5.7.3")
	git(writer, "config", "user.name", "qa")
	git(writer, "config", "user.email", "qa@example.invalid")
	git(writer, "commit", "--allow-empty", "-m", "initial")
	git(writer, "remote", "add", "origin", remote)
	git(writer, "push", "origin", "release-5.7.3")
	git(reader, "clone", "-b", "release-5.7.3", remote, ".")
	git(reader, "checkout", "-b", "unrelated")
	old := git(reader, "rev-parse", "HEAD")
	git(writer, "commit", "--allow-empty", "-m", "updated")
	git(writer, "push", "origin", "release-5.7.3")
	want := git(writer, "rev-parse", "HEAD")
	g := New(filepath.Clean(reader), "qa")
	local, err := g.VersionRevision(context.Background(), "5.7.3")
	if err != nil || local.SHA != old {
		t.Fatalf("读取了未拉取的提交：%+v %v", local, err)
	}
	if git(reader, "rev-parse", "origin/release-5.7.3") != old {
		t.Fatal("读取代码时擅自更新了本地远端跟踪引用")
	}
	// 用户在外部 Git 工具同步后，软件下次读取才会使用已更新的本地引用。
	git(reader, "fetch", "origin", "release-5.7.3")
	latest, err := g.VersionRevision(context.Background(), "5.7.3")
	if err != nil || latest.SHA != want {
		t.Fatalf("没有固定最新版本提交：%+v %v", latest, err)
	}
	if git(reader, "branch", "--show-current") != "unrelated" || git(reader, "rev-parse", "HEAD") != old {
		t.Fatal("更新版本代码切换或覆盖了当前工作区")
	}
	if _, err = g.VersionRevision(context.Background(), "missing"); err == nil {
		t.Fatal("不存在的版本回退到了当前分支")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = g.VersionRevision(ctx, "5.7.3"); err == nil {
		t.Fatal("已取消仍获取版本提交")
	}
}
