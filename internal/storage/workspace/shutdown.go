package workspace

import (
	"log/slog"
	"time"
)

const ShutdownGracePeriod = 2 * time.Second

const ShutdownPollInterval = 20 * time.Millisecond

// RequestClose 发出应用退出信号，立即阻止新任务并取消已有任务。
// 无业务入参；只在用户关闭主窗口或应用退出收尾时调用。
// 不返回业务值；取消不等于数据库回滚，执行结果仍由独立审计记录。
func (s *Store) RequestClose() {
	// 记录锁保证关闭与启动任务互斥，不能在取消之后又启动一批新 SQL。
	s.Mu.Lock()
	defer s.Mu.Unlock()
	// 原生关闭消息与 run 的收尾都会调用，重复请求不能重复改变任务状态。
	if s.Closing {
		return
	}
	s.Closing = true
	// 取消仅发送停止信号，数据库驱动的真实或未知结果仍由原任务回填。
	// 同一个上下文覆盖 CLI 与数据库，托盘最小化不走此入口。
	if s.CancelTasks != nil {
		s.CancelTasks()
	}
	slog.Info("用户请求退出，正在取消后台任务")
}

// StopWindowTasks 等待短暂收尾并持久化无法确认的执行状态。
// 无业务入参；调用前窗口已经关闭，不能无限等待而占住单实例锁。
// 不返回业务值；超时的 SQL 记录为已中断，应用正常结束并释放互斥句柄。
func (s *Store) StopWindowTasks() {
	// 主动取消后只给审计与 CLI 退出留短暂时间，不再等待完整任务超时。
	s.RequestClose()
	deadline := time.Now().Add(ShutdownGracePeriod)
	for time.Now().Before(deadline) {
		// 读取占用状态后马上释放锁，让被取消的协程继续写入最后一条审计。
		s.Mu.Lock()
		busy := s.AIBusy || s.SQLActive()
		s.Mu.Unlock()
		if !busy {
			// 已确认完成时立即退出，不额外停留到固定超时时刻。
			return
		}
		time.Sleep(ShutdownPollInterval)
		// 只进行短轮询，不在持锁期间等待外部工具。
	}
	// 驱动或子进程失去响应时也允许退出；未确认 SQL 不能被标成成功或普通失败。
	s.Mu.Lock()
	defer s.Mu.Unlock()
	// 只有“执行中”表示可能已经向服务端写入，等待中的库不能伪装为执行失败。
	for i := range s.History {
		if s.History[i].State == "执行中" {
			// 中断表示结果需核对，不能进入默认“重试失败库”的自动集合。
			s.History[i].State = "已中断"
			s.History[i].Finished = Stamp()
			s.History[i].Detail = "用户退出软件，执行结果未确认，请先核对数据库"
		}
	}
	// 审计始终独立落盘，不因窗口退出修改版本发布状态或验收结论。
	if err := s.PersistHistory(); err != nil {
		slog.Error("退出时执行审计保存失败", "error", err)
	}
	slog.Info("后台收尾达到时限，应用退出")
	// 主线程随后结束，命名互斥对象的句柄随 defer 释放，允许下次双击重新启动。
}
