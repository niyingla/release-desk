package workspace

import (
	"errors"
	"strings"
)

const (
	VerificationAutomatic     = "自动验证"
	VerificationManual        = "手动验证"
	VerificationManualPending = "需手动验证"
)

// NeedsManualVerification 判断关键结果是否明确需要人工核验。
// 参数 item：当前版本事项，旧记录没有方式字段时保留原自动验证流程。
// 返回：仅验证事项且明确声明人工方式时为 true，不根据历史证据不足推测。
func NeedsManualVerification(item Item) bool {
	// 可观察性由扫描或用户明确维护，不能用一次空查询代替分类。
	return item.Kind == "verify" && (item.Data["verification_mode"] == VerificationManual || item.Data["verification_mode"] == "" && item.VerifyState == VerificationManualPending)
}

// PendingVerificationState 取得本轮条件尚未验收的正确状态。
// 参数 item：新建或条件发生变化的独立事项，不读取历史成功结论。
// 返回：人工项为需手动验证，其他事项保持待验证。
func PendingVerificationState(item Item) string {
	// 人工分类不是验收通过，只表示自动日志取证无法证明关键结果。
	if NeedsManualVerification(item) {
		return VerificationManualPending
	}
	// 自动项不继承人工分类，变更方式后必须重新产生有效证据。
	return "待验证"
}

// ValidateVerificationMode 校验扫描或编辑声明的可观察性分类。
// 参数 item：待保存事项；手动验证必须描述不可观察原因及人工操作方向。
// 返回：不合法方式或缺少人工原因时拒绝，旧记录的空方式保持兼容。
func ValidateVerificationMode(item Item) error {
	mode := item.Data["verification_mode"]
	// 空方式表示尚未迁移的历史协议，不能因升级就重写既有验收结果。
	// 只维护验证类字段，其他表格不能意外继承人工验收规则。
	if item.Kind != "verify" || mode == "" || mode == VerificationAutomatic {
		return nil
	}
	if mode != VerificationManual {
		// 错误枚举直接拒绝，避免拼写偏差被当作可自动取证。
		return errors.New("验证方式只能为自动验证或手动验证")
	}
	// 明确原因才能判断后续需要人工操作还是补充日志观测点。
	if strings.TrimSpace(item.Data["manual_reason"]) == "" {
		return errors.New("手动验证需要说明关键结果无法从日志判断的原因")
	}
	return nil
}
