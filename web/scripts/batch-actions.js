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
  // 批量勾选状态属于版本与类型组合，不能跨页复用。
  // 上下文变化时清空旧 ID，避免把另一版本事项带入操作。
  // 有效集合只包括本类型、可自动处理的事项。
  // 人工验证项即使曾被选中过，也必须从自动选择集合剔除。
  // 记录被外部扫描删除或改型后，旧勾选自动失效。
  const context = `${currentVersion}|${currentKind}`;
  if (bulkSelectionContext !== context) bulkCheckedIDs.clear();
  bulkSelectionContext = context;
  const valid = new Set(
    version.items
      .filter(
        (item) => item.kind === currentKind && !needsManualVerification(item),
      )
      .map((item) => item.id),
  );
  bulkCheckedIDs = new Set([...bulkCheckedIDs].filter((id) => valid.has(id)));
}

/**
 * 渲染 SQL 或变更验证表的勾选与批量操作入口。
 * @returns {string} 只包含本表格操作，不自动启动任何后台任务。
 */
function bulkControls() {
  // 只有 SQL 和验证表提供批量启动，其他类型不借用此入口。
  // 验证队列可以包含多个版本，因此判断关联时还需查看逐行版本。
  // 已选数量来自稳定 ID 集合，不根据当前行高亮推断选择。
  // 没有勾选时禁用启动，查看已有进度仍是独立操作。
  if (!["sql", "verify"].includes(currentKind)) return "";
  const queue = currentKind === "sql" ? state.sql_queue : state.verify_queue;
  const hasQueue =
    queue &&
    (queue.version === currentVersion ||
      (currentKind === "verify" &&
        queue.rows.some((row) => row.version === currentVersion)));
  return `<div class="bulk-tools"><button class="text-link" data-action="bulk-select-all">全选</button><span class="bulk-count">已选 ${bulkCheckedIDs.size} 项</span>${button(currentKind === "sql" ? "bulk-execute" : "bulk-verify", currentKind === "sql" ? "批量执行" : "批量验证", "play", "", bulkCheckedIDs.size ? "" : "disabled")}${currentKind === "verify" ? verificationConcurrencyControl() : ""}${hasQueue ? button("bulk-progress", queue.running ? "查看进度" : "批次结果", "clock") : ""}</div>`;
}

/**
 * 在验证列表直接展示并发设置，逐项入队和批量验证使用同一偏好。
 * @returns {string} 运行中显示固定上限并禁用修改；空闲时可选 1 至后端统一上限。
 */
function verificationConcurrencyControl() {
  // 运行中的队列上限属于启动快照，界面不能中途伪装成另一个并发数。
  // 空闲时显示后端持久化偏好，不在浏览器另存一份默认值。
  // 可选范围由后端策略提供，避免前后端允许上限不一致。
  // 禁用只是展示约束，提交时后端仍会再次检查并发范围。
  const running = state.verify_queue?.running || state.single_verify?.running;
  const selected = running
    ? state.verify_queue?.concurrency || state.verification_default_concurrency
    : state.verification_default_concurrency;
  const options = Array.from(
    { length: state.verification_concurrency },
    (_, index) => index + 1,
  );
  return `<label class="verification-concurrency">并发数<select id="verification-concurrency" aria-label="验证并发数" title="逐项重试与批量验证共用，保存后记住选择；运行中的队列上限固定。" ${running ? "disabled" : ""}>${options.map((value) => `<option value="${value}" ${value === selected ? "selected" : ""}>${value}</option>`).join("")}</select>${running ? "<span>本轮上限</span>" : ""}</label>`;
}

/**
 * 渲染当前行的复选框，名称只作为可访问性纯文本。
 * @param {object} item 当前表格中的单条事项。
 * @returns {string} 勾选单元格，非批量表格返回空字符串。
 */
function bulkRowCheckbox(item) {
  // 复选框绑定稳定 ID，名称只用于辅助识别，不能成为执行目标。
  if (!["sql", "verify"].includes(currentKind)) return "";
  // 人工项不提供可批量选择的数据属性，全选只遍历真正允许的自动项。
  if (needsManualVerification(item))
    return '<td class="table-check-cell"><input type="checkbox" disabled aria-label="需手动验证，不可加入自动队列" title="关键结果需人工核对，不能自动验证"></td>';
  return `<td class="table-check-cell"><input type="checkbox" data-bulk-check="${escapeHTML(item.id)}" aria-label="选择 ${escapeHTML(itemTitle(item))}" ${bulkCheckedIDs.has(item.id) ? "checked" : ""}></td>`;
}

/**
 * 同步全选的半选状态、计数和按钮可用性。
 * @returns {void} 不重建表格，连续勾选不会影响当前行详情或滚动位置。
 */
function updateBulkChecks() {
  // 只同步控件状态，不能为了勾选一次而重建整张表。
  // 可见复选框决定全选和半选显示，其他筛选隐藏项不参与半选判断。
  // 选择集合才是已选总数来源，不能把详情选中行误算入数量。
  // 空表不能显示成全选，避免用户误以为有待执行目标。
  // 部分选择使用浏览器的半选状态，与是否启动任务无关。
  // 批量按钮随集合是否为空启用，不据此判断后端是否允许执行。
  // 全选按钮文案反映当前可见行状态，不修改已有业务记录。
  // 此方法不提交请求，网络失败不会影响当前勾选操作。
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
  // 只遍历页面当前允许选择的控件，人工项没有对应的数据属性。
  // 添加和移除都以事项稳定 ID 为单位，不修改版本中的待办状态。
  // 最后统一更新计数和半选状态，避免逐行渲染造成闪烁。
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
  // 启动预览时先固定所选环境，目标发现不会随下拉框后续变化漂移。
  // 提交的是版本与已选 ID，SQL 正文和目标库由后端读取。
  // 前端不根据目录名重新排序，展示后端已经按正文确定的队列。
  // 只读发现不意味着执行授权，后面仍需勾选确认。
  // 返回期间环境变化就丢弃这份预览，不能在另一环境下沿用旧令牌。
  // 逐脚本显示目标集合，平台、服务商和租户脚本不能共用同一列表。
  // 统计的是脚本乘目标的逐库执行次数，不是不同物理库的去重数量。
  // 跳过记录仍显示，便于区分历史成功和此次真正待执行目标。
  // SQL 高亮只改变展示，不改写保存在后端的一次性计划。
  // 确认按钮首次禁用，复选框是本轮明确确认的来源。
  // 窗口关闭不消费计划，也不会启动其中一部分脚本。
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
  // 确认弹窗保存当前已选 ID 副本，后续勾选不能扩大已经审阅的范围。
  // 事项列表来自当前版本，跨版本追加由后端队列逐项管理。
  // 统一时间窗用于本次所选事项，打开面板不会立即查询日志。
  // 并发候选来自后端的有限范围，不根据浏览器机器性能自行增大。
  // 新队列使用持久化偏好，追加队列沿用本轮已经固定的上限。
  // 追加时禁用选择框，避免显示一个实际上不会采用的新并发数。
  // 业务名称在列表中转义，模型生成的标题不能改变确认控件。
  // 确认文案说明独立失败不会中断其他验证，停止则有独立收尾语义。
  // 本方法只组织条件，真正提交与任务互斥仍由启动端点完成。
  bulkVerifyIDs = [...bulkCheckedIDs];
  const items = versionData().items.filter((item) =>
    bulkVerifyIDs.includes(item.id),
  );
  // 允许范围来自后端统一策略，默认值来自持久化设置，页面不另写并发常量。
  const concurrencyOptions = Array.from(
    { length: state.verification_concurrency },
    (_, index) => String(index + 1),
  );
  const appending = state.verify_queue?.running;
  const selectedConcurrency = String(
    appending
      ? state.verify_queue.concurrency
      : state.verification_default_concurrency,
  );
  showModal(
    "批量验证上线结果",
    appending
      ? `加入当前验证队列，沿用本轮 ${selectedConcurrency} 并发，已有等待或运行项自动合并`
      : `已勾选 ${items.length} 项变更，最多 ${state.verification_concurrency} 项并发，独立保存结论`,
    `<div class="notice info">每项先集合版本代码与生产日志，再由 AI 自主判断目标是否达到。参考条件可调整，不按固定日志词判定。最多同时验证 ${state.verification_concurrency} 项，可在下方调小并发数，会使用 Codex 额度；一项失败或证据不足不影响其他项。停止会取消全部运行项，保留已完成结果。</div><div class="form-grid">${formField("并发数（启动后记住选择）", "bulk-verify-concurrency", selectedConcurrency, { select: concurrencyOptions })}${verificationRangeControl("bulk-verify")}${formField("开始时间（北京时间）", "bulk-verify-start", dateTimeLocal(new Date(Date.now() - 3600000)), { type: "datetime-local" })}${formField("结束时间（北京时间）", "bulk-verify-end", dateTimeLocal(new Date()), { type: "datetime-local" })}</div><ol class="bulk-selection-list">${items.map((item) => `<li>${escapeHTML(itemTitle(item))}<small>${escapeHTML(item.service || "由 AI 定位涉及服务")}</small></li>`).join("")}</ol>`,
    `${button("close-modal", "取消")}${button("bulk-confirm-verify", `开始验证 ${items.length} 项`, "spark", "primary")}`,
    true,
  );
  // 追加使用本轮既定上限，不能让表单显示一个不会实际生效的不同并发数。
  if (appending) $('[name="bulk-verify-concurrency"]').disabled = true;
}

/**
 * 打开批次进度或完成后的完整结果。
 * @param {string} kind sql 或 verify，决定读取哪一类队列。
 * @returns {void} 面板可收起，关闭软件仍按原规则停止任务。
 */
function showBulkProgress(kind) {
  // 每次打开进度先重置本窗口的展开缓存，不沿用另一批次的条目键。
  // 面板类型固定读取 SQL 或验证队列，不能由后续标签切换偷换数据来源。
  // 停止按钮只是入口，是否仍可停止会在渲染快照时重新判断。
  // 收起只关闭窗口，不把后台队列标记为取消。
  // 先建立面板节点再渲染，异步轮询才能安全更新现有容器。
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
  // 展开状态先从当前节点采集，再生成下一份模板。
  // 稳定行键用于保留阅读位置，不能用并发完成顺序作为身份。
  // SQL 和验证读取各自队列，停止按钮也跟随同一类型。
  // 队列尚未到达时显示等待，不凭空建立一个已结束批次。
  // 已处理数量排除等待和运行状态，不把最后启动的序号当成进度。
  // SQL 顶部必须显示固定环境，防止把 UAT 审计理解为测试结果。
  // 验证的运行数和等待数分别按真实状态统计。
  // 停止请求只显示正在收尾，未完成行不能提前改成失败。
  // 每个验证项保留独立标题与版本，跨版本追加也可辨认来源。
  // 有结构化结论时显示完整分析，只有阶段信息时展示当前摘要。
  // 结果正文只做转义和复制，不在前端重算是否通过。
  // 历史详情的展开选择与逐项取消入口相互独立。
  // 只有等待项能显示取消按钮，运行项的取消由后端策略决定。
  // 实时进展使用倒序展示副本，持久化原序不被改变。
  // SQL 当前运行脚本优先使用正在回报的逐库状态。
  // 其他脚本继续使用各自保存的执行结果，不被当前批次覆盖。
  // 库名展示副本采用自然编号排序，不能重新安排后端写入顺序。
  // 初次打开可展开正在执行脚本，后续刷新尊重用户自己的选择。
  // 尚未执行的脚本明确显示等待，不用空结果冒充成功。
  // 最终通过增量 DOM 更新保留滚动，不整块替换进度表。
  // 本方法不发送停止、执行或验证请求，轮询只是回显。
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
        const rowKey = row.id || row.item_id;
        const expanded = bulkExpandedState.get(rowKey) || false;
        const content =
          (row.result
            ? `<details data-bulk-progress-id="${escapeHTML(rowKey)}" ${expanded ? "open" : ""}><summary>${escapeHTML(row.detail || "查看验证依据")}</summary><pre class="code">${escapeHTML(full)}</pre></details>`
            : `<span class="bulk-result-text">${escapeHTML(row.detail || "—")}</span>`) +
          verificationProgressHTML(row.progress);
        return `<tr data-progress-key="${escapeHTML(rowKey)}"><td>${escapeHTML(row.version && row.version !== queue.version ? row.version + " · " + row.title : row.title)}</td><td>${badge(verificationStatusLabel(row.state, row.result))}${queuedVerificationCancelButton(row)}</td><td data-copy="${escapeHTML(full)}">${content}</td></tr>`;
      })
      .join("")}</tbody></table>`;
  } else {
    body += queue.rows
      .map((row) => {
        const rows = orderedDatabaseRows(
          queue.running &&
            state.batch?.item_id === row.item_id &&
            state.batch?.version === queue.version
            ? state.batch.rows
            : row.executions,
        );
        const expanded = bulkExpandedState.has(row.item_id)
          ? bulkExpandedState.get(row.item_id)
          : row.state === "执行中";
        return `<details class="bulk-preview-item" data-bulk-progress-id="${escapeHTML(row.item_id)}" ${expanded ? "open" : ""}><summary><span>${escapeHTML(row.name)}</span>${badge(row.phase)}${badge(row.state)}<span>${escapeHTML(row.side)}</span></summary>${row.detail ? `<div class="source-note">${escapeHTML(row.detail)}</div>` : ""}${rows.length ? `<table class="bulk-results"><thead><tr><th>数据库</th><th>状态</th><th>执行详情</th></tr></thead><tbody>${rows.map((result) => `<tr><td>${escapeHTML(result.database)}</td><td>${badge(result.state)}</td><td data-copy="${escapeHTML(result.detail || "")}"><span class="bulk-result-text">${escapeHTML(result.detail || "—")}</span></td></tr>`).join("")}</tbody></table>` : '<div class="source-note">尚未执行</div>'}</details>`;
      })
      .join("");
  }
  updateProgressView($("#modal-body"), body);
}

/**
 * 展示 AI 明确报告的核验行动和阶段发现，兼容没有实时摘要的旧任务。
 * @param {Array<object>} entries 后端过滤、脱敏并限量的时间与纯文本摘要。
 * @returns {string} 最近进展列表；所有模型文字转义后展示。
 */
function verificationProgressHTML(entries = []) {
  // 没有实时摘要的旧任务直接省略该区域，不补造验证思路。
  // 倒序只作用于数组副本，原任务历史仍保留作者写入顺序。
  // 时间与摘要共同构成稳定键，顶部新增不会覆盖正在阅读的旧条目。
  // 模型摘要只作为已脱敏的可见行动说明，仍需转义后展示。
  if (!entries.length) return "";
  // 后端历史维持原序，界面用副本倒序，最新内容始终在顶部。
  return `<section class="verification-live"><strong>核验进展 · 最新在前</strong><ol>${[
    ...entries,
  ]
    .reverse()
    .map(
      (entry) =>
        `<li data-progress-key="${escapeHTML(entry.at + "|" + entry.summary)}"><time>${escapeHTML(taskTime(entry.at))}</time><span>${escapeHTML(entry.summary)}</span></li>`,
    )
    .join("")}</ol></section>`;
}

/**
 * 处理当前表格的全选、预览、启动和停止操作。
 * @param {string} action 来自本应用按钮的内部操作名。
 * @returns {Promise<boolean>} 已处理为 true；其他操作交回原界面派发器。
 */
async function handleBatchAction(action) {
  // 此派发器只消费批量操作，其他点击必须返回未处理。
  // 全选切换只影响可见行，不能顺带启动后台任务。
  // SQL 入口先生成只读预览，验证入口先收集统一条件。
  // 预览与执行是两步，确认前不能把排好的脚本交给执行器。
  // SQL 确认必须来自当前弹窗中的勾选，不能只凭按钮可见执行。
  // 执行请求只提交一次性令牌和确认值，不接收客户端改写的 SQL。
  // 后端接收后先刷新状态，再打开对应批次进度。
  // 验证提交使用确认时保存的 ID 集合，不读取后来新增的勾选。
  // 并发数转换为数字只是协议转换，实际上限仍需后端再次校验。
  // 起止时间来自本次表单，不能在发送时偷偷扩大窗口。
  // 追加到已有验证队列时遵循当前队列的快照上限。
  // 查看进度只选择当前类型，不重新执行任何脚本或模型。
  // 停止操作先定位实际显示的队列，避免按当前标签误停另一任务。
  // 只有运行中队列才发送停止，已完成历史保持只读。
  // 停止端点根据类型区分 SQL 与验证，参数仍是本轮稳定 ID。
  // 取消请求不承诺已经回滚 DDL，也不把响应迟到当作失败重试。
  // 提示之后刷新实际状态，保留已经完成的独立结果。
  // 未知动作返回 false，外层操作入口可继续识别。
  // 已消费动作返回 true，防止同一点击被执行两次。
  // 空选择与无效环境最终仍由后端拒绝，界面禁用不是唯一约束。
  // 后台任务启动失败向统一错误提示抛出，不展示虚假的运行窗口。
  // 方法内没有循环重发，用户重新确认前不会自动重试。
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

document.addEventListener("change", async (event) => {
  const target = event.target;
  if (target.id === "verification-concurrency") {
    target.disabled = true;
    try {
      // 仅保存并发偏好，不启动模型，也不覆盖其他工作区设置。
      await api("verification/concurrency", {
        concurrency: Number(target.value),
      });
      await refresh(true);
      toast("验证并发数已保存");
    } catch (error) {
      target.value = String(state.verification_default_concurrency);
      toast(error.message, true);
    } finally {
      if (target.isConnected)
        target.disabled = Boolean(
          state.verify_queue?.running || state.single_verify?.running,
        );
    }
    return;
  }
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
