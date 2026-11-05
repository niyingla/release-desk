package aiservice

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"release-desk/internal/storage/workspace"
)

// TestFullScanPinsSelectedVersion 验证工作区处于较新版本时，整版扫描仍发送用户选择的旧版提交。
// 参数 t：使用临时仓库和本地 CLI 替身，不调用模型、不访问远端或用户数据。
func TestFullScanPinsSelectedVersion(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("测试仓库操作失败：%v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init")
	git("config", "user.name", "qa")
	git("config", "user.email", "qa@example.invalid")
	git("checkout", "-b", "release-5.8.0")
	git("commit", "--allow-empty", "-m", "older target")
	want := git("rev-parse", "HEAD")
	git("checkout", "-b", "release-5.8.1")
	git("commit", "--allow-empty", "-m", "newer working branch")
	head := git("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(dir, "uncommitted.txt"), []byte("preserve workspace"), 0600); err != nil {
		t.Fatal(err)
	}
	status := git("status", "--porcelain")
	store, err := workspace.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	config := workspace.DefaultSettings()
	config.CodeRepository = dir
	capture := filepath.Join(t.TempDir(), "prompt.txt")
	t.Setenv("RELEASE_DESK_TEST_SCAN_PROMPT", capture)
	cli := buildScanCaptureCLI(t)
	s := &Service{Store: store}
	_, err = s.InvokeVersionScan(context.Background(), cli, workspace.VersionFile{Version: workspace.Version{Name: "5.8.0"}}, config)
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := os.ReadFile(capture)
	if err != nil || !strings.Contains(string(prompt), want) || !strings.Contains(string(prompt), "refs/heads/release-5.8.0") || strings.Contains(string(prompt), head) {
		t.Fatalf("整版扫描未固定用户选择的旧版引用与 SHA：%v", err)
	}
	if git("branch", "--show-current") != "release-5.8.1" || git("rev-parse", "HEAD") != head || git("status", "--porcelain") != status {
		t.Fatal("扫描切换分支或修改了工作区")
	}
	if err = os.Remove(capture); err != nil {
		t.Fatal(err)
	}
	_, err = s.InvokeVersionScan(context.Background(), cli, workspace.VersionFile{Version: workspace.Version{Name: "missing"}}, config)
	if err == nil {
		t.Fatal("缺少发布分支时仍启动了模型")
	}
	if _, err = os.Stat(capture); !os.IsNotExist(err) {
		t.Fatal("无效版本仍被交给 CLI，可能回退当前 HEAD")
	}
}

// buildScanCaptureCLI 构建仅用于测试的本地协议替身，捕获实际传给 CLI 的提示词。
// 参数 t：隔离构建目录及错误报告；不复用真实 Codex 登录、模型或网络。
// 返回：临时可执行文件；仅输出空候选，验证提示词和启动边界。
func buildScanCaptureCLI(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	source := `package main
import("io";"os")
func main(){
 p,e:=io.ReadAll(os.Stdin);if e!=nil{panic(e)}
 if e=os.WriteFile(os.Getenv("RELEASE_DESK_TEST_SCAN_PROMPT"),p,0600);e!=nil{panic(e)}
 for i,a:=range os.Args {if a=="-o" && i+1<len(os.Args){if e=os.WriteFile(os.Args[i+1],[]byte("{\"summary\":\"qa\",\"warnings\":[],\"items\":[]}"),0600);e!=nil{panic(e)};return}}
 os.Exit(2)
}`
	path := filepath.Join(dir, "main.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(dir, "capture-cli.exe")
	goExe := filepath.Join(runtime.GOROOT(), "bin", "go")
	if runtime.GOOS == "windows" {
		goExe += ".exe"
	}
	if out, err := exec.Command(goExe, "build", "-o", cli, path).CombinedOutput(); err != nil {
		t.Fatalf("构建协议替身失败：%v %s", err, out)
	}
	return cli
}
