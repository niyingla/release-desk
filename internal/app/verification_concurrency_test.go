package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	aiservice "release-desk/internal/ai"
	"release-desk/internal/storage/workspace"
)

// concurrentVerifyFixture 创建超过并发上限的隔离批次，以便核对补位与等待状态。
// 参数 t：测试上下文；所有记录只写入临时目录。
// 返回：独立工作区及含十二项的批量请求。
func concurrentVerifyFixture(t *testing.T) (*workspace.Store, aiservice.VerifyQueueRequest) {
	t.Helper()
	s, req := queueVerifyFixture(t)
	v := s.Versions[req.Version]
	v.Items = nil
	req.IDs = nil
	for i := 1; i <= 12; i++ {
		id := fmt.Sprintf("v%d", i)
		req.IDs = append(req.IDs, id)
		v.Items = append(v.Items, workspace.Item{ID: id, Kind: "verify", Data: map[string]string{"title": id, "change": "独立修复目标"}})
	}
	if err := s.Save(v); err != nil {
		t.Fatal(err)
	}
	return s, req
}

// nextVerificationStart 有界等待一项真实进入验收器，串行实现会超时而不是挂住测试。
// 参数 t：测试上下文；参数 started：验收器写入的稳定事项 ID 通道。
// 返回：已开始的事项 ID；超时直接结束当前测试。
func nextVerificationStart(t *testing.T, started <-chan string) string {
	t.Helper()
	select {
	case id := <-started:
		return id
	case <-time.After(3 * time.Second):
		t.Fatal("未达到六项同时验证，队列可能仍在串行执行")
		return ""
	}
}

// stopVerificationFixture 等待取消真正收尾，防止测试临时目录清理与结果保存竞争。
// 参数 t：测试上下文；参数 s：当前测试独占的临时工作区。
func stopVerificationFixture(t *testing.T, s *workspace.Store) {
	t.Helper()
	s.Mu.Lock()
	if s.VerifyQueue == nil {
		s.Mu.Unlock()
		return
	}
	if s.VerifyQueueCancel != nil {
		s.VerifyQueueCancel()
	}
	s.Mu.Unlock()
	waitVerifyQueue(t, s)
}

// TestVerifyQueueHonorsRequestedConcurrency 验证页面选择真正限制工作者数量且重启后保留。
// 参数 t：测试上下文；独立临时设置文件，不修改真实用户偏好。
func TestVerifyQueueHonorsRequestedConcurrency(t *testing.T) {
	s, req := concurrentVerifyFixture(t)
	// 使用真实 JSON 请求边界；后端忽略该字段时会多启动工作者，测试应失败。
	if err := json.Unmarshal([]byte(`{"concurrency":3}`), &req); err != nil {
		t.Fatal(err)
	}
	started := make(chan string, 12)
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	if err := (&aiservice.Service{Store: s}).StartVerifyQueue(req, func(ctx context.Context, r aiservice.VerifyRequest, _ workspace.Item, _ workspace.Settings) (workspace.VerifyResult, error) {
		started <- r.ID
		select {
		case <-release:
		case <-ctx.Done():
			return workspace.VerifyResult{}, ctx.Err()
		}
		return workspace.VerifyResult{Status: "通过", Summary: r.ID, Environment: "生产", Evidence: []string{"模拟证据"}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopVerificationFixture(t, s) })
	for range 3 {
		nextVerificationStart(t, started)
	}
	select {
	case id := <-started:
		t.Fatalf("选择三并发却启动了额外事项：%s", id)
	case <-time.After(80 * time.Millisecond):
	}
	once.Do(func() { close(release) })
	q := waitVerifyQueue(t, s)
	if q.Concurrency != 3 {
		t.Fatalf("实际并发数错误：%d", q.Concurrency)
	}
	content, err := os.ReadFile(filepath.Join(s.Dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err = json.Unmarshal(content, &config); err != nil || config["verification_concurrency"] != float64(3) {
		t.Fatalf("并发偏好未可靠保存：%v %v", config["verification_concurrency"], err)
	}
	// 从全新工作区实例恢复，确认默认控件值不依赖旧进程内存或浏览器端口。
	restarted, err := workspace.OpenStore(s.Dir)
	if err != nil || restarted.Config().VerificationConcurrency != 3 {
		t.Fatalf("重启后并发偏好丢失：%v", err)
	}
}

// TestVerifyQueueRejectsInvalidConcurrency 防止直接 API 请求绕过页面的并发范围。
// 参数 t：测试上下文；回调只计数，不访问模型或日志。
func TestVerifyQueueRejectsInvalidConcurrency(t *testing.T) {
	for _, count := range []int{-1, 7} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s, req := concurrentVerifyFixture(t)
			t.Cleanup(func() { stopVerificationFixture(t, s) })
			if err := json.Unmarshal([]byte(fmt.Sprintf(`{"concurrency":%d}`, count)), &req); err != nil {
				t.Fatal(err)
			}
			var called atomic.Int32
			err := (&aiservice.Service{Store: s}).StartVerifyQueue(req, func(context.Context, aiservice.VerifyRequest, workspace.Item, workspace.Settings) (workspace.VerifyResult, error) {
				called.Add(1)
				return workspace.VerifyResult{}, nil
			})
			if err == nil || called.Load() != 0 {
				t.Fatalf("非法并发数仍启动取证：%d %v", count, err)
			}
		})
	}
}

// TestVerifyQueueRunsSixAndRefillsSlots 证明最多六项并发、空位即时补齐且不会丢失逐项历史。
// 参数 t：测试上下文；验收器仅模拟耗时外部边界，不启动 Codex。
func TestVerifyQueueRunsSixAndRefillsSlots(t *testing.T) {
	s, req := concurrentVerifyFixture(t)
	started := make(chan string, 12)
	release := make(chan struct{})
	refill := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var active, peak atomic.Int32
	ai := &aiservice.Service{Store: s}
	err := ai.StartVerifyQueue(req, func(ctx context.Context, r aiservice.VerifyRequest, _ workspace.Item, _ workspace.Settings) (workspace.VerifyResult, error) {
		count := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); count > old && !peak.CompareAndSwap(old, count); old = peak.Load() {
		}
		started <- r.ID
		gate := release
		if r.ID == "v6" {
			gate = refill
		}
		select {
		case <-gate:
		case <-ctx.Done():
			return workspace.VerifyResult{}, ctx.Err()
		}
		if r.ID == "v2" {
			return workspace.VerifyResult{}, errors.New("模拟单项 CLI 失败")
		}
		return workspace.VerifyResult{Status: "通过", Summary: r.ID, Environment: "生产", Evidence: []string{"模拟运行证据 " + r.ID}, Analysis: &workspace.VerificationAnalysis{Goal: r.ID, Code: "代码依据 " + r.ID, Logs: "运行日志 " + r.ID, Reasoning: "综合判断 " + r.ID}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopVerificationFixture(t, s) })
	seen := map[string]bool{}
	for range 6 {
		seen[nextVerificationStart(t, started)] = true
	}
	// 批次仍持有工作区占用，不能重复提交另一组六并发任务。
	if err := ai.StartVerifyQueue(req, nil); err == nil {
		t.Fatal("运行中允许重复启动整个验证批次")
	}
	select {
	case id := <-started:
		t.Fatalf("超过六项并发：%s", id)
	case <-time.After(80 * time.Millisecond):
	}
	// 只释放一个槽位，仍有五项未完成时第七项就应该开始。
	close(refill)
	id := nextVerificationStart(t, started)
	if id != "v7" || seen[id] {
		t.Fatalf("未按等待顺序补位：%s", id)
	}
	seen[id] = true
	once.Do(func() { close(release) })
	q := waitVerifyQueue(t, s)
	if peak.Load() != 6 || q.Rows[1].State != "证据不足" {
		t.Fatalf("并发上限或失败继续错误：peak=%d queue=%+v", peak.Load(), q)
	}
	restarted, err := workspace.OpenStore(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for i, item := range restarted.Versions[req.Version].Items {
		if len(item.VerifyRuns) != 1 || q.Rows[i].Result == nil || (i != 1 && q.Rows[i].Result.Summary != item.ID) {
			t.Fatalf("结果串项或历史丢失：%s %+v", item.ID, q.Rows[i])
		}
		if i != 1 && (item.VerifyRuns[0].Analysis == nil || item.VerifyRuns[0].Analysis.Code != "代码依据 "+item.ID) {
			t.Fatalf("重启后分项分析丢失：%s", item.ID)
		}
	}
	record, ok := restarted.TaskHistory.Get(q.ID)
	if !ok || !strings.Contains(record.Rows[0].Detail, "综合判断 v1") {
		t.Fatal("任务中心未保留代码与日志的综合判断")
	}
	if s.AIBusy {
		t.Fatal("所有工作者收尾后未释放占用")
	}
}

// TestVerifyQueueSaveFailureCancelsOtherWorkers 证明无法保存结论时整批停止，等待项不再收费取证。
// 参数 t：测试上下文；只破坏临时工作区路径，不操作真实用户记录。
func TestVerifyQueueSaveFailureCancelsOtherWorkers(t *testing.T) {
	s, req := concurrentVerifyFixture(t)
	started := make(chan string, 12)
	releaseFailure := make(chan struct{})
	if err := (&aiservice.Service{Store: s}).StartVerifyQueue(req, func(ctx context.Context, r aiservice.VerifyRequest, _ workspace.Item, _ workspace.Settings) (workspace.VerifyResult, error) {
		started <- r.ID
		if r.ID == "v1" {
			select {
			case <-releaseFailure:
			case <-ctx.Done():
			}
		} else {
			<-ctx.Done()
		}
		return workspace.VerifyResult{Status: "通过", Summary: r.ID, Environment: "生产", Evidence: []string{"隔离模拟证据"}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopVerificationFixture(t, s) })
	for range 6 {
		nextVerificationStart(t, started)
	}
	// 先确定所有槽位已占用，再模拟磁盘路径不可写，唯一释放项会触发保存失败。
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("QA"), 0600); err != nil {
		t.Fatal(err)
	}
	s.Mu.Lock()
	s.Dir = blocked
	s.Mu.Unlock()
	close(releaseFailure)
	q := waitVerifyQueue(t, s)
	for i, row := range q.Rows {
		want := "未验证"
		if i == 0 {
			want = "保存失败"
		} else if i < 6 {
			want = "已停止"
		}
		if row.State != want || len(s.Versions[req.Version].Items[i].VerifyRuns) != 0 {
			t.Fatalf("保存失败后仍继续取证或错误写回：%+v", row)
		}
	}
	select {
	case id := <-started:
		t.Fatalf("保存失败后还启动了 %s", id)
	default:
	}
}

// TestVerifyQueueStopsAllWorkersAndRejectsLateResults 证明停止覆盖所有并发项且迟到通过不能写回。
// 参数 t：测试上下文；验收器故意在取消后返回通过，模拟无法及时响应的工具。
func TestVerifyQueueStopsAllWorkersAndRejectsLateResults(t *testing.T) {
	s, req := concurrentVerifyFixture(t)
	started := make(chan string, 12)
	releaseFirst := make(chan struct{})
	ai := &aiservice.Service{Store: s}
	err := ai.StartVerifyQueue(req, func(ctx context.Context, r aiservice.VerifyRequest, _ workspace.Item, _ workspace.Settings) (workspace.VerifyResult, error) {
		started <- r.ID
		if r.ID == "v1" {
			select {
			case <-releaseFirst:
			case <-ctx.Done():
			}
		} else {
			<-ctx.Done()
		}
		return workspace.VerifyResult{Status: "通过", Summary: r.ID, Environment: "生产", Evidence: []string{"模拟证据"}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopVerificationFixture(t, s) })
	for range 6 {
		nextVerificationStart(t, started)
	}
	close(releaseFirst)
	if id := nextVerificationStart(t, started); id != "v7" {
		t.Fatalf("首项完成未补位：%s", id)
	}
	s.Mu.Lock()
	id := s.VerifyQueue.ID
	s.Mu.Unlock()
	if err = s.StopTask(id); err != nil {
		t.Fatal(err)
	}
	q := waitVerifyQueue(t, s)
	for i, row := range q.Rows {
		want := "未验证"
		if i == 0 {
			want = "通过"
		} else if i < 7 {
			want = "已停止"
		}
		if row.State != want || (i != 0 && len(s.Versions[req.Version].Items[i].VerifyRuns) != 0) {
			t.Fatalf("停止后结果错误：%s %+v", row.ItemID, row)
		}
	}
	select {
	case extra := <-started:
		t.Fatalf("停止后仍启动等待项：%s", extra)
	default:
	}
}
