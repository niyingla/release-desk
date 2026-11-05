// 任务中心只读取已保存的摘要和结果，回看历史不会重新调用数据库或 Codex。
let tasksVisible = false;
let taskFilter = "";
let selectedTaskID = "";
let taskDetailRequest = 0;
const taskKindLabels = {
  scan: "版本扫描",
  verify: "AI 验证",
  sql: "SQL 执行",
};

/**
 * 渲染统一任务目录，保留运行中和历史任务的独立结果。
 * @returns {void} 仅渲染本机保存的数据。
 */
function renderTaskCenter() {
  // 任务目录来自本机持久化快照，切换筛选不重新执行历史任务。
  // 缺少旧任务目录时显示空态，不能据此认定以前没有 SQL 审计。
  // 筛选只影响可见类型，不删除其他类型的记录。
  // 运行数量按完整目录统计，不被当前筛选掩盖。
  // 面包屑切到任务中心，避免把任务误认为当前业务版本的内容。
  // 任务标题和进展来自外部运行结果，进入页面前必须转义。
  // 查看按钮按任务是否结束区分进度与结果，二者仍绑定同一个稳定 ID。
  // 停止按钮只在运行时出现，正在停止时禁用重复请求。
  // 这里不依据任务名称或开始时间推断是否仍在执行。
  const records = state.tasks || [];
  const rows = records.filter(
    (record) => !taskFilter || record.kind === taskFilter,
  );
  const running = records.filter((record) => record.running).length;
  $("#breadcrumb").textContent = "任务中心";
  $("#content").innerHTML =
    `<div class="page-head"><div><div class="eyebrow">TASK CENTER</div><h1>任务中心</h1><p class="subtitle">${running ? `${running} 个任务正在处理` : "当前没有运行中的任务"} · 历史结果保存在本机，重启后可继续查看</p></div></div><div class="tabs">${[
      ["", "全部任务"],
      ["scan", "版本扫描"],
      ["verify", "AI 验证"],
      ["sql", "SQL 执行"],
    ]
      .map(
        ([kind, label]) =>
          `<button class="tab ${taskFilter === kind ? "active" : ""}" data-action="task-filter" data-kind="${kind}">${label}</button>`,
      )
      .join(
        "",
      )}</div><div class="panel task-list">${rows.length ? `<table><thead><tr><th>任务</th><th>所属版本</th><th>状态 / 进度</th><th>开始时间</th><th>操作</th></tr></thead><tbody>${rows.map((record) => `<tr><td><b>${escapeHTML(record.title)}</b><small class="source-note task-kind">${taskKindLabels[record.kind] || escapeHTML(record.kind)}</small></td><td>${escapeHTML(record.version)}</td><td>${badge(record.status)}<small class="source-note task-kind">${escapeHTML(record.progress)}</small></td><td>${escapeHTML(taskTime(record.started))}</td><td><div class="actions">${button("task-detail", record.running ? "查看进度" : "查看结果", "clock", "", `data-id="${escapeHTML(record.id)}"`)}${record.running ? button("task-stop", "停止", "", "danger", `data-id="${escapeHTML(record.id)}" ${record.status === "正在停止" ? "disabled" : ""}`) : ""}</div></td></tr>`).join("")}</tbody></table>` : '<div class="empty">暂无任务记录。新版开始记录后续扫描、验证与 SQL 执行；旧的逐库审计仍可在 SQL 页查看。</div>'}</div>`;
}

/**
 * 将构建与任务的 UTC 时间按当前电脑时区展示。
 * @param {string} value 服务端 RFC3339 时间文本。
 * @returns {string} 可读时间；未知历史时间保留空白语义。
 */
function taskTime(value) {
  // 后端时间统一保存为 RFC3339，界面再转换为用户电脑时区。
  // 缺少历史时间时保留未知状态，不补造当前时间。
  return value
    ? new Date(value).toLocaleString("zh-CN", { hour12: false })
    : "—";
}

/**
 * 打开单轮任务详情，查询只绑定稳定 ID。
 * @param {string} id 已保存的历史任务 ID。
 * @returns {Promise<void>} 显示逐项与逐库结果；不激活历史任务。
 */
async function showTaskDetail(id) {
  // 选定的是一轮任务 ID，不以业务版本名查找最后一次任务代替。
  // 先打开可关闭的本地面板，再异步读取详情。
  // 初始正文为空，避免短暂显示上一个任务的结果。
  // 独立弹窗类型用于识别后续轮询和迟到响应。
  // 历史任务只展示记录，不调用原来的启动入口。
  selectedTaskID = id;
  showModal(
    "任务详情",
    "正在读取本地记录…",
    "",
    button("close-modal", "关闭"),
    true,
    "task-result",
  );
  await renderTaskDetail();
}

/**
 * 刷新任务明细并保留用户展开的结果与滚动位置。
 * @returns {Promise<void>} 只显示当前弹窗请求，迟到响应被丢弃。
 */
async function renderTaskDetail() {
  // 每次读取记录请求的序号都递增，只允许最新一次回写界面。
  // 任务 ID 在请求发出前固定，用户切换目标后旧响应必须丢弃。
  // 弹窗关闭或更换类型后，不再操作已经不属于本任务的节点。
  // 读取成功才采集当前滚动位置，避免把等待时的空白位置当成用户选择。
  // 已展开条目使用稳定键保存，不依赖列表索引重建展开状态。
  // 首次打开和后续刷新分别处理，首次可展开，后续尊重用户收起选择。
  // 最近进展从所有事项中按时间取最后一条，不使用正在运行的行号代替。
  // 进展附带来源标题，跨事项并发时仍能定位到对应业务。
  // 头部文本直接写 textContent，不把任务标题当成 HTML。
  // 正文交给增量 DOM 同步，未变化的节点继续保留身份。
  // 同步后恢复滚动位置，轮询不会把用户拉回顶部。
  // 运行和结束时间分别展示，缺失时间不自行推算。
  // 底部停止按钮随真实运行状态更新，不用按钮是否存在推断任务状态。
  // 停止入口依然绑定这一轮任务 ID，不会误停相同版本的后续任务。
  const id = selectedTaskID,
    request = ++taskDetailRequest;
  const record = await api("task/detail", { id });
  if (
    modalType !== "task-result" ||
    id !== selectedTaskID ||
    request !== taskDetailRequest
  )
    return;
  const body = $("#modal-body"),
    scroll = body.scrollTop;
  const opened = new Set(
    [...body.querySelectorAll("details[open]")].map(
      (node) => node.dataset.taskRow,
    ),
  );
  const hasRows = Boolean(body.querySelector("[data-task-row]"));
  // 顶部实时展示最近的可公开摘要，完整条件和依据仍保留在对应事项内。
  const latest = (record.rows || [])
    .flatMap((row) =>
      (row.progress || []).map((entry) => ({ ...entry, title: row.title })),
    )
    .sort((a, b) => a.at.localeCompare(b.at))
    .at(-1);
  $(".modal-head h2").textContent = record.title;
  $(".modal-head p").textContent =
    `${record.version} · ${taskKindLabels[record.kind]} · ${record.status}`;
  updateProgressView(
    body,
    `<div class="bulk-progress-summary">${record.running ? '<span class="spinner"></span>' : icon(record.status === "已完成" ? "check" : "file")}<b>${escapeHTML(record.status)}</b><span>${escapeHTML(record.progress)}</span></div>${latest ? `<div class="verification-current"><strong>${escapeHTML(latest.title)}</strong><span>${escapeHTML(latest.summary)}</span></div>` : ""}<div class="source-note">开始：${escapeHTML(taskTime(record.started))}　结束：${escapeHTML(taskTime(record.finished))}</div>${taskRows(record.rows || [], opened, hasRows)}`,
  );
  body.scrollTop = scroll;
  updateProgressView(
    $(".modal-foot"),
    `${record.running ? button("task-stop", "停止任务", "", "danger", `data-id="${escapeHTML(record.id)}" ${record.status === "正在停止" ? "disabled" : ""}`) : ""}${button("close-modal", "收起")}`,
  );
}

/**
 * 渲染纯文本状态树，SQL 子行包含全部目标库。
 * @param {Array<object>} rows 受限任务结果树，不含可执行 HTML。
 * @param {Set<string>} opened 用户已经展开的稳定行键。
 * @param {boolean} restored 是否保留用户选择，首次打开默认展开。
 * @param {string} prefix 父节点稳定路径，避免不同脚本的索引冲突。
 * @returns {string} 已转义明细 HTML。
 */
function taskRows(rows, opened, restored, prefix = "") {
  // 递归路径参与稳定键，多个脚本下的同索引子项不会互相覆盖展开状态。
  // 等待项的取消按钮必须与当前队列稳定 ID 对上，历史记录不能发起新取消。
  // 条件、详情和证据均作为文字展示，回看不改变保存的验收结论。
  return rows
    .map((row, index) => {
      const key = `${prefix}${row.id || index}`;
      // 任务详情也提供逐项取消；只对当前队列中相同稳定行 ID 生效。
      const pending = state.verify_queue?.rows.find(
        (entry) => entry.id === row.id,
      );
      return `<details class="bulk-preview-item task-result" data-task-row="${escapeHTML(key)}" ${!restored || opened.has(key) ? "open" : ""}><summary><span>${escapeHTML(row.title)}</span>${badge(row.state)}${pending ? queuedVerificationCancelButton(pending) : ""}</summary>${verificationProgressHTML(row.progress)}${row.condition ? `<div class="source-note task-text">${escapeHTML(row.condition)}</div>` : ""}${row.detail ? `<pre class="code task-text" data-copy="${escapeHTML(row.detail)}">${escapeHTML(row.detail)}</pre>` : ""}${row.evidence?.length ? `<ul class="task-evidence">${row.evidence.map((entry) => `<li>${escapeHTML(entry)}</li>`).join("")}</ul>` : ""}${taskRows(row.children || [], opened, restored, key + "/")}</details>`;
    })
    .join("");
}

/**
 * 处理目录筛选、回看和停止，不按版本名取消任务。
 * @param {string} action 本应用内部按钮操作名。
 * @param {HTMLElement} element 提供稳定任务 ID 的点击控件。
 * @returns {Promise<boolean>} 是否已经消费此操作。
 */
async function handleTaskAction(action, element) {
  // 任务导航与业务版本导航使用不同的可见状态。
  // 进入目录先收起旧弹窗，避免后台结果覆盖任务中心。
  // 清空当前版本和选中行，只改变页面位置，不删除业务内容。
  // 筛选以任务类型为准，不通过标题猜测任务归属。
  // 筛选后重新渲染目录，不调用任务执行端点。
  // 查看详情只传稳定 ID，仍由后端检查记录是否存在。
  // 停止请求同样绑定稳定 ID，不按版本批量停止不相关任务。
  // 停止提示说明请求已经发出，不冒充所有外部操作都已经中止。
  // 刷新后若详情窗口仍打开，再回显本轮的实际收尾状态。
  // 未知动作返回未处理，交回其他业务派发器。
  // 已处理动作统一返回成功消费标记，避免同一点击继续落入另一套逻辑。
  switch (action) {
    case "tasks":
      closeModal();
      tasksVisible = true;
      currentVersion = "";
      selectedID = "";
      render();
      break;
    case "task-filter":
      taskFilter = element.dataset.kind;
      renderTaskCenter();
      break;
    case "task-detail":
      await showTaskDetail(element.dataset.id);
      break;
    case "task-stop":
      await api("task/cancel", { id: element.dataset.id });
      toast("已请求停止，正在保存已有结果…");
      await refresh(true);
      if (modalType === "task-result") await renderTaskDetail();
      break;
    default:
      return false;
  }
  return true;
}
