package app

import (
	"context"
	"testing"
	"time"

	aiservice "release-desk/internal/ai"
	sqlservice "release-desk/internal/sql"
	"release-desk/internal/storage/workspace"
)

// TestScanAndSQLRunIndependently 验证两种启动顺序、独立取消和并发合并后的审计持久化。
// 参数 t：Go 测试上下文；扫描和数据库调用均由可控通道模拟，不访问外部服务。
func TestScanAndSQLRunIndependently(t *testing.T) {
	for _, scanFirst := range []bool{true, false} {
		name := "SQL先启动"
		if scanFirst {
			name = "扫描先启动"
		}
		t.Run(name, func(t *testing.T) {
			s, cfg, preview := queueSQLFixture(t)
			ai := &aiservice.Service{Store: s}
			db := &sqlservice.Service{Store: s}
			scanRelease, sqlRelease := make(chan struct{}), make(chan struct{})
			scanEntered, sqlEntered := make(chan struct{}), make(chan struct{}, 1)
			t.Cleanup(func() {
				s.CancelTasks()
				if s.Scan != nil {
					waitForScan(t, s)
				}
				if s.SQLQueue != nil {
					waitSQLQueue(t, s)
				}
			})
			startScan := func() error {
				return ai.StartVersionScan(preview.Version, func(ctx context.Context, _ workspace.VersionFile, _ workspace.Settings) (aiservice.ScanResult, error) {
					close(scanEntered)
					select {
					case <-ctx.Done():
						return aiservice.ScanResult{}, ctx.Err()
					case <-scanRelease:
						return scanFixture(), nil
					}
				})
			}
			startSQL := func() error {
				return db.StartSQLQueue(preview, cfg, func(ctx context.Context, _ sqlservice.DBConfig, _ string, _ []string) (string, error) {
					select {
					case sqlEntered <- struct{}{}:
					default:
					}
					select {
					case <-ctx.Done():
						return "", &sqlservice.DatabaseFailure{Err: ctx.Err(), Uncertain: true}
					case <-sqlRelease:
						return "模拟核验完成", nil
					}
				})
			}
			first, second := startSQL, startScan
			if scanFirst {
				first, second = startScan, startSQL
			}
			if err := first(); err != nil {
				t.Fatal(err)
			}
			if err := second(); err != nil {
				t.Fatalf("独立任务被互斥：%v", err)
			}
			waitConcurrentTask(t, scanEntered)
			waitConcurrentTask(t, sqlEntered)
			if err := startScan(); err == nil {
				t.Fatal("重复扫描未被拒绝")
			}
			if err := startSQL(); err == nil {
				t.Fatal("重复 SQL 批次未被拒绝")
			}
			if scanFirst {
				// SQL 仍在执行时完成扫描合并，再停止 SQL；已生成的审计不得丢失。
				close(scanRelease)
				if result := waitForScan(t, s); result.Error != "" {
					t.Fatal(result.Error)
				}
				s.Mu.Lock()
				active, id := s.SQLActive(), s.SQLQueue.ID
				s.Mu.Unlock()
				if !active {
					t.Fatal("扫描结束提前释放 SQL 占用")
				}
				if err := s.StopTask(id); err != nil {
					t.Fatal(err)
				}
				q := waitSQLQueue(t, s)
				if q.Rows[1].State != "未执行" {
					t.Fatal("停止后仍执行后续脚本")
				}
			} else {
				// 停止扫描只取消自己的上下文，SQL 应继续执行所有目标。
				s.Mu.Lock()
				id := s.Scan.ID
				s.Mu.Unlock()
				if err := s.StopTask(id); err != nil {
					t.Fatal(err)
				}
				if result := waitForScan(t, s); !result.Cancelled {
					t.Fatal("扫描没有取消")
				}
				s.Mu.Lock()
				active := s.SQLActive()
				s.Mu.Unlock()
				if !active {
					t.Fatal("停止扫描同时取消了 SQL")
				}
				close(sqlRelease)
				q := waitSQLQueue(t, s)
				for _, row := range q.Rows {
					if row.State != "成功" {
						t.Fatalf("SQL 未继续完成：%+v", row)
					}
				}
			}
			reopened, err := workspace.OpenStore(s.Dir)
			if err != nil {
				t.Fatal(err)
			}
			wantHistory, wantItems := 6, 3
			if scanFirst {
				wantHistory, wantItems = 1, 4
			}
			if len(reopened.History) != wantHistory || len(reopened.Versions[preview.Version].Items) != wantItems {
				t.Fatal("并发扫描覆盖了版本内容或 SQL 执行审计")
			}
		})
	}
}

// waitConcurrentTask 等待模拟执行器真正进入，避免任务准入回归导致测试永久等待。
// 参数 t：Go 测试上下文；参数 entered：执行器开始处理时发出信号的通道。
func waitConcurrentTask(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("后台任务未进入执行器")
	}
}

// TestSQLFinishesWhileScanContinues 验证 SQL 正常结束或被取消都不影响仍在运行的扫描。
// 参数 t：Go 测试上下文；数据库和模型均为通道驱动的本地模拟。
func TestSQLFinishesWhileScanContinues(t *testing.T) {
	for _, cancelSQL := range []bool{false, true} {
		t.Run(map[bool]string{false: "SQL完成", true: "SQL停止"}[cancelSQL], func(t *testing.T) {
			s, cfg, preview := queueSQLFixture(t)
			t.Cleanup(s.StopWindowTasks)
			scanRelease, sqlEntered := make(chan struct{}), make(chan struct{}, 1)
			if err := (&aiservice.Service{Store: s}).StartVersionScan(preview.Version, func(ctx context.Context, _ workspace.VersionFile, _ workspace.Settings) (aiservice.ScanResult, error) {
				select {
				case <-ctx.Done():
					return aiservice.ScanResult{}, ctx.Err()
				case <-scanRelease:
					return scanFixture(), nil
				}
			}); err != nil {
				t.Fatal(err)
			}
			if err := (&sqlservice.Service{Store: s}).StartSQLQueue(preview, cfg, func(ctx context.Context, _ sqlservice.DBConfig, _ string, _ []string) (string, error) {
				select {
				case sqlEntered <- struct{}{}:
				default:
				}
				if cancelSQL {
					<-ctx.Done()
					return "", &sqlservice.DatabaseFailure{Err: ctx.Err(), Uncertain: true}
				}
				return "完成", nil
			}); err != nil {
				t.Fatal(err)
			}
			waitConcurrentTask(t, sqlEntered)
			if cancelSQL {
				s.Mu.Lock()
				id := s.SQLQueue.ID
				s.Mu.Unlock()
				if err := s.StopTask(id); err != nil {
					t.Fatal(err)
				}
			}
			waitSQLQueue(t, s)
			s.Mu.Lock()
			active := s.AIBusy && s.Scan.Running
			s.Mu.Unlock()
			if !active {
				t.Fatal("SQL 收尾释放了扫描占用")
			}
			close(scanRelease)
			if result := waitForScan(t, s); result.Error != "" || result.Cancelled {
				t.Fatalf("SQL 影响了扫描结果：%+v", result)
			}
		})
	}
}
