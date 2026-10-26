package schema

import "strings"

// alterChanges 跟踪同一 ALTER 中已保留的动作，防止用初始结构省略后续依赖项。
type alterChanges struct {
	objects       map[string]bool
	layoutChanged bool
}

// affects 判断前文动作是否使当前子句的初始元数据失效。
// 参数 clause：按作者顺序处理的当前完整子句，包含字段、索引及位置约束。
// 返回：依赖同名对象或已变化列顺序时为 true，必须保留原动作。
func (c *alterChanges) affects(clause Clause) bool {
	for _, check := range clause.Checks {
		// DROP 后重新 ADD、连续 CHANGE 等必须使用前文动作之后的对象状态。
		if c.objects[check.Kind+"|"+strings.ToLower(check.Name)] {
			return true
		}
		// 新增或移动其他字段也可能改变相对位置，MODIFY/CHANGE 不能据旧顺序省略。
		if check.Kind == "position" && c.layoutChanged && !clause.SkipExisting {
			return true
		}
	}
	// 无关 ADD 仍可跳过真实已存在的对象，不把同库所有动作都当作相互依赖。
	return false
}

// record 记录一个已经保留的子句所影响的对象与列布局。
// 参数 clause：即将发送的 ALTER 子句，不接受已省略的动作。
// 无返回值；影响范围仅在当前单条 ALTER 内使用，不跨库或跨语句复用。
func (c *alterChanges) record(clause Clause) {
	for _, check := range clause.Checks {
		// 对象类别参与键值，避免同名字段和索引互相遮盖。
		c.objects[check.Kind+"|"+strings.ToLower(check.Name)] = true
		// 保守保留后续位置动作，最终仍由实际 ORDINAL_POSITION 验证。
		if check.Kind == "column" || check.Kind == "position" {
			c.layoutChanged = true
		}
	}
}
