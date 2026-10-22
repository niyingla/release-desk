package workspace

import "testing"

// TestConfigRequirementSurvivesRestart 验证配置要求持久化及旧文件的保守缺省行为。
// 参数 t：Go 测试上下文；全部使用临时目录，不读取用户版本文件。
func TestConfigRequirementSurvivesRestart(t *testing.T) {
	// 三种显式选择和旧文件缺字段均经过实际保存、重启读取。
	for _, test := range []struct{ name, input, want string }{
		{"legacy", "", "待确认"},
		{"required", "必须配置", "必须配置"},
		{"optional", "可选配置", "可选配置"},
		{"unknown", "待确认", "待确认"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := OpenStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			// 缺字段模拟旧版文件，不把缺失信息推断为代码已有默认值。
			data := map[string]string{"filename": "service.yml", "content": "feature:\n  enabled: true"}
			if test.input != "" {
				data["required"] = test.input
			}
			version := VersionFile{Format: "release-desk-version", Schema: 1, Version: Version{Name: "5.7.3"}, Items: []Item{{ID: "config-flag", Kind: "config", Data: data}}}
			if err = store.Save(version); err != nil {
				t.Fatal(err)
			}
			// 重新建立工作区，检查文件读取结果而非仅检查输入对象。
			restarted, err := OpenStore(store.Dir)
			if err != nil {
				t.Fatal(err)
			}
			if got := restarted.Versions["5.7.3"].Items[0].Data["required"]; got != test.want {
				t.Fatalf("配置要求丢失或被误判：got %q, want %q", got, test.want)
			}
		})
	}
}

// TestConfigRequirementRejectsAmbiguousValue 防止 AI 或外部文件写入不可识别的要求。
// 参数 t：Go 测试上下文；只验证事项协议，不调用数据库或外部服务。
func TestConfigRequirementRejectsAmbiguousValue(t *testing.T) {
	// 字符串 false 不能隐式解释为可选，调用方应使用约定选项。
	item := Item{ID: "config-flag", Kind: "config", Data: map[string]string{"filename": "service.yml", "required": "false"}}
	if err := NormalizeItem(&item); err == nil {
		t.Fatal("无效配置要求被接受")
	}
}
