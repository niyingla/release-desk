package versioning

import (
	"regexp"
	"strconv"
	"strings"
)

// VersionNumbers 提取完整版本的数字段。
// 参数 name：本方法定位的名称或目标数据库名；所属范围由调用方校验。
// 返回：返回名称中完整的数字段，用于数值版本比较。
func VersionNumbers(name string) []int {
	// 版本比较按数字段处理。
	// 补丁号不能按字符串逐字符比较。
	// 分段数量不同由上层比较函数补零。

	matches := regexp.MustCompile(`\d+`).FindAllString(name, -1)
	out := []int{}

	for _, m := range matches {
		n, _ := strconv.Atoi(m)
		out = append(out, n)
	}
	return out
}

// CompareVersion 比较两个版本的数字顺序。
// 参数 a：待比较的第一个完整版本名称。
// 参数 b：待比较的第二个完整版本名称。
// 返回：a 较大返回 1，较小返回 -1；数字段相同以名称作稳定比较。
func CompareVersion(a, b string) int {
	// 先比较主版本，再逐段比较次版本与补丁。
	// 短版本缺失的段按零看待。
	// 数值更大的版本排列在前。
	// 5.7.10 必须排在 5.7.9 前面。
	// 数字完全相同时使用名称保持确定顺序。
	// 比较不会修改版本原始名称。

	aa, bb := VersionNumbers(a), VersionNumbers(b)

	for i := 0; i < len(aa) || i < len(bb); i++ {
		x, y := 0, 0
		if i < len(aa) {
			x = aa[i]
		}
		if i < len(bb) {
			y = bb[i]
		}
		if x > y {
			return 1
		}
		if x < y {
			return -1
		}
	}

	return strings.Compare(a, b)
}
