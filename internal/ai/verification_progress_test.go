package aiservice

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestVerificationEventsExposeOnlySummaries 验证事件流只消费公开核验摘要而非内部推理或工具正文。
// 参数 t：测试上下文；输入为虚构 JSONL，不启动 Codex。
func TestVerificationEventsExposeOnlySummaries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var summaries []string
	writer := newVerificationEventWriter(ctx, func(text string) { summaries = append(summaries, text) })
	// 内部推理、原始日志、最终 JSON 和没有约定前缀的消息都不成为进度。
	for _, entry := range []map[string]any{
		{"type": "item.completed", "item": map[string]any{"type": "reasoning", "text": "内部内容"}},
		{"type": "item.completed", "item": map[string]any{"type": "command_execution", "aggregated_output": "原始日志"}},
		{"type": "item.completed", "item": map[string]any{"type": "agent_message", "text": "{\"status\":\"通过\"}"}},
		{"type": "item.completed", "item": map[string]any{"type": "agent_message", "text": "核验进展：已核对旧逻辑与新逻辑，正在检索关键业务日志"}},
	} {
		payload, _ := json.Marshal(entry)
		payload = append(payload, '\n')
		// 模拟 stdout 跨多次 Write 切断 JSON，仍须精确恢复事件边界。
		_, _ = writer.Write(payload[:7])
		_, _ = writer.Write(payload[7:])
	}
	if len(summaries) != 1 || !strings.Contains(summaries[0], "旧逻辑与新逻辑") {
		t.Fatalf("事件过滤或分片错误：%v", summaries)
	}
	cancel()
	_, _ = writer.Write([]byte("{\"type\":\"item.started\",\"item\":{\"type\":\"command_execution\"}}\n"))
	if len(summaries) != 1 {
		t.Fatal("取消后仍显示迟到进度")
	}
}

// TestVerificationProgressRedactsSensitiveText 验证摘要脱敏、截断以及超大事件后的恢复。
// 参数 t：测试上下文；仅处理虚构标记，不读取任何凭据。
func TestVerificationProgressRedactsSensitiveText(t *testing.T) {
	got := sanitizeVerificationSummary("检索关键日志 password=fake-password token=fake-token Authorization: Bearer fake-bearer 手机号13812345678")
	for _, sensitive := range []string{"fake-password", "fake-token", "fake-bearer", "13812345678"} {
		if strings.Contains(got, sensitive) {
			t.Fatalf("摘要未脱敏：%s", sensitive)
		}
	}
	var summaries []string
	writer := newVerificationEventWriter(context.Background(), func(text string) { summaries = append(summaries, text) })
	// 异常超大输出不能无限积累，也不能破坏后续合法进度的读取。
	_, _ = writer.Write([]byte(strings.Repeat("x", 128*1024) + "\n"))
	_, _ = writer.Write([]byte("{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"核验进展：正在核对修复分支\"}}\n"))
	if len(summaries) != 1 {
		t.Fatalf("超长事件后未恢复：%v", summaries)
	}
}
