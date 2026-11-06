package workspace

import "testing"

// TestSQLLifecycleNormalization 验证明确退役状态及旧标题标记生效，不误判普通描述。
// 参数 t：使用虚构 SQL 的协议测试，不读写用户版本或连接数据库。
func TestSQLLifecycleNormalization(t *testing.T) {
	for _, test := range []struct {
		name, lifecycle, description, want string
	}{
		{name: "初始化", want: "有效"},
		{name: "初始化", lifecycle: "已退役", want: "已退役"},
		{name: "历史初始化【已退役，勿执行】", want: "已退役"},
		{name: "历史初始化 [已退役, 勿执行]", lifecycle: "有效", want: "已退役"},
		{name: "未退役的规则", description: "查询已退役业务的数据", want: "有效"},
	} {
		t.Run(test.name+test.lifecycle, func(t *testing.T) {
			item := Item{ID: "sql", Kind: "sql", Data: map[string]string{"name": test.name, "lifecycle": test.lifecycle, "description": test.description, "side": "服务商端", "sql": "INSERT INTO demo(id) VALUES(1);"}}
			if err := NormalizeItem(&item); err != nil {
				t.Fatal(err)
			}
			if item.Data["lifecycle"] != test.want {
				t.Fatalf("脚本状态 = %q，期望 %q", item.Data["lifecycle"], test.want)
			}
		})
	}
	item := Item{ID: "sql", Kind: "sql", Data: map[string]string{"name": "初始化", "lifecycle": "拼错状态", "side": "服务商端", "sql": "INSERT INTO demo(id) VALUES(1);"}}
	if err := NormalizeItem(&item); err == nil {
		t.Fatal("未知脚本状态不应进入版本文件")
	}
}

// TestSQLRetirementSurvivesExternalRefresh 验证 AI 刷新或旧文件省略字段不能重新启用退役 SQL。
// 参数 t：纯内存版本快照，不写入用户数据。
func TestSQLRetirementSurvivesExternalRefresh(t *testing.T) {
	old := VersionFile{Items: []Item{{ID: "sql", Kind: "sql", Data: map[string]string{"name": "旧初始化", "lifecycle": "已退役"}}}}
	incoming := VersionFile{Items: []Item{{ID: "sql", Kind: "sql", Data: map[string]string{"name": "刷新后的初始化", "lifecycle": "有效"}}}}
	merged, err := MergeExternalVersion(old, incoming)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Items[0].Data["lifecycle"] != "已退役" {
		t.Fatal("外部刷新重新启用了已退役的 SQL")
	}
}
