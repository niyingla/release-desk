package aiservice

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// TestConcurrentPreflightSharesFailure 验证六项同时启动只探测一次，故障不冒充业务失败。
// 参数 t：测试上下文；只使用受控回调，不访问生产或调用模型。
func TestConcurrentPreflightSharesFailure(t *testing.T) {
	service := &Service{}
	var calls atomic.Int32
	release := make(chan struct{})
	failure := errors.New("QA network blocked")
	run := func(context.Context) error { calls.Add(1); <-release; return failure }
	var group sync.WaitGroup
	for range 6 {
		group.Add(1)
		go func() {
			defer group.Done()
			// 每项收到同一受控阻断，不应重复启动网络或后续收费操作。
			if err := service.ensureVerificationAccess(context.Background(), "qa", run); !errors.Is(err, failure) {
				t.Errorf("预检错误丢失：%v", err)
			}
		}()
	}
	close(release)
	group.Wait()
	if calls.Load() != 1 {
		t.Fatalf("并发预检重复 %d 次", calls.Load())
	}
}

// TestPreflightCancellationDoesNotPoisonOtherItems 验证单项取消不污染其他独立任务。
// 参数 t：测试上下文；固定回调确保缓存取消后可重新检查，无外部取证。
func TestPreflightCancellationDoesNotPoisonOtherItems(t *testing.T) {
	service := &Service{}
	// 首次取消结果必须释放缓存，后续独立项不能跟随失败。
	err := service.ensureVerificationAccess(context.Background(), "qa", func(context.Context) error { return context.Canceled })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := service.ensureVerificationAccess(context.Background(), "qa", func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	// 改变主机配置即使处于缓存期也必须独立检查，不能复用其他端点的成功。
	err = service.ensureVerificationAccess(context.Background(), "different-host", func(context.Context) error { return errors.New("different network") })
	if err == nil {
		t.Fatal("配置变化错误复用缓存")
	}
}
