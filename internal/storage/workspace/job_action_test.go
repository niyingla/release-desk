package workspace

import "testing"

// TestJobClosureSurvivesRestart 验证待关闭操作可保存，并独立于实际发布进度。
// 参数 t：Go 测试上下文；仅使用临时目录，不调用调度系统。
func TestJobClosureSurvivesRestart(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// “关闭”是本次发布需要做的操作，不代表已经执行或已经发布。
	v := VersionFile{Format: "release-desk-version", Schema: 1, Version: Version{Name: "5.7.3"}, Items: []Item{{ID: "job-close", Kind: "job", Action: "关闭", Status: "待处理", Online: "待上线", Data: map[string]string{"handler": "syncJob", "frequency": "0 0 * * * ?"}}}}
	if err = store.Save(v); err != nil {
		t.Fatalf("关闭操作不能保存：%v", err)
	}
	// 从文件重新读取，避免只验证内存里未真正保存的表单数据。
	restarted, err := OpenStore(store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	got := restarted.Versions["5.7.3"].Items[0]
	if got.Action != "关闭" || got.Online != "待上线" || got.Status != "待处理" {
		t.Fatalf("操作类型或独立发布状态丢失：%+v", got)
	}
}

// TestClosureIsOnlyForJobs 防止定时任务的关闭语义扩散到配置或其他类型。
// 参数 t：Go 测试上下文；只校验协议，不读写实际发布文件。
func TestClosureIsOnlyForJobs(t *testing.T) {
	for _, test := range []struct {
		kind, field, action string
		valid               bool
	}{
		{"job", "handler", "新增", true},
		{"job", "handler", "修改", true},
		{"job", "handler", "关闭", true},
		{"job", "handler", "删除", true},
		{"config", "filename", "关闭", false},
		{"topic", "topic", "关闭", false},
	} {
		// 旧“删除”仍合法，升级不能把已有删除记录悄悄改成关闭。
		item := Item{ID: "item", Kind: test.kind, Action: test.action, Data: map[string]string{test.field: "sample"}}
		err := NormalizeItem(&item)
		if (err == nil) != test.valid {
			t.Errorf("%s/%s 校验结果错误：%v", test.kind, test.action, err)
		}
	}
}
