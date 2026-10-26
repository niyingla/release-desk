package sqlservice

import (
	"reflect"
	"testing"
)

// TestDatabaseTargetsUseNumericSuffixOrder 验证模板优先及两端库名尾号按数值升序。
// 参数 t：测试上下文；只筛选虚构库名，不连接数据库或执行 SQL。
func TestDatabaseTargetsUseNumericSuffixOrder(t *testing.T) {
	for _, tc := range []struct {
		side        string
		input, want []string
	}{
		{"服务商端", []string{"sp10001", "sp10", "sp2", "sp1", "sp"}, []string{"sp", "sp1", "sp2", "sp10", "sp10001"}},
		{"租户端", []string{"newlink10", "newlink2", "newlink", "newlink_share10", "newlink_share2", "newlink_share"}, []string{"newlink", "newlink2", "newlink10", "newlink_share", "newlink_share2", "newlink_share10"}},
		{"服务商端", []string{"sp999999999999999999999999999999", "sp10", "sp9", "sp1000000000000000000000000000000"}, []string{"sp9", "sp10", "sp999999999999999999999999999999", "sp1000000000000000000000000000000"}},
	} {
		// 使用真实目标库匹配入口，避免只测试独立比较器却漏接执行预览。
		got := MatchDB(tc.input, tc.side)
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s 排序错误：%v", tc.side, got)
		}
	}
}
