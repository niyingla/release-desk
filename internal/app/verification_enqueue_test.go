package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	aiservice "release-desk/internal/ai"
	"release-desk/internal/storage/workspace"
)

// TestVerificationEnqueueRunsIndependentItems 验证连续入队并行、等待、去重及逐项时间窗。
// 参数 t：测试上下文；取证器全部模拟，只写入临时目录。
func TestVerificationEnqueueRunsIndependentItems(t *testing.T) {
	s, batch := concurrentVerifyFixture(t)
	// 固定为两并发，才能明确观察第一个任务运行时第二个可开始、后续仍等待。
	s.Mu.Lock()
	err := s.RememberVerificationConcurrency(2)
	s.Mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	ai := &aiservice.Service{Store: s}
	started := make(chan aiservice.VerifyRequest, 12)
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	runner := func(ctx context.Context, req aiservice.VerifyRequest, _ workspace.Item, _ workspace.Settings) (workspace.VerifyResult, error) {
		started <- req
		select {
		case <-ctx.Done():
			return workspace.VerifyResult{}, ctx.Err()
		case <-release:
		}
		return workspace.VerifyResult{Status: "通过", Summary: req.ID, Environment: "生产", Evidence: []string{"模拟独立证据"}}, nil
	}
	var queueID string
	for i := 0; i < 5; i++ {
		// 每条请求保留自己的时间，不复用最早入队项的窗口。
		req := aiservice.VerifyRequest{Version: batch.Version, ID: batch.IDs[i], Start: batch.Start, End: batch.End}
		if i == 1 {
			req.Start = time.Now().In(time.FixedZone("Asia/Shanghai", 8*60*60)).Add(-2 * time.Hour).Format("2006-01-02T15:04")
		}
		receipt, err := ai.EnqueueVerification(req, runner)
		if err != nil {
			t.Fatalf("第%d项无法在运行中入队：%v", i+1, err)
		}
		if queueID == "" {
			queueID = receipt.QueueID
			t.Cleanup(func() { stopVerificationFixture(t, s) })
		}
		if !receipt.Accepted || receipt.QueueID != queueID {
			t.Fatalf("未追加到同一队列：%+v", receipt)
		}
		if i < 2 {
			// 两项都应在放行前进入取证器，不能退化成串行。
			select {
			case got := <-started:
				if got.ID != req.ID || got.Start != req.Start {
					t.Fatalf("条件串项：%+v", got)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("独立验证未并行开始")
			}
		}
	}
	// 同一事项运行中再点一次，只返回原队列身份，不添加收费任务。
	receipt, err := ai.EnqueueVerification(aiservice.VerifyRequest{Version: batch.Version, ID: batch.IDs[0], Start: batch.Start, End: batch.End}, runner)
	if err != nil || receipt.Accepted {
		t.Fatalf("重复入队未被合并：%+v %v", receipt, err)
	}
	s.Mu.Lock()
	rows := workspace.Clone(s.VerifyQueue.Rows)
	s.Mu.Unlock()
	if len(rows) != 5 {
		t.Fatalf("重复或丢失事项：%d", len(rows))
	}
	for _, row := range rows[2:] {
		if row.State != "等待中" {
			t.Fatalf("超过并发上限：%+v", rows)
		}
	}
	select {
	case req := <-started:
		t.Fatalf("多启动了任务：%+v", req)
	case <-time.After(80 * time.Millisecond):
	}
	once.Do(func() { close(release) })
	q := waitVerifyQueue(t, s)
	// 重新读取正式记录，确认动态追加项与首项同样可靠保存。
	reopened, err := workspace.OpenStore(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for i, row := range q.Rows {
		_, item, _, findErr := reopened.Find(batch.Version, row.ItemID)
		if findErr != nil || row.State != "通过" || len(item.VerifyRuns) != 1 {
			t.Fatal(fmt.Sprintf("第%d项历史异常：%+v %v", i+1, row, findErr))
		}
	}
}

// TestCancelWaitingVerificationKeepsOthers 验证精确取消等待项且旧按钮不取消后来重新排队的事项。
// 参数 t：隔离测试上下文，全部取证由通道模拟。
func TestCancelWaitingVerificationKeepsOthers(t *testing.T) {
	s, batch := concurrentVerifyFixture(t)
	s.Mu.Lock()
	err := s.RememberVerificationConcurrency(1)
	s.Mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	ai := &aiservice.Service{Store: s}
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	started := make(chan string, 12)
	runner := func(ctx context.Context, req aiservice.VerifyRequest, _ workspace.Item, _ workspace.Settings) (workspace.VerifyResult, error) {
		// 实时摘要使用真实回调并保存，不依赖模拟界面自行编造进度。
		aiservice.ReportVerificationProgress(ctx, "已对比旧逻辑与新逻辑，正在检索关键业务日志，手机号13812345678")
		started <- req.ID
		select {
		case <-ctx.Done():
			return workspace.VerifyResult{}, ctx.Err()
		case <-release:
		}
		return workspace.VerifyResult{Status: "通过", Summary: req.ID, Evidence: []string{"模拟独立证据"}}, nil
	}
	for _, id := range batch.IDs[:3] {
		if _, err := ai.EnqueueVerification(aiservice.VerifyRequest{Version: batch.Version, ID: id, Start: batch.Start, End: batch.End}, runner); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { stopVerificationFixture(t, s) })
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("首项未开始")
	}
	s.Mu.Lock()
	queueID := s.VerifyQueue.ID
	rowID := s.VerifyQueue.Rows[1].ID
	activeID := s.VerifyQueue.Rows[0].ID
	s.Mu.Unlock()
	cancelBody, _ := json.Marshal(map[string]string{"queue_id": queueID, "row_id": rowID})
	if _, err := ai.CancelQueuedVerification(cancelBody); err != nil {
		t.Fatal(err)
	}
	// 已开始项不能通过“取消排队”冒充尚未调用模型。
	activeBody, _ := json.Marshal(map[string]string{"queue_id": queueID, "row_id": activeID})
	if _, err := ai.CancelQueuedVerification(activeBody); err == nil {
		t.Fatal("运行项仍被取消为未执行")
	}
	if _, err := ai.EnqueueVerification(aiservice.VerifyRequest{Version: batch.Version, ID: batch.IDs[1], Start: batch.Start, End: batch.End}, runner); err != nil {
		t.Fatal(err)
	}
	if _, err := ai.CancelQueuedVerification(cancelBody); err == nil {
		t.Fatal("旧按钮误取消后来重新排队项")
	}
	once.Do(func() { close(release) })
	q := waitVerifyQueue(t, s)
	if len(q.Rows) != 4 || q.Rows[1].State != "已取消" || q.Rows[3].State != "通过" {
		t.Fatalf("单项取消影响了其他项：%+v", q.Rows)
	}
	// 回看文件必须保留摘要且脱敏，不能只在内存中短暂展示。
	reopened, err := workspace.OpenStore(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	record, ok := reopened.TaskHistory.Get(queueID)
	if !ok || len(record.Rows[0].Progress) < 2 {
		t.Fatal("实时核验进展没有保存到任务历史")
	}
	encoded, _ := json.Marshal(record)
	if strings.Contains(string(encoded), "13812345678") || !strings.Contains(string(encoded), "旧逻辑与新逻辑") {
		t.Fatal("核验进展缺失或未脱敏")
	}
}

// TestVerificationConcurrencyControlPersists 验证页面并发偏好独立保存并拒绝运行中改动。
// 参数 t：临时工作区，不启动任何模型或 SQL。
func TestVerificationConcurrencyControlPersists(t *testing.T) {
	s, _ := concurrentVerifyFixture(t)
	app := &App{Store: s}
	endpoint := app.handlers()["verification/concurrency"]
	if endpoint == nil {
		t.Fatal("并发设置端点未注册")
	}
	if _, err := endpoint([]byte(`{"concurrency":3}`)); err != nil {
		t.Fatal(err)
	}
	// 重新读取完整配置，确认只更改并发数并能跨进程保留。
	reopened, err := workspace.OpenStore(s.Dir)
	if err != nil || reopened.Config().VerificationConcurrency != 3 {
		t.Fatalf("并发设置未保存：%v", err)
	}
	for _, body := range []string{`{"concurrency":0}`, `{"concurrency":7}`, `{"concurrency":2,"code_repository":"other"}`} {
		if _, err := endpoint([]byte(body)); err == nil {
			t.Fatal("非法设置被接受")
		}
	}
	s.VerifyQueue = &workspace.VerifyQueue{ID: "test-running", Running: true, Concurrency: 3}
	if _, err := endpoint([]byte(`{"concurrency":2}`)); err == nil || s.Config().VerificationConcurrency != 3 {
		t.Fatal("运行期间改变了并发偏好")
	}
}

// TestEnqueueAroundCompletionDoesNotLoseWork 验证高速结束与并发入队衔接，不丢失任何已接受请求。
// 参数 t：仅使用临时文件和立即返回的模拟证据。
func TestEnqueueAroundCompletionDoesNotLoseWork(t *testing.T) {
	s, batch := concurrentVerifyFixture(t)
	ai := &aiservice.Service{Store: s}
	var requests sync.WaitGroup
	for _, id := range batch.IDs {
		requests.Add(1)
		go func(id string) {
			defer requests.Done()
			_, err := ai.EnqueueVerification(aiservice.VerifyRequest{Version: batch.Version, ID: id, Start: batch.Start, End: batch.End}, func(_ context.Context, req aiservice.VerifyRequest, _ workspace.Item, _ workspace.Settings) (workspace.VerifyResult, error) {
				return workspace.VerifyResult{Status: "通过", Summary: req.ID, Evidence: []string{"模拟"}}, nil
			})
			if err != nil {
				t.Errorf("收尾期间丢失入队请求：%v", err)
			}
		}(id)
	}
	requests.Wait()
	t.Cleanup(func() { stopVerificationFixture(t, s) })
	waitVerifyQueue(t, s)
	// 各队列可能在不同时间结束，业务记录仍须每项恰好留下一个结果。
	for _, id := range batch.IDs {
		_, item, _, err := s.Find(batch.Version, id)
		if err != nil || len(item.VerifyRuns) != 1 {
			t.Fatalf("已接受请求没有结果：%s %v", id, err)
		}
	}
}

// TestVerificationEnqueueStopsWaitingItems 验证停止后拒绝追加、等待项不运行、迟到结果不回填。
// 参数 t：测试上下文；阻塞模拟取证，不调用真实模型或日志。
func TestVerificationEnqueueStopsWaitingItems(t *testing.T) {
	s, batch := concurrentVerifyFixture(t)
	s.Mu.Lock()
	err := s.RememberVerificationConcurrency(1)
	s.Mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	ai := &aiservice.Service{Store: s}
	started := make(chan string, 12)
	runner := func(ctx context.Context, req aiservice.VerifyRequest, _ workspace.Item, _ workspace.Settings) (workspace.VerifyResult, error) {
		// 故意在取消后返回通过，证明不会接受迟到结论。
		started <- req.ID
		<-ctx.Done()
		return workspace.VerifyResult{Status: "通过", Summary: "迟到结果", Evidence: []string{"不应回填"}}, nil
	}
	for _, id := range batch.IDs[:3] {
		_, err := ai.EnqueueVerification(aiservice.VerifyRequest{Version: batch.Version, ID: id, Start: batch.Start, End: batch.End}, runner)
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { stopVerificationFixture(t, s) })
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("首项未开始")
	}
	s.Mu.Lock()
	id := s.VerifyQueue.ID
	s.Mu.Unlock()
	if err := s.StopTask(id); err != nil {
		t.Fatal(err)
	}
	q := waitVerifyQueue(t, s)
	// 已排队项保持未验证，没有伪造失败历史；运行项保留已停止语义。
	for _, row := range q.Rows {
		if row.State != "未验证" && row.State != "已停止" {
			t.Fatalf("取消结果错误：%+v", row)
		}
		_, item, _, _ := s.Find(batch.Version, row.ItemID)
		if len(item.VerifyRuns) != 0 {
			t.Fatal("取消后写入了迟到结论")
		}
	}
	select {
	case id := <-started:
		t.Fatalf("停止后开始了等待项：%s", id)
	default:
	}
}
