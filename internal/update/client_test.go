package updateservice

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestCheckUsesPublicReleaseAndNumericVersions 核对实际 GitHub 请求及版本比较，不连接外网。
// 参数 t：测试上下文；响应只模拟 GitHub 的网络边界。
func TestCheckUsesPublicReleaseAndNumericVersions(t *testing.T) {
	s := New(context.Background())
	s.current = "1.9.0"
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.github.com/repos/niyingla/release-desk/releases/latest" || r.Header.Get("Authorization") != "" {
			t.Errorf("请求地址或匿名范围错误：%s", r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"tag_name":"v1.10.0","body":"新版说明","assets":[{"name":"ReleaseDesk-1.10.0.exe","state":"uploaded","size":100,"browser_download_url":"https://github.com/niyingla/release-desk/releases/download/v1.10.0/ReleaseDesk-1.10.0.exe"}]}`))}, nil
	})}
	s.Check(context.Background())
	got := s.Snapshot()
	if !got.Available || got.Latest != "1.10.0" || got.DownloadURL == "" || got.Note != "新版说明" || got.Checking {
		t.Fatalf("未正确识别新版：%+v", got)
	}
}

// TestCheckRejectsUntrustedAssetsAndNonStableVersions 防止从响应里的任意网址下载程序。
// 参数 t：测试上下文；覆盖旧版、预发布、无效标签、无 EXE 和跨站资源。
func TestCheckRejectsUntrustedAssetsAndNonStableVersions(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		available  bool
	}{
		{"older", `{"tag_name":"v1.8.0"}`, false},
		{"prerelease", `{"tag_name":"v2.0.0","prerelease":true}`, false},
		{"draft", `{"tag_name":"v2.0.0","draft":true}`, false},
		{"invalid-tag", `{"tag_name":"v1.9.1-rc.1"}`, false},
		{"cross-site", `{"tag_name":"v2.0.0","assets":[{"name":"ReleaseDesk-2.0.0.exe","state":"uploaded","size":10,"browser_download_url":"https://evil.example/ReleaseDesk-2.0.0.exe"}]}`, false},
		{"missing-asset", `{"tag_name":"v2.0.0","assets":[]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := New(context.Background())
			s.current = "1.9.0"
			s.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}
			s.Check(context.Background())
			got := s.Snapshot()
			if got.Available != tc.available || got.DownloadURL != "" {
				t.Fatalf("不可信或非稳定发布不能用于更新：%+v", got)
			}
		})
	}
}

// TestCheckDoesNotDuplicateConcurrentRequests 防止周期检查和连续点击产生重复网络请求。
// 参数 t：测试上下文；阻塞的请求可由测试释放，不访问真实 GitHub。
func TestCheckDoesNotDuplicateConcurrentRequests(t *testing.T) {
	s := New(context.Background())
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var called atomic.Int32
	s.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		if called.Add(1) == 1 {
			close(started)
		}
		<-release
		return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}
	go func() { defer close(done); s.Check(context.Background()) }()
	<-started
	s.Check(context.Background())
	close(release)
	<-done
	if called.Load() != 1 || s.Snapshot().Checking || s.Snapshot().Error != "" {
		t.Fatalf("重复请求或首次无发布被当成故障：%+v", s.Snapshot())
	}
}
