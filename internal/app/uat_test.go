package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	sqlservice "release-desk/internal/sql"
	"release-desk/internal/storage/workspace"
)

// TestUATQueueIsIndependent 验证 UAT 批量执行保持环境、失败继续与持久化语义。
// 参数 t：Go 测试上下文；数据库执行器为模拟函数，不连接任何环境。
func TestUATQueueIsIndependent(t *testing.T) {
	s, cfg, preview := uatQueueFixture(t)
	visited := 0
	err := (&sqlservice.Service{Store: s}).StartSQLQueue(preview, cfg, func(_ context.Context, config sqlservice.DBConfig, _ string, _ []string) (string, error) {
		if config.Environment != "uat" {
			t.Error("UAT 被路由到其他环境")
		}
		visited++
		if visited == 2 {
			return "", errors.New("模拟单库业务失败")
		}
		return "模拟完成", nil
	})
	if err != nil {
		t.Fatalf("UAT 执行未支持：%v", err)
	}
	queue := waitSQLQueue(t, s)
	if visited != 6 || len(s.History) != 6 {
		t.Fatalf("UAT 目标不完整：%d", visited)
	}
	if queue.Environment != "uat" || queue.Rows[0].State != "有失败" || queue.Rows[1].State != "成功" {
		t.Fatal("队列没有标注 UAT")
	}
	restarted, err := workspace.OpenStore(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range restarted.History {
		if record.Environment != "uat" {
			t.Fatal("逐库历史未持久化 UAT 环境")
		}
	}
	if !strings.Contains(restarted.TaskHistory.List()[0].Title, "UAT") {
		t.Fatal("任务历史缺少环境信息")
	}
}

// uatQueueFixture 为 UAT 构造隔离模拟队列，与测试环境使用相同主机以验证环境边界。
// 参数 t：Go 测试上下文；不会保存真实凭据或连接数据库。
// 返回：隔离工作区、UAT 连接快照及三条有序 SQL 预览。
func uatQueueFixture(t *testing.T) (*workspace.Store, sqlservice.DBConfig, sqlservice.SQLQueuePreview) {
	t.Helper()
	s, cfg, preview := queueSQLFixture(t)
	cfg.Environment = "uat"
	preview.Environment = "uat"
	for index := range preview.Items {
		preview.Items[index].Plan.Environment = "uat"
	}
	return s, cfg, preview
}

// TestUATQueueRejectsMixedEnvironment 验证整批拒绝，不产生部分执行。
// 参数 t：Go 测试上下文；执行回调一旦触发便视为失败。
func TestUATQueueRejectsMixedEnvironment(t *testing.T) {
	s, cfg, preview := uatQueueFixture(t)
	preview.Items[1].Plan.Environment = "test"
	called := false
	err := (&sqlservice.Service{Store: s}).StartSQLQueue(preview, cfg, func(context.Context, sqlservice.DBConfig, string, []string) (string, error) {
		called = true
		return "", nil
	})
	if err == nil || called || s.SQLQueue != nil || len(s.History) != 0 {
		t.Fatal("环境混合后仍启动执行")
	}
}

// TestUATQueueCanStopAndRetainResults 验证取消保留 UAT 审计，后续脚本不执行。
// 参数 t：Go 测试上下文；模拟执行器只等待取消信号。
func TestUATQueueCanStopAndRetainResults(t *testing.T) {
	s, cfg, preview := uatQueueFixture(t)
	started := make(chan struct{})
	err := (&sqlservice.Service{Store: s}).StartSQLQueue(preview, cfg, func(ctx context.Context, _ sqlservice.DBConfig, _ string, _ []string) (string, error) {
		close(started)
		<-ctx.Done()
		return "", &sqlservice.DatabaseFailure{Err: ctx.Err(), Uncertain: true}
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	s.Mu.Lock()
	id := s.SQLQueue.ID
	s.Mu.Unlock()
	if err = s.StopTask(id); err != nil {
		t.Fatal(err)
	}
	queue := waitSQLQueue(t, s)
	if queue.Environment != "uat" || queue.Rows[1].State != "未执行" || len(s.History) != 1 || s.History[0].Environment != "uat" {
		t.Fatal("停止后环境或已有审计丢失")
	}
}

// TestUATSingleBatchPersistsEnvironment 验证单条脚本复用逐库执行器并独立记录环境。
// 参数 t：Go 测试上下文；仅使用模拟执行器。
func TestUATSingleBatchPersistsEnvironment(t *testing.T) {
	s, cfg, preview := uatQueueFixture(t)
	batch := sqlservice.NewBatch(preview.Items[0].Plan)
	s.Batch = batch
	(&sqlservice.Service{Store: s}).RunBatch(batch, cfg, []string{preview.Items[0].Plan.SQL}, func(config sqlservice.DBConfig, _ string, _ []string) (string, error) {
		if config.Environment != "uat" {
			t.Error("单项执行环境丢失")
		}
		return "模拟完成", nil
	})
	if batch.Environment != "uat" || len(s.History) != 2 || s.History[0].Environment != "uat" {
		t.Fatal("单项逐库审计未隔离环境")
	}
}
