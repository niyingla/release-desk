// 生命周期来自后端规范化字段，旧标题标记在文件导入时统一转换。
const retiredSQLHelp =
  "该 SQL 已退役，仅供查看、复制和查阅历史，不能执行、重试或核验";

/**
 * 判断当前事项是否已被停用，不根据历史成功或失败结果推断执行资格。
 * @param {object} item 当前版本中的事项；其他类型不适用 SQL 退役规则。
 * @returns {boolean} 后端已规范化为退役的 SQL 返回 true。
 */
function isRetiredSQL(item) {
  // 新状态只约束 SQL，不能影响配置或手动验证项的原有操作。
  return item.kind === "sql" && item.data.lifecycle === "已退役";
}

/**
 * 生成 SQL 详情的操作区，退役后保留历史入口但不提供新的数据库任务。
 * @param {object} item 当前详情绑定的 SQL，不从界面高亮或其他表格推断。
 * @returns {string} 详情操作 HTML；复制按钮仍由通用详情组件保留。
 */
function sqlDetailActions(item) {
  // 退役提示说明按钮消失的原因，原执行记录仍能独立打开查看。
  // 本处只决定展示资格，旧预览能否执行仍由后台重新检查当前记录。
  const execution = isRetiredSQL(item)
    ? `<span title="${escapeHTML(retiredSQLHelp)}">${badge("已退役")}</span>`
    : button("execute", "一键执行", "play", "primary") +
      button("retry", "重试失败库", "refresh") +
      button("check-structure", "仅核验结构", "check");
  // 展示历史不会创建新预览，也不复用退役前留下的执行令牌。
  return execution + button("history", "执行明细", "clock");
}
