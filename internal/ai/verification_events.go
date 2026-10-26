package aiservice

import (
	"context"
	"encoding/json"
	"strings"

	"release-desk/internal/policy"
)

// verificationEventWriter 只消费明确可公开的 JSONL 进度，原始输出不写盘。
type verificationEventWriter struct {
	ctx      context.Context
	emit     func(string)
	line     []byte
	dropping bool
}

// newVerificationEventWriter 为一次 CLI 调用创建独立事件消费者。
// 参数 ctx：本项取消上下文；参数 emit：只接收过滤后行动摘要的回调。
// 返回：供 exec.Stdout 串行写入的消费者，不与其他并行调用共享缓冲区。
func newVerificationEventWriter(ctx context.Context, emit func(string)) *verificationEventWriter {
	// 缓冲仅用于重组分片的一行 JSON，不保存整段 CLI 输出。
	return &verificationEventWriter{ctx: ctx, emit: emit}
}

// Write 按 JSONL 边界处理分片事件，超长行丢弃后继续读取。
// 参数 payload：CLI stdout 的本次字节片段，可能包含半行或多行。
// 返回：始终消费全部输入；未知和异常事件不影响主进程及最终 JSON 结果。
func (w *verificationEventWriter) Write(payload []byte) (int, error) {
	for _, char := range payload {
		// JSONL 的换行才表示一个事件结束，Write 的调用次数不代表消息边界。
		if char == '\n' {
			// 只有完整且有界的事件进入解码，工具大段输出不会耗尽内存。
			if !w.dropping && w.ctx.Err() == nil {
				w.consumeEvent(w.line)
			}
			w.line = w.line[:0]
			// 清空标记允许下一条正常事件继续展示，异常行不永久破坏进度流。
			w.dropping = false
			continue
		}
		// 超过上限后持续消费到下个换行，不能把残余尾部误当成新事件。
		if w.dropping {
			continue
		}
		if len(w.line) >= policy.MaxProgressEventBytes {
			// 丢弃整条事件而非截断 JSON，避免错误解码部分敏感工具输出。
			w.dropping = true
			w.line = w.line[:0]
			continue
		}
		// 缓冲只累积当前有界事件，其他任务拥有各自的 Writer。
		w.line = append(w.line, char)
	}
	// 即使已经取消也继续排空输出，避免子进程因管道写满拖延退出。
	return len(payload), nil
}

// consumeEvent 提取模型明确标记的进度摘要或通用取证阶段。
// 参数 line：有界且完整的一行 JSONL；内部推理、命令输出和最终 JSON 均不展示。
// 不返回业务值；未知协议项静默忽略，兼容 CLI 事件字段扩展。
func (w *verificationEventWriter) consumeEvent(line []byte) {
	var event struct {
		Type string `json:"type"`
		Item struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"item"`
	}
	// 不消费其他字段，命令、工具响应、原始日志和 reasoning 文本不会进入界面。
	if json.Unmarshal(line, &event) != nil {
		// stdout 的非协议提示不作为用户可见的业务发现。
		return
	}
	// 仅展示模型已经完成的一条公开说明，不接收 reasoning 类型或内部增量。
	if event.Type == "item.completed" && event.Item.Type == "agent_message" {
		// 只有约定前缀的公开行动摘要可展示，最终结构化结论仍从独立文件读取。
		if summary, ok := strings.CutPrefix(strings.TrimSpace(event.Item.Text), "核验进展："); ok {
			w.emit(summary)
		}
	}
	// 通用活动只说明确实发生了取证调用，不根据命令参数推断生产验证成功。
	if event.Type == "item.started" && (event.Item.Type == "command_execution" || event.Item.Type == "mcp_tool_call") {
		// 模型暂未报告发现时仍展示真实取证活动，不猜测命令内容或暴露参数。
		w.emit("正在执行只读取证步骤，等待代码或日志核对结果")
	}
}
