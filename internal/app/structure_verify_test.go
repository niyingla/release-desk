package app

import (
	"strings"
	"testing"

	sqlservice "release-desk/internal/sql"
	"release-desk/internal/storage/workspace"
)

// TestExplicitStructureVerificationIsReadOnly 验证后端令牌校验、逐库配置和审计保留只读身份。
// 参数 t：Go 测试上下文；执行器只检查配置，绝不连接数据库。
func TestExplicitStructureVerificationIsReadOnly(t *testing.T) {
	store, config, preview := uatQueueFixture(t)
	plan := preview.Items[0].Plan
	plan.VerifyOnly = true
	service := &sqlservice.Service{Store: store}
	if _, err := service.ValidateSQLPlan(plan, config); err == nil {
		t.Fatal("只读计划允许了可写目标")
	}
	plan.SchemaCheckOnly = append([]string{}, plan.Databases...)
	if _, err := service.ValidateSQLPlan(plan, config); err != nil {
		t.Fatal(err)
	}
	batch := sqlservice.NewBatch(plan)
	store.Batch = batch
	service.RunBatch(batch, config, []string{plan.SQL}, func(cfg sqlservice.DBConfig, _ string, _ []string) (string, error) {
		if !cfg.SchemaOnly {
			t.Error("只读目标被当作普通执行")
		}
		return "只读结构核验通过", nil
	})
	restarted, err := workspace.OpenStore(store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range restarted.History {
		if !record.VerificationOnly || !record.SchemaOnly || record.State != "成功" {
			t.Fatal("审计丢失只读语义")
		}
	}
	if !strings.Contains(restarted.TaskHistory.List()[0].Title, "只读结构核验") {
		t.Fatal("任务中心误显示为 SQL 写入")
	}
}
