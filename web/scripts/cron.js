// Cron 中文解释统一在本机生成；第三方解析器与中文语言包随 EXE 离线分发。
(() => {
  /**
   * 将纯表达式解释为中文，兼容标准 5 段及 Quartz 6/7 段。
   * @param {string} value 原始 Cron 输入，不包含人工频率说明。
   * @returns {{text:string, valid:boolean, empty:boolean}} 显示文案及解析状态；不计算或启动调度。
   */
  function describe(value) {
    const expression = String(value || "").trim();
    if (!expression)
      return { text: "未填写 Cron，实际调度待确认", valid: false, empty: true };
    const parts = expression.split(/\s+/);
    try {
      // 标准 Cron 的星期从 0 开始；XXL-Job 的 Quartz 从 1（周日）开始。
      // 拒绝零步长，避免描述库将 /0 解释成“每隔 0 分钟”。
      if (
        ![5, 6, 7].includes(parts.length) ||
        /\/0+(?=[,\s]|$)/.test(expression)
      )
        throw new Error("invalid cron");
      const text = globalThis.cronstrue.toString(expression, {
        locale: "zh_CN",
        use24HourTimeFormat: true,
        dayOfWeekStartIndexZero: parts.length === 5,
        throwExceptionOnParseError: true,
      });
      // 仅简化没有日期限制的固定时间；周、月、年份等限制保留解析器完整结果。
      return {
        text: text.replace(/^在(\d{2}:\d{2}(?::\d{2})?)$/, "每天 $1"),
        valid: true,
        empty: false,
      };
    } catch {
      // 输入未完成时也明确提示，不沿用上一次成功结果误导用户。
      return {
        text: "无法解析，请检查 Cron 表达式",
        valid: false,
        empty: false,
      };
    }
  }

  /**
   * 为表格拼接表达式与括号解释，原始复制值由表格另行保留。
   * @param {string} value 已保存的纯 Cron 表达式，未知频率允许为空。
   * @returns {string} 仅用于显示的纯文本；调用方负责 HTML 转义。
   */
  function display(value) {
    // 空频率不能伪装成任何已确认的计划。
    return value ? `${value}（${describe(value).text}）` : "—";
  }

  /**
   * 按当前输入实时更新表单解释及无效提示。
   * @param {HTMLInputElement} input 当前定时任务的 frequency 输入框。
   * @returns {void} 只更新关联提示；不改变输入、不请求网络、不保存记录。
   */
  function updatePreview(input) {
    const preview = document.getElementById("cron-preview");
    if (!preview || !input) return;
    const result = describe(input.value);
    // 使用 textContent，错误输入中的标签也不会执行或改变弹窗结构。
    preview.textContent = result.text;
    preview.classList.toggle("invalid", !result.valid && !result.empty);
    input.setAttribute("aria-invalid", String(!result.valid && !result.empty));
    input.setAttribute("aria-describedby", "cron-preview");
  }

  globalThis.ReleaseCron = { describe, display, updatePreview };
})();
