package workspace

import (
	"errors"
	"regexp"
	"slices"
)

const (
	SQLLifecycleActive  = "有效"
	SQLLifecycleRetired = "已退役"
)

var sqlLifecycles = []string{SQLLifecycleActive, SQLLifecycleRetired}

// 只兼容明确的标题标记，不根据描述中提到退役业务等普通文字停用脚本。
var sqlRetirementMarker = regexp.MustCompile(`(?:^|[【\[（(])\s*已退役(?:[，,、:：;；\s】\]）)]|$)`)

var ErrRetiredSQL = errors.New("该 SQL 已退役，仅供查看、复制和查阅历史，不能执行、重试或核验")

// IsRetiredSQL 判断 SQL 是否已经退役，兼容旧版本只有标题标记的记录。
// 参数 item：当前版本中的事项快照；非 SQL 类型始终返回 false。
// 返回：明确状态或旧标题表示退役时为 true；不根据执行历史推断可执行性。
func IsRetiredSQL(item Item) bool {
	// 退役属于脚本生命周期，不能与历史成功、待办完成或版本发布状态混用。
	if item.Kind != "sql" {
		return false
	}
	// 标题仍含明确禁用标记时不能被旧客户端补写的“有效”值重新启用。
	return item.Data["lifecycle"] == SQLLifecycleRetired || sqlRetirementMarker.MatchString(item.Data["name"])
}

// NormalizeSQLLifecycle 规范化脚本状态，将旧标题中的明确退役标记转成协议字段。
// 参数 data：已通过 SQL 基本字段检查的字符串字典；缺省状态兼容旧的有效脚本。
// 返回：成功补全 lifecycle；未知取值返回错误，不写入版本文件。
func NormalizeSQLLifecycle(data map[string]string) error {
	// 新状态必须先校验，拼写错误不能被当作可执行的缺省值。
	if data["lifecycle"] != "" && !slices.Contains(sqlLifecycles, data["lifecycle"]) {
		return errors.New("SQL 脚本状态应为有效或已退役")
	}
	// 普通旧记录继续可用，只迁移明确标记，不扫描 SQL 正文猜测业务用途。
	if data["lifecycle"] == "" {
		data["lifecycle"] = SQLLifecycleActive
	}
	// 兼容迁移不会删除原脚本和审计，也不改写曾经执行成功或失败的事实。
	if IsRetiredSQL(Item{Kind: "sql", Data: data}) {
		data["lifecycle"] = SQLLifecycleRetired
	}
	return nil
}

// RequireExecutableSQL 在目标发现和预览确认前统一拒绝不可执行事项。
// 参数 item：后端按版本及稳定 ID 查得的当前记录，不使用客户端声称的状态。
// 返回：有效 SQL 返回 nil；非 SQL 或退役脚本返回明确错误，不访问数据库。
func RequireExecutableSQL(item Item) error {
	// 入口类型必须明确，不能把其他事项的同名数据字段送入 SQL 执行器。
	if item.Kind != "sql" {
		return errors.New("只能执行 SQL 记录")
	}
	// 复核和失败重试同样属于新任务，退役后只保留原执行历史供查询。
	if IsRetiredSQL(item) {
		return ErrRetiredSQL
	}
	// 通过仅代表允许继续规划，实际执行仍须经过目标预览与用户勾选确认。
	return nil
}
