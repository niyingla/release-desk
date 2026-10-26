package aiservice

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"release-desk/internal/storage/fileio"
	"release-desk/internal/storage/workspace"
)

// TestRetryVerificationPreservesHistory 验证失败与证据不足重试后追加历史并持久化新结论。
// 参数 t：测试上下文；只操作临时版本文件，不调用 Codex 或查询生产环境。
func TestRetryVerificationPreservesHistory(t *testing.T) {
	for _, status := range []string{"证据不足", "失败"} {
		t.Run(status, func(t *testing.T) {
			// 使用独立数据目录，验证真实持久化逻辑而不触碰用户发版记录。
			store, err := workspace.OpenStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			previous := workspace.VerifyResult{Status: status, Summary: "首次验证结论", Evidence: []string{"首次证据"}}
			item := workspace.Item{ID: "retry-item", Kind: "verify", VerifyState: status, VerifyRuns: []workspace.VerifyResult{previous}, Data: map[string]string{"title": "重试事项", "change": "修复目标", "result": FormatResult(previous)}}
			version := workspace.VersionFile{Format: "release-desk-version", Schema: 1, Version: workspace.Version{Name: "qa-retry"}, Items: []workspace.Item{item}}
			// 保存第一次结论，重试必须基于该记录追加，不能覆盖整段历史。
			if err := store.Save(version); err != nil {
				t.Fatal(err)
			}
			// 保存会补齐默认动作与状态；启动快照须与生产入口一样从已保存记录读取。
			_, item, _, err = store.Find(version.Version.Name, item.ID)
			if err != nil {
				t.Fatal(err)
			}
			next := workspace.VerifyResult{Status: "通过", Summary: "补证后达到目标", Environment: "生产", Evidence: []string{"本轮独立证据"}}
			// 模拟已经完成的只读核验；正常结果仍通过生产代码的回填与条件校验。
			service := &Service{Store: store}
			if err := service.FinishVerification(context.Background(), VerifyRequest{Version: version.Version.Name, ID: item.ID}, item, next); err != nil {
				t.Fatal(err)
			}
			// 重新打开磁盘记录，不能只检查内存中追加成功。
			reopened, err := workspace.OpenStore(store.Dir)
			if err != nil {
				t.Fatal(err)
			}
			_, got, _, err := reopened.Find(version.Version.Name, item.ID)
			if err != nil || len(got.VerifyRuns) != 2 || got.VerifyState != "通过" {
				t.Fatalf("重试结果或历史未保存：%+v %v", got, err)
			}
			// 原证据和新证据必须分属两次运行，不能把旧结论充当本轮结果。
			if got.VerifyRuns[0].Status != status || got.VerifyRuns[0].Summary != previous.Summary || got.VerifyRuns[0].Evidence[0] != "首次证据" || got.VerifyRuns[1].Evidence[0] != "本轮独立证据" || !strings.Contains(got.Data["result"], next.Summary) {
				t.Fatal("重试覆盖旧历史或未更新本轮结果")
			}
		})
	}
}

// TestSingleVerifyAcceptsMissingHints 验证缺少固定线索仍可提交 AI 自主分析。
// 参数 t：Go 测试上下文；临时无效 CLI 保证不会启动真实模型或查询生产。
func TestSingleVerifyAcceptsMissingHints(t *testing.T) {
	// 独立 PATH 仅含无效 PE 文件，工具发现成功但后台调用一定本地失败。
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "codex.exe"), []byte("QA invalid executable"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	store, err := workspace.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// 仅有改动目标，没有预填服务、步骤、关键词和预期结果。
	v := workspace.VersionFile{Format: "release-desk-version", Schema: 1, Version: workspace.Version{Name: "5.7.3"}, Items: []workspace.Item{{ID: "fix-a", Kind: "verify", Data: map[string]string{"title": "防止重复创建排班", "change": "按业务日期建立唯一性保护"}}}}
	if err = store.Save(v); err != nil {
		t.Fatal(err)
	}
	end := time.Now().In(time.FixedZone("Asia/Shanghai", 8*60*60)).Add(-time.Minute)
	req := VerifyRequest{Version: v.Version.Name, ID: "fix-a", Start: end.Add(-time.Hour).Format("2006-01-02T15:04"), End: end.Format("2006-01-02T15:04")}
	if err = (&Service{Store: store}).StartVerify(req); err != nil {
		t.Fatalf("缺参考线索仍应提交分析：%v", err)
	}
	// 等待故障收尾，避免测试恢复 PATH 后后台意外发现真实 CLI。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		store.Mu.Lock()
		done := store.SingleVerify != nil && !store.SingleVerify.Running
		state := ""
		if done {
			state = store.SingleVerify.Result.Status
		}
		store.Mu.Unlock()
		if done {
			if state != "证据不足" {
				t.Fatalf("工具故障不能当作修复失败或通过：%s", state)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("隔离验证任务未收尾")
}

// TestVerificationPromptCarriesCodeContext 验证 AI 能定位版本代码且不会受旧结论影响。
// 参数 t：Go 测试上下文；只生成内存提示词，不调用模型。
func TestVerificationPromptCarriesCodeContext(t *testing.T) {
	config := workspace.DefaultSettings()
	config.CodeRepository = filepath.Join(t.TempDir(), "code")
	config.SQLRepository = filepath.Join(t.TempDir(), "sql")
	item := workspace.Item{Kind: "verify", Action: "修改", Data: map[string]string{"title": "阻止重复排班", "source_evidence": "abc123 TaskService#create", "result": "旧结论：通过"}}
	prompt := VerificationPrompt("5.7.3", item, "2026-10-16T10:00", "2026-10-16T11:00", config)
	// 检查实际序列化的业务上下文，来源与仓库不能只停留在界面文案。
	start := strings.LastIndex(prompt, "\n{")
	if start < 0 {
		t.Fatal("提示词缺少 JSON 业务上下文")
	}
	var data struct {
		CodeRepository string            `json:"code_repository"`
		SQLRepository  string            `json:"sql_repository"`
		Action         string            `json:"action"`
		Change         map[string]string `json:"change"`
	}
	if err := json.Unmarshal([]byte(prompt[start:]), &data); err != nil {
		t.Fatal(err)
	}
	if data.CodeRepository != config.CodeRepository || data.SQLRepository != config.SQLRepository || data.Action != "修改" || data.Change["source_evidence"] != "abc123 TaskService#create" || data.Change["result"] != "" {
		t.Fatal("AI 缺少代码定位信息或旧结论泄漏到本轮判断")
	}
}

// TestVerificationResultNeedsSubstantiveEvidence 验证不同证据类型均可用，空白不能冒充证据。
// 参数 t：Go 测试上下文；仅处理临时结果 JSON，不访问真实日志。
func TestVerificationResultNeedsSubstantiveEvidence(t *testing.T) {
	for _, test := range []struct {
		name, want string
		evidence   []string
	}{
		{"blank", "证据不足", []string{"", " \n"}},
		{"runtime-state", "通过", []string{"已核对部署 SHA，生产只读运行状态与修复目标一致"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "result.json")
			result := workspace.VerifyResult{Status: "通过", Environment: "生产", Summary: "独立核验结论", Evidence: test.evidence}
			if err := fileio.WriteJSON(path, result); err != nil {
				t.Fatal(err)
			}
			// 应检查证据是否有内容，不要求出现某个固定日志词。
			got, err := ReadVerificationResult(path)
			if err != nil || got.Status != test.want {
				t.Fatalf("证据语义错误：%+v, %v", got, err)
			}
		})
	}
}

// TestVerificationResultPreservesJointAnalysis 防止代码、日志和综合判断在结果读取与复制时丢失。
// 参数 t：测试上下文；只读临时 JSON，不调用真实模型或生产日志。
func TestVerificationResultPreservesJointAnalysis(t *testing.T) {
	// 模型判断不依赖固定关键词：旧检索词未命中，仍可依据实际业务链路给出通过。
	path := filepath.Join(t.TempDir(), "result.json")
	content := `{"status":"通过","summary":"重复请求由唯一性保护拦截","environment":"生产","window":"2026-10-22 10:00 ~ 11:00","evidence":["已发布提交 abc123 TaskService#create","10:30 同业务日期两次请求仅产生一次任务"],"next_steps":"","analysis":{"goal":"同业务日期不得重复创建","code":"写入主库时原子校验唯一键","logs":"预填词未命中；沿请求链查到重复请求被拦截","reasoning":"代码保护与同一业务请求的日志相互印证，修复目标已体现"}}`
	// 写入完整响应，验证真实读取器不会丢弃扩展的分析信息。
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadVerificationResult(path)
	if err != nil || got.Status != "通过" {
		t.Fatalf("不应按固定检索词否定 AI 判断：%+v %v", got, err)
	}
	// 用户复制结果时应得到推理依据，而不只是最后的状态。
	text := FormatResult(got)
	for _, want := range []string{"同业务日期不得重复创建", "写入主库时原子校验唯一键", "沿请求链查到重复请求被拦截", "修复目标已体现"} {
		if !strings.Contains(text, want) {
			t.Fatalf("结果丢失分析内容 %q：%s", want, text)
		}
	}
}
