// 勾选只存在当前版本与表格中；切换业务类型后清空，避免隐藏选择被误执行。
let bulkCheckedIDs = new Set();
let bulkSelectionContext = "";
let bulkPreview = null;
let bulkVerifyIDs = [];
let bulkProgressKind = "";
// 保留用户展开的逐库结果，状态轮询不得不断折叠正在查看的历史脚本。
const bulkExpandedState = new Map();

/**
 * 将勾选限制在当前版本和事项类型，移除已经删除的记录。
 * @param {object} version 当前完整版本快照。
 * @returns {void} 不修改发布记录，仅更新界面选择。
 */
function syncBulkSelection(version) {
  const context = `${currentVersion}|${currentKind}`;
  if (bulkSelectionContext !== context) bulkCheckedIDs.clear();
  bulkSelectionContext = context;
  const valid = new Set(
    version.items
      .filter((item) => item.kind === currentKind)
      .map((item) => item.id),
  );
  bulkCheckedIDs = new Set([...bulkCheckedIDs].filter((id) => valid.has(id)));
}

/**
 * 渲染 SQL 或变更验证表的勾选与批量操作入口。
 * @returns {string} 只包含本表格操作，不自动启动任何后台任务。
 */
function bulkControls() {
  if (!["sql", "verify"].includes(currentKind)) return "";
  const queue = currentKind === "sql" ? state.sql_queue : state.verify_queue;
  return `<div class="bulk-tools"><button class="text-link" data-action="bulk-select-all">全选</button><span class="bulk-count">已选 ${bulkCheckedIDs.size} 项</span>${button(currentKind === "sql" ? "bulk-execute" : "bulk-verify", currentKind === "sql" ? "批量执行" : "批量验证", "play", "", bulkCheckedIDs.size ? "" : "disabled")}${queue?.version === currentVersion ? button("bulk-progress", queue.running ? "查看进度" : "批次结果", "clock") : ""}</div>`;
}

/**
 * 渲染当前行的复选框，名称只作为可访问性纯文本。
 * @param {object} item 当前表格中的单条事项。
 * @returns {string} 勾选单元格，非批量表格返回空字符串。
 */
function bulkRowCheckbox(item) {
  if (!["sql", "verify"].includes(currentKind)) return "";
  return `<td class="table-check-cell"><input type="checkbox" data-bulk-check="${escapeHTML(item.id)}" aria-label="选择 ${escapeHTML(itemTitle(item))}" ${bulkCheckedIDs.has(item.id) ? "checked" : ""}></td>`;
}

/**
 * 同步全选的半选状态、计数和按钮可用性。
 * @returns {void} 不重建表格，连续勾选不会影响当前行详情或滚动位置。
 */
function updateBulkChecks() {
  const boxes = [...document.querySelectorAll("[data-bulk-check]")];
  const selected = boxes.filter((box) =>
    bulkCheckedIDs.has(box.dataset.bulkCheck),
  ).length;
  const all = document.querySelector("[data-bulk-all]");
  if (all) {
    all.checked = boxes.length > 0 && selected === boxes.length;
    all.indeterminate = selected > 0 && selected < boxes.length;
  }
  const count = document.querySelector(".bulk-count");
  if (count) count.textContent = `已选 ${bulkCheckedIDs.size} 项`;
  const select = document.querySelector('[data-action="bulk-select-all"]');
  if (select)
    select.textContent =
      boxes.length && selected === boxes.length ? "取消全选" : "全选";
  for (const button of document.querySelectorAll(
    '[data-action="bulk-execute"], [data-action="bulk-verify"]',
  ))
    button.disabled = bulkCheckedIDs.size === 0;
}

/**
 * 对当前筛选可见行执行全选或取消，不把其他版本加入选择。
 * @param {boolean} checked 选中为 true，取消为 false。
 * @returns {void} 仅更新已显示行的界面勾选状态。
 */
function selectVisibleBulkRows(checked) {
  for (const box of document.querySelectorAll("[data-bulk-check]")) {
    box.checked = checked;
    checked
      ? bulkCheckedIDs.add(box.dataset.bulkCheck)
      : bulkCheckedIDs.delete(box.dataset.bulkCheck);
  }
  updateBulkChecks();
}

/**
 * 展示所有勾选 SQL 的后端排序、目标库和原文预览。
 * @returns {Promise<void>} 只读预览成功后显示确认勾选，不执行脚本。
 */
async function showBulkSQLPreview() {
  const environment = selectedSQLEnvironment;
  toast("正在核对所选 SQL 的执行顺序和目标库…");
  bulkPreview = await api("db/batch/plan", {
    version: currentVersion,
    ids: [...bulkCheckedIDs],
    environment,
  });
  // 目标发现期间切换环境后丢弃旧预览，不弹出与当前视图不符的确认。
  if (environment !== selectedSQLEnvironment) {
    bulkPreview = null;
    return;
  }
  const targets = bulkPreview.items.reduce(
    (sum, item) => sum + item.plan.databases.length,
    0,
  );
  const body = bulkPreview.items
    .map(
      (item, index) =>
        `<details class="bulk-preview-item"><summary><span>${index + 1}. ${escapeHTML(item.name)}</span>${badge(item.phase)}<span>${escapeHTML(item.plan.side)} · ${item.plan.databases.length} 个目标库</span></summary><div class="source-note">服务器：${escapeHTML(item.plan.server || "—")}</div><div class="bulk-targets">${escapeHTML(item.plan.databases.join("、") || "没有待执行库")}</div>${item.plan.skipped.length ? `<div class="source-note">跳过：${escapeHTML(item.plan.skipped.join("、"))}</div>` : ""}<pre class="code bulk-sql-code">${highlight(item.plan.sql, "sql")}</pre></details>`,
    )
    .join("");
  showModal(
    `确认批量执行${sqlEnvironmentLabel(bulkPreview.environment)} SQL`,
    `${bulkPreview.items.length} 条脚本 · ${targets} 次逐库执行 · 建表 → 改表 → 初始化数据`,
    `<div class="notice">本次执行环境：${escapeHTML(sqlEnvironmentLabel(bulkPreview.environment))}。结构 SQL 执行前检查已存在对象，执行后核对真实字段与索引。历史成功库只复核结构，不重复执行数据语句。单库失败继续；停止或失败不代表 DDL 已回滚。</div>${body}<label class="checkbox-row"><input type="checkbox" id="bulk-sql-confirm">我已核对全部脚本、执行顺序与目标库，确认执行。</label>`,
    `${button("close-modal", "取消")}${button("bulk-confirm-sql", "开始批量执行", "play", "primary", "disabled")}`,
    true,
  );
}

/**
 * 展示勾选变更的统一生产日志查询窗口。
 * @returns {void} 仅显示条件，确认后才会调用已登录 Codex。
 */
function showBulkVerify() {
  bulkVerifyIDs = [...bulkCheckedIDs];
  const items = versionData().items.filter((item) =>
    bulkVerifyIDs.includes(item.id),
  );
  // 允许范围来自后端统一策略，默认值来自持久化设置，页面不另写并发常量。
  const concurrencyOptions = Array.from(
    { length: state.verification_concurrency },
    (_, index) => String(index + 1),
  );
  const selectedConcurrency = String(state.verification_default_concurrency);
  showModal(
    "批量验证上线结果",
    `已勾选 ${items.length} 项变更，最多 ${state.verification_concurrency} 项并发，独立保存结论`,
    `<div class="notice info">每项先集合版本代码与生产日志，再由 AI 自主判断目标是否达到。参考条件可调整，不按固定日志词判定。最多同时验证 ${state.verification_concurrency} 项，可在下方调小并发数，会使用 Codex 额度；一项失败或证据不足不影响其他项。停止会取消全部运行项，保留已完成结果。</div><div class="form-grid">${formField("并发数（启动后记住选择）", "bulk-verify-concurrency", selectedConcurrency, { select: concurrencyOptions })}${formField("开始时间（北京时间）", "bulk-verify-start", dateTimeLocal(new Date(Date.now() - 3600000)), { type: "datetime-local" })}${formField("结束时间（北京时间）", "bulk-verify-end", dateTimeLocal(new Date()), { type: "datetime-local" })}</div><ol class="bulk-selection-list">${items.map((item) => `<li>${escapeHTML(itemTitle(item))}<small>${escapeHTML(item.service || "由 AI 定位涉及服务")}</small></li>`).join("")}</ol>`,
    `${button("close-modal", "取消")}${button("bulk-confirm-verify", `开始验证 ${items.length} 项`, "spark", "primary")}`,
    true,
  );
}

/**
 * 打开批次进度或完成后的完整结果。
 * @param {string} kind sql 或 verify，决定读取哪一类队列。
 * @returns {void} 面板可收起，关闭软件仍按原规则停止任务。
 */
function showBulkProgress(kind) {
  bulkExpandedState.clear();
  bulkProgressKind = kind;
  showModal(
    kind === "sql" ? "批量 SQL 执行进度" : "批量 AI 验证进度",
    "逐项展示结果；收起面板或最小化到托盘可继续运行。",
    "",
    `${button("bulk-cancel", "停止批次", "", "danger")}${button("close-modal", "收起")}`,
    true,
    "bulk-progress",
  );
  renderBulkProgress();
}

/**
 * 回显每个脚本和每个目标库，或每个变更的独立验收结果。
 * @returns {void} 所有外部详情先转义，不在浏览器解释为 HTML。
 */
function renderBulkProgress() {
  document.querySelectorAll("[data-bulk-progress-id]").forEach((element) => {
    bulkExpandedState.set(element.dataset.bulkProgressId, element.open);
  });
  const queue =
    bulkProgressKind === "sql" ? state.sql_queue : state.verify_queue;
  const cancel = document.querySelector('[data-action="bulk-cancel"]');
  if (cancel) cancel.disabled = !queue?.running;
  if (!queue) {
    $("#modal-body").innerHTML = '<div class="empty">等待批次状态…</div>';
    return;
  }
  const done = queue.rows.filter(
    (row) => !["等待中", "执行中", "验证中"].includes(row.state),
  ).length;
  let body = `<div class="bulk-progress-summary">${queue.running ? '<span class="spinner"></span>' : icon("check")}<b>${queue.running ? "正在处理" : "批次已结束"}</b><span>已处理 ${done} / ${queue.rows.length} 项</span></div>`;
  if (bulkProgressKind === "sql")
    body =
      `<div class="source-note">执行环境：${escapeHTML(sqlEnvironmentLabel(queue.environment))}</div>` +
      body;
  if (bulkProgressKind === "verify") {
    // 并发进度按真实行状态统计，不能把最后开始的行号当作完成数量。
    const active = queue.rows.filter((row) => row.state === "验证中").length;
    const waiting = queue.rows.filter((row) => row.state === "等待中").length;
    body += `<div class="source-note">运行中 ${active} · 等待中 ${waiting} · 并发上限 ${queue.concurrency || state.verification_concurrency}${queue.cancel_requested ? " · 正在停止全部验证" : ""}</div>`;
    body += `<table class="bulk-results"><thead><tr><th>修改事项</th><th>验证状态</th><th>结果摘要 / 完整依据</th></tr></thead><tbody>${queue.rows
      .map((row) => {
        // 完整分析保持可展开与双击复制；没有结果时只显示当前进度。
        const full = row.result
          ? verificationResultText(row.result)
          : row.detail || "";
        const expanded = bulkExpandedState.get(row.item_id) || false;
        const content = row.result
          ? `<details data-bulk-progress-id="${escapeHTML(row.item_id)}" ${expanded ? "open" : ""}><summary>${escapeHTML(row.detail || "查看验证依据")}</summary><pre class="code">${escapeHTML(full)}</pre></details>`
          : `<span class="bulk-result-text">${escapeHTML(row.detail || "—")}</span>`;
        return `<tr><td>${escapeHTML(row.title)}</td><td>${badge(row.state)}</td><td data-copy="${escapeHTML(full)}">${content}</td></tr>`;
      })
      .join("")}</tbody></table>`;
  } else {
    body += queue.rows
      .map((row) => {
        const rows =
          queue.running &&
          state.batch?.item_id === row.item_id &&
          state.batch?.version === queue.version
            ? state.batch.rows
            : row.executions;
        const expanded = bulkExpandedState.has(row.item_id)
          ? bulkExpandedState.get(row.item_id)
          : row.state === "执行中";
        return `<details class="bulk-preview-item" data-bulk-progress-id="${escapeHTML(row.item_id)}" ${expanded ? "open" : ""}><summary><span>${escapeHTML(row.name)}</span>${badge(row.phase)}${badge(row.state)}<span>${escapeHTML(row.side)}</span></summary>${row.detail ? `<div class="source-note">${escapeHTML(row.detail)}</div>` : ""}${rows.length ? `<table class="bulk-results"><thead><tr><th>数据库</th><th>状态</th><th>执行详情</th></tr></thead><tbody>${rows.map((result) => `<tr><td>${escapeHTML(result.database)}</td><td>${badge(result.state)}</td><td data-copy="${escapeHTML(result.detail || "")}"><span class="bulk-result-text">${escapeHTML(result.detail || "—")}</span></td></tr>`).join("")}</tbody></table>` : '<div class="source-note">尚未执行</div>'}</details>`;
      })
      .join("");
  }
  $("#modal-body").innerHTML = body;
}

/**
 * 处理当前表格的全选、预览、启动和停止操作。
 * @param {string} action 来自本应用按钮的内部操作名。
 * @returns {Promise<boolean>} 已处理为 true；其他操作交回原界面派发器。
 */
async function handleBatchAction(action) {
  switch (action) {
    case "bulk-select-all":
      selectVisibleBulkRows(
        !document.querySelector("[data-bulk-all]")?.checked,
      );
      break;
    case "bulk-execute":
      await showBulkSQLPreview();
      break;
    case "bulk-verify":
      showBulkVerify();
      break;
    case "bulk-confirm-sql":
      if (!$("#bulk-sql-confirm").checked)
        throw new Error("请先勾选核对全部 SQL 与目标库");
      await api("db/batch/execute", {
        token: bulkPreview.token,
        confirm: true,
      });
      await refresh(true);
      showBulkProgress("sql");
      break;
    case "bulk-confirm-verify":
      await api("ai/verify-batch", {
        version: currentVersion,
        ids: bulkVerifyIDs,
        // 由后端再次校验范围并持久化偏好，界面选择不会绕过执行上限。
        concurrency: Number($("[name=bulk-verify-concurrency]").value),
        start: $("[name=bulk-verify-start]").value,
        end: $("[name=bulk-verify-end]").value,
      });
      await refresh(true);
      showBulkProgress("verify");
      break;
    case "bulk-progress":
      showBulkProgress(currentKind);
      break;
    case "bulk-cancel": {
      const queue =
        bulkProgressKind === "sql" ? state.sql_queue : state.verify_queue;
      if (queue?.running)
        await api(
          bulkProgressKind === "sql"
            ? "db/batch/cancel"
            : "ai/verify-batch/cancel",
          { id: queue.id },
        );
      toast("已请求停止，正在保存当前结果…");
      await refresh(true);
      break;
    }
    default:
      return false;
  }
  return true;
}

document.addEventListener("change", (event) => {
  const target = event.target;
  if (target.matches("[data-bulk-check]")) {
    target.checked
      ? bulkCheckedIDs.add(target.dataset.bulkCheck)
      : bulkCheckedIDs.delete(target.dataset.bulkCheck);
    updateBulkChecks();
  }
  if (target.matches("[data-bulk-all]")) selectVisibleBulkRows(target.checked);
  if (target.id === "bulk-sql-confirm")
    document.querySelector('[data-action="bulk-confirm-sql"]').disabled =
      !target.checked;
});
