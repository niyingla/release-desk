// 验证交互只负责展示和输入；能否自动执行仍由后端再次校验。
/**
 * 生成当前事项详情中的验证操作。
 * @param {object} item 固定到当前版本的验证事项。
 * @returns {string} 人工项仅允许复制步骤，自动项提供验证及提示词入口。
 */
function verificationDetailActions(item) {
  if (needsManualVerification(item))
    return button("copy-verification", "复制人工验证内容", "copy");
  return (
    button("verify", "AI 智能验证", "spark", "primary") +
    button("copy-prompt", "复制 AI 验证提示词", "copy")
  );
}

/**
 * 组装人工核对和复制需要的完整业务内容。
 * @param {object} item 当前事项数据，不触发请求或自动验收。
 * @returns {string} 原因、目标及步骤的纯文本。
 */
function verificationItemText(item) {
  return [
    item.data.title,
    `验证方式：${item.data.verification_mode || "自动验证"}`,
    item.data.manual_reason ? `手动原因：${item.data.manual_reason}` : "",
    `修改内容：${item.data.change || "—"}`,
    `验证步骤：${item.data.steps || "—"}`,
    `参考日志：${item.data.keywords || "—"}`,
    `预期结果：${item.data.expected || "—"}`,
    item.data.result || "",
  ]
    .filter(Boolean)
    .join("\n\n");
}

/**
 * 显式选择适合接口或定时任务触发周期的时间窗口。
 * @param {string} prefix 单项或批量表单字段前缀。
 * @returns {string} 不会在后台扩大用户已经确认的查询范围。
 */
function verificationRangeControl(prefix) {
  const choices = [
    ["1", "最近 1 小时"],
    ["6", "最近 6 小时"],
    ["24", "最近 24 小时"],
    ["72", "最近 3 天"],
  ];
  return `<label class="form-field">查询范围<select data-verification-range="${escapeHTML(prefix)}">${choices.map(([hours, label]) => `<option value="${hours}">${label}</option>`).join("")}</select><small class="source-note">低频任务可选较长范围，也可直接修改起止时间</small></label>`;
}

/**
 * 将用户明确选择的范围写入当前弹窗，尚不提交取证。
 * @param {HTMLElement} target 当前变更的快捷范围下拉框。
 * @returns {boolean} 已处理范围选择为 true，其余字段交给原流程。
 */
function applyVerificationRange(target) {
  const prefix = target.dataset.verificationRange;
  if (!prefix) return false;
  const end = new Date(),
    start = new Date(end.getTime() - Number(target.value) * 3600000);
  $(`[name="${prefix}-start"]`).value = dateTimeLocal(start);
  $(`[name="${prefix}-end"]`).value = dateTimeLocal(end);
  return true;
}

/**
 * 区分模型确认程度和取证阻塞，不改写持久化的业务结论。
 * @param {string} status 原始状态，排队与运行状态不受影响。
 * @param {object} result 本轮结构化证据，旧记录可以缺失。
 * @returns {string} 用户可直接识别的状态标签。
 */
function verificationStatusLabel(status, result) {
  if (status === "通过" && result?.confirmation === "高概率确认")
    return "通过（高概率）";
  if (status === "证据不足" && result?.blocker) return result.blocker;
  return status;
}
