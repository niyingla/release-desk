"use strict";

// 页面只通过本机能力令牌访问 Go 后端；记录内容一律转义后展示。
const TOKEN = document.querySelector('meta[name="release-token"]').content;
const $ = (selector, root = document) => root.querySelector(selector);
const escapeHTML = (value) =>
  String(value ?? "").replace(
    /[&<>"']/g,
    (ch) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[
        ch
      ],
  );
const kindOrder = ["job", "config", "sql", "topic", "verify"];
const sqlPhaseOrder = ["创建表", "修改表", "初始化数据", "待识别"];
const titles = {
  job: "定时任务",
  config: "配置",
  sql: "SQL",
  topic: "Topic",
  verify: "变更与验证",
};
const icons = {
  grid: '<rect x="3" y="3" width="7" height="7" rx="1"/><rect x="14" y="3" width="7" height="7" rx="1"/><rect x="3" y="14" width="7" height="7" rx="1"/><rect x="14" y="14" width="7" height="7" rx="1"/>',
  folder: '<path d="M3 7V5a2 2 0 0 1 2-2h5l3 3h6a2 2 0 0 1 2 2v11H3z"/>',
  settings:
    '<circle cx="12" cy="12" r="3"/><path d="m9 3-1 3-3 1-2 3 2 3 1 4 3 1 3 3 3-2 4-1 1-4 2-3-2-3-3-1-1-3z"/>',
  spark:
    '<path d="m12 3 2.5 6.5L21 12l-6.5 2.5L12 21l-2.5-6.5L3 12l6.5-2.5z"/>',
  archive:
    '<rect x="3" y="4" width="18" height="5" rx="1"/><path d="M5 9v11h14V9M10 13h4"/>',
  upload: '<path d="M4 15v5h16v-5M12 16V3m-5 5 5-5 5 5"/>',
  download: '<path d="M4 15v5h16v-5M12 3v13m-5-5 5 5 5-5"/>',
  search: '<circle cx="10" cy="10" r="6"/><path d="m15 15 6 6"/>',
  clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>',
  file: '<path d="M6 3h8l4 4v14H6zM14 3v5h5M9 12h6M9 16h6"/>',
  database:
    '<ellipse cx="12" cy="5" rx="8" ry="3"/><path d="M4 5v7c0 4 16 4 16 0V5M4 12v7c0 4 16 4 16 0v-7"/>',
  check: '<path d="m5 12 4 4L19 6"/>',
  git: '<circle cx="6" cy="5" r="2"/><circle cx="6" cy="19" r="2"/><circle cx="18" cy="7" r="2"/><path d="M6 7v10M8 15c9 0 10-2 10-6"/>',
  refresh: '<path d="M20 8a8 8 0 1 0 0 8M20 3v5h-5"/>',
  plus: '<path d="M12 4v16M4 12h16"/>',
  copy: '<rect x="8" y="8" width="12" height="13" rx="2"/><path d="M16 8V3H3v13h5"/>',
  edit: '<path d="m4 16-1 5 5-1L21 7l-4-4zM14 6l4 4"/>',
  play: '<path d="m8 4 12 8-12 8z"/>',
  arrow: '<path d="M4 12h16m-6-6 6 6-6 6"/>',
  message: '<path d="M4 4h16v12H9l-5 5z"/>',
};
const icon = (name) =>
  `<svg viewBox="0 0 24 24">${icons[name] || icons.file}</svg>`;
let state = null;
let catalog = [];
let gitBranch = "";
let currentVersion = "";
let archivedOnly = false;
let currentKind = "job";
let selectedID = "";
let searchText = "";
let modalType = "";
let currentPlan = null;
let currentScripts = [];
let toastTimer;
let refreshTask = null;
let lastCopyClick = { key: "", at: 0 };
let templateInstructions = "";

/**
 * 调用经过本机能力令牌鉴权的 Go 接口。
 * @param {*} operation 后端业务端点名，仅使用软件注册的操作。
 * @param {*} payload 与端点协议一致的业务对象，不包含日志输出指令。
 * @returns {Promise<*>} 操作完成结果；失败抛出错误，由界面统一提示，不当作成功。
 */
async function api(operation, payload = {}) {
  const response = await fetch(`/api/${operation}`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-Release-Token": TOKEN },
    body: JSON.stringify(payload),
  });
  if (!response.ok) throw new Error(`请求失败（${response.status}）`);
  const result = await response.json();
  if (result.error) throw new Error(result.error);
  return result.data;
}

/**
 * 显示不包含敏感正文的短暂操作提示。
 * @param {*} message 本次操作的可读提示。
 * @param {*} error 是否使用错误提示样式，不改变业务结论。
 * @returns {*} 当前展示结果；渲染方法只更新界面，不自动执行数据库或发布。
 */
function toast(message, error = false) {
  const element = $("#toast");
  element.textContent = message;
  element.className = `toast show${error ? " error" : ""}`;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(
    () => element.classList.remove("show"),
    error ? 7000 : 2600,
  );
}

/**
 * 复制单元格完整原文到原生 Windows 剪贴板。
 * @param {*} text 完整原文，保留换行与中文，不使用表格省略后的显示值。
 * @returns {Promise<*>} 操作完成结果；失败抛出错误，由界面统一提示，不当作成功。
 */
async function copyText(text) {
  if (!text) return toast("这一项暂无内容");
  // 由 Go 写原生 Windows 剪贴板，避免 WebView 权限差异或异步用户手势失效。
  await api("clipboard/copy", { text });
  toast("已复制完整内容");
}

/**
 * 生成已转义的有限状态徽标。
 * @param {*} text 完整原文，保留换行与中文，不使用表格省略后的显示值。
 * @returns {*} 当前展示结果；渲染方法只更新界面，不自动执行数据库或发布。
 */
function badge(text) {
  const color = [
    "已上线",
    "已完成",
    "已发布",
    "成功",
    "通过",
    "通过（高概率）",
  ].includes(text)
    ? "green"
    : ["失败", "部分失败", "已中断", "结构不一致", "有失败"].includes(text)
      ? "red"
      : ["进行中", "执行中", "等待中", "运行中", "正在停止", "验证中"].includes(
            text,
          )
        ? "purple"
        : [
              "待上线",
              "待验证",
              "证据不足",
              "待核验",
              "已停止",
              "需手动验证",
              "取证受阻",
              "无适用样本",
              "版本关联不足",
              "关键证据缺失",
            ].includes(text)
          ? "amber"
          : "";
  return `<span class="badge ${color}"><span class="dot"></span>${escapeHTML(text || "待处理")}</span>`;
}

// 操作用途集中定义，标题说明实际影响，避免各入口的提示文字不一致。
const actionDescriptions = {
  "check-structure":
    "只读核对所选环境全部目标库的表、字段、索引及字段顺序；不会执行 DDL 或数据语句。",
  "scan-version":
    "使用 Codex 扫描当前版本的代码和 Git 改动，整理并写入发版记录；会使用账号额度。",
  "edit-version": "修改当前版本的发布状态、日期和备注；在此可标记为已发布。",
  export: "将当前版本的五类事项导出为 HTML 发布清单，便于查看和分享。",
  "confirm-scan":
    "确认使用 Codex 额度扫描弹窗中指定的版本，并增量写入发版记录。",
  "confirm-archive-version":
    "确认按弹窗所示归档或恢复指定版本，保留原有事项和历史。",
};

/**
 * 生成工作台内部操作按钮，并提供可悬停阅读的用途说明。
 * @param {*} action 内部操作标识，由统一事件入口分派。
 * @param {*} label 按钮、字段或卡片的可见文字。
 * @param {*} name 已登记图标名称，空字符串表示不展示图标。
 * @param {*} type 按钮样式，使用 primary、danger 等内部有限类别。
 * @param {*} extra 程序内部生成的附加按钮属性，不接受未经转义的业务正文。
 * @param {string} description 当前按钮特有的用途说明；为空时使用操作说明或按钮文字。
 * @returns {*} 当前展示结果；渲染方法只更新界面，不自动执行数据库或发布。
 */
function button(
  action,
  label,
  name = "",
  type = "",
  extra = "",
  description = "",
) {
  // 业务名称可能来自本地文件，放入 title 前仍需完整转义。
  const help = description || actionDescriptions[action] || label;
  return `<button class="button ${type}" data-action="${action}" title="${escapeHTML(help)}" ${extra}>${name ? icon(name) : ""}${label}</button>`;
}

/**
 * 为总览和详情生成相同的归档或恢复入口。
 * @param {Object} version 版本基本信息，包含 name 和独立的 archived 标记。
 * @param {string} style 按钮样式，表格内使用 text-link，详情使用默认样式。
 * @returns {string} 带明确目标状态的按钮 HTML，不采用含义不确定的状态反转。
 */
function archiveButton(version, style = "") {
  // 使用显式目标状态，重复点击同一按钮不会把归档操作变为恢复。
  const archived = Boolean(version.archived);
  return button(
    "archive-version",
    archived ? "恢复版本" : "归档版本",
    "archive",
    style,
    `data-name="${escapeHTML(version.name)}" data-archived="${!archived}"`,
    archived
      ? "将此版本恢复到版本列表，保留原有事项和执行历史。"
      : "将此版本从左侧和常规列表隐藏，内容与历史保留，可在归档版本中恢复。",
  );
}

/**
 * 归档和恢复前展示明确目标及后果，不在打开弹窗时修改版本文件。
 * @param {string} name 用户所选版本的完整名称，确认按钮绑定该版本，避免操作其他版本。
 * @param {boolean} archived true 表示归档，false 表示恢复。
 * @returns {void} 只显示确认弹窗；取消或关闭不提交归档请求。
 */
function confirmVersionArchive(name, archived) {
  // 名称和目标状态写在确认按钮上，不依赖确认时可能已变化的当前页面。
  showModal(
    archived ? "确认归档版本" : "确认恢复版本",
    name,
    `<div class="notice info">${archived ? "归档后，该版本将从左侧和常规列表隐藏。原有事项、线上标记和执行历史保留，可在“归档版本”中恢复。" : "恢复后，该版本会重新进入版本列表，原有事项和执行历史保留。"}</div>`,
    button("close-modal", "取消") +
      button(
        "confirm-archive-version",
        archived ? "确认归档" : "确认恢复",
        "archive",
        "primary",
        `data-name="${escapeHTML(name)}" data-archived="${archived}"`,
      ),
    false,
    "archive-confirm",
  );
}

/**
 * 定位当前选中的完整版本快照。
 * @returns {*} 当前展示结果；渲染方法只更新界面，不自动执行数据库或发布。
 */
function versionData() {
  return state?.versions.find((v) => v.version.name === currentVersion);
}
/**
 * 定位当前版本中选中的稳定 ID 事项。
 * @returns {*} 当前展示结果；渲染方法只更新界面，不自动执行数据库或发布。
 */
function selectedItem() {
  return versionData()?.items.find((r) => r.id === selectedID);
}
/**
 * 按业务元数据取得该类型自己的名称字段。
 * @param {*} item 当前类型的独立事项快照。
 * @returns {*} 当前展示结果；渲染方法只更新界面，不自动执行数据库或发布。
 */
function itemTitle(item) {
  return item.data[state.fields[item.kind][0][0]];
}
/**
 * 渲染一张工作台摘要卡片。
 * @param {*} label 按钮、字段或卡片的可见文字。
 * @param {*} value 该字段需要显示或编辑的完整值。
 * @param {*} note 摘要卡片的业务说明。
 * @param {*} image 已登记的本地图标名称。
 * @param {*} color 软件已定义的样式类别。
 * @returns {*} 当前展示结果；渲染方法只更新界面，不自动执行数据库或发布。
 */
function stat(label, value, note, image, color = "") {
  return `<div class="stat"><div class="stat-top">${label}<span class="stat-icon ${color}">${icon(image)}</span></div><div class="stat-value">${value}</div><div class="stat-note">${note}</div></div>`;
}

/**
 * 刷新版本导航与当前位置。
 * @returns {*} 当前展示结果；渲染方法只更新界面，不自动执行数据库或发布。
 */
function renderSidebar() {
  $("#software-version").textContent =
    `v${state.software?.current || "开发版"} · 更新记录`;
  // 已发布分组首次默认折叠；自动刷新和切换版本保留用户本次展开状态。
  const publishedOpen = $("#published-versions")?.open || false;
  const versions = state.versions.filter((v) => !v.version.archived);
  const published = versions.filter((v) => v.version.status === "已发布");
  const versionLink = (v) =>
    `<button class="version-link ${currentVersion === v.version.name ? "active" : ""}" data-action="open-version" data-name="${escapeHTML(v.version.name)}">${escapeHTML(v.version.name)}<small>${v.items.length}</small></button>`;
  $("#side-versions").innerHTML = versions.length
    ? versions
        .filter((v) => v.version.status !== "已发布")
        .map(versionLink)
        .join("") +
      (published.length
        ? `<details id="published-versions" class="published-versions" ${publishedOpen ? "open" : ""}><summary><span class="published-arrow" aria-hidden="true">›</span>已发布版本<small>${published.length}</small></summary>${published.map(versionLink).join("")}</details>`
        : "")
    : '<div class="local-note">暂无未归档版本</div>';
  // 归档只隐藏左侧版本快捷入口，仍可从独立归档列表查看和恢复。
  $('[data-action="overview"]').classList.toggle(
    "active",
    !currentVersion && !archivedOnly && !tasksVisible,
  );
  $('[data-action="archived-versions"]').classList.toggle(
    "active",
    !currentVersion && archivedOnly && !tasksVisible,
  );
  $("#breadcrumb").textContent =
    currentVersion || (archivedOnly ? "归档版本" : "版本总览");
  $('[data-action="tasks"]').classList.toggle("active", tasksVisible);
  $("#task-count").textContent =
    (state.tasks || []).filter((task) => task.running).length || "";
}

/**
 * 渲染数字版本倒序的版本总览。
 * @returns {*} 当前展示结果；渲染方法只更新界面，不自动执行数据库或发布。
 */
function renderOverview() {
  const versions = state.versions.filter(
    (v) => Boolean(v.version.archived) === archivedOnly,
  );
  const items = versions.flatMap((v) => v.items);
  const pendingOnline = items.filter(
    (r) => ["job", "config"].includes(r.kind) && r.online !== "已上线",
  ).length;
  const versionsByName = new Map(
    state.versions.map((v) => [v.version.name, v]),
  );
  const combined = [...catalog];
  for (const v of versions)
    if (!combined.some((c) => c.name === v.version.name))
      combined.push({ name: v.version.name, folder: "手动创建", count: 0 });
  combined.sort((a, b) =>
    b.name.localeCompare(a.name, undefined, { numeric: true }),
  );
  // Git 候选仍保留在普通总览，归档列表只展示用户实际归档过的记录。
  const visible = combined.filter(
    (v) =>
      Boolean(versionsByName.get(v.name)?.version.archived) === archivedOnly &&
      `${v.name} ${v.folder}`.toLowerCase().includes(searchText.toLowerCase()),
  );
  $("#content").innerHTML =
    `<div class="page-head"><div><div class="eyebrow">RELEASE WORKSPACE</div><h1>${archivedOnly ? "归档版本" : "版本总览"}</h1><div class="subtitle">${archivedOnly ? "记录与执行历史完整保留，恢复后重新显示在左侧。" : "把发布前的每一件事，变成清晰可追踪的记录。"}</div></div><div class="actions">${button("sync-git", "同步 Git", "refresh")}${button("new-version", "新建版本", "plus", "primary")}</div></div>
    <div class="stats">${stat("已记录版本", versions.length, "你的发布记录，持续积累", "grid")}${stat("待上线事项", pendingOnline, "定时任务与配置的线上发布状态", "clock", "amber")}${stat("SQL 执行记录", state.history.length, "测试 / UAT · 每个数据库独立追踪", "database", "blue")}${stat("待验证修改", items.filter((r) => r.kind === "verify" && r.verify_state !== "通过").length, "上线后，使用真实日志核对结果", "check", "green")}</div>
    <div class="panel"><div class="panel-head"><div class="panel-title">版本列表 <small>按版本号倒序</small></div><div class="tools"><span class="repo-status">${icon("git")} ${escapeHTML(gitBranch || "本地 Git")}</span><label class="search">${icon("search")}<input id="table-search" placeholder="搜索版本…" value="${escapeHTML(searchText)}"></label></div></div><div class="table-wrap"><table><thead><tr><th style="width:20%">版本</th><th style="width:29%">来源目录</th><th style="width:13%">记录数 / SQL 文件</th><th style="width:14%">计划发布日期</th><th style="width:12%">版本状态</th><th style="width:12%">操作</th></tr></thead><tbody>${visible
      .map((c) => {
        const v = versionsByName.get(c.name);
        return `<tr><td data-copy="${escapeHTML(c.name)}"><div class="version-cell"><span class="version-icon">${icon("git")}</span><div><strong>${escapeHTML(c.name)}</strong><small>${v?.version.archived ? "已归档" : v ? "已加入工作台" : "来自 SQL 仓库"}</small></div></div></td><td data-copy="${escapeHTML(c.folder)}"><span class="cell-text">${escapeHTML(c.folder)}</span></td><td>${v ? v.items.length : "—"} <span class="row-number">/ ${c.count}</span></td><td>${escapeHTML(v?.version.date || "—")}</td><td>${v ? badge(v.version.status) : '<span class="badge">尚未添加</span>'}</td><td><button class="text-link" data-action="${v ? "open-version" : "add-git-version"}" data-name="${escapeHTML(c.name)}">${v ? "查看详情 →" : "＋ 加入记录"}</button>${v ? archiveButton(v.version, "text-link") : ""}</td></tr>`;
      })
      .join(
        "",
      )}</tbody></table>${visible.length ? "" : empty(archivedOnly ? "暂无归档版本" : "还没有版本记录", archivedOnly ? "在版本详情点击归档后，可在这里查看与恢复。" : "同步 Git 获取版本目录，或手动创建一个版本。")}</div><div class="panel-foot"><span>共 ${visible.length} 个版本 · ${archivedOnly ? "点击恢复即可重新显示在左侧" : "选择需要的版本手动加入工作台"}</span><span>Git 同步保留本地未提交修改</span></div></div>
    <div class="hint-banner">${icon("spark")}<span><b>让 AI 帮你填写发布记录</b>　在版本详情点击 AI 扫描，查看进度或随时终止，扫描完成后自动更新。</span></div>`;
}

/**
 * 渲染没有记录时的空状态。
 * @param {*} title 面板或空状态标题。
 * @param {*} description 空状态的说明文字。
 * @returns {*} 当前展示结果；渲染方法只更新界面，不自动执行数据库或发布。
 */
function empty(title, description) {
  return `<div class="empty">${icon("file")}<b>${escapeHTML(title)}</b>${escapeHTML(description)}</div>`;
}

/**
 * 取得当前 SQL 内容和连接下每个库最近的结果。
 * @param {*} item 当前类型的独立事项快照。
 * @returns {*} 当前展示结果；渲染方法只更新界面，不自动执行数据库或发布。
 */
function latestExecutions(item) {
  const byDatabase = new Map();
  const connection = state.sql_connections?.[selectedSQLEnvironment];
  for (const row of state.history)
    if (
      row.version === currentVersion &&
      row.item_id === item.id &&
      row.digest === item.data.content_digest &&
      (row.environment || "test") === selectedSQLEnvironment &&
      (!connection || row.server === connection)
    )
      byDatabase.set(row.database, row);
  return [...byDatabase.values()];
}

/**
 * 从当前内容的逐库结果推导展示状态。
 * @param {*} item 当前类型的独立事项快照。
 * @returns {*} 当前展示结果；渲染方法只更新界面，不自动执行数据库或发布。
 */
function sqlStatus(item) {
  const rows = latestExecutions(item);
  if (!rows.length) return "未执行";
  if (rows.some((r) => r.state === "执行中")) return "执行中";
  if (rows.some((r) => r.state === "已中断")) return "已中断";
  if (rows.some((r) => ["失败", "结构不一致"].includes(r.state)))
    return "部分失败";
  if (rows.some((r) => r.state === "待核验")) return "待核验";
  if (rows.some((r) => ["等待中", "未执行"].includes(r.state))) return "未完成";
  // 只读核验通过不能当作已经执行过初始化数据。
  if (rows.some((r) => r.verification_only)) return "结构核验通过";
  return "成功";
}

const columns = {
  job: [
    ["handler", "Handler 名称", 22],
    ["action", "操作类型", 10],
    ["frequency", "执行频率 / Cron", 20],
    ["service", "所属服务", 17],
    ["description", "描述", 19],
  ],
  config: [
    ["filename", "配置文件", 18],
    ["prefix", "前缀", 14],
    ["content", "配置内容", 24],
    ["required", "是否必须配置", 14],
    ["description", "描述", 18],
  ],
  sql: [
    ["phase", "SQL 类型", 13],
    ["order", "顺序", 6],
    ["name", "脚本名称", 34],
    ["side", "所属端", 12],
    ["execution", "执行状态", 23],
  ],
  topic: [
    ["topic", "Topic 名称", 29],
    ["action", "变更", 11],
    ["partitions", "分区数", 10],
    ["group", "消费组", 22],
    ["description", "描述", 16],
  ],
  verify: [
    ["title", "修改事项", 19],
    ["change", "修改内容", 22],
    ["steps", "验证方式", 23],
    ["keywords", "日志条件", 15],
    ["verification", "验证状态", 9],
  ],
};

/**
 * 取得表格某字段的完整值。
 * @param {*} item 当前类型的独立事项快照。
 * @param {*} field 统一元数据中定义的字段键。
 * @returns {*} 当前展示结果；渲染方法只更新界面，不自动执行数据库或发布。
 */
function fieldValue(item, field) {
  if (field === "service" || field === "action") return item[field];
  if (field === "execution") return sqlStatus(item);
  if (field === "verification")
    return (
      verificationQueueRow(item)?.state ||
      verificationStatusLabel(
        item.verify_state ||
          (needsManualVerification(item) ? "需手动验证" : "待验证"),
        item.verify_runs?.at(-1),
      )
    );
  // 旧版本文件没有声明配置要求，不能当作已有默认值的可选配置。
  if (item.kind === "config" && field === "required")
    return item.data.required || state.config_requirements[0];
  return item.data[field] || "";
}

/**
 * 判断是否有不允许追加验证的任务，运行中的验证队列仍可接收独立事项。
 * @returns {boolean} 扫描、SQL、旧单项任务或停止中为 true；后端仍做最终校验。
 */
function verificationBusy() {
  // 放开验证队列追加，不改变扫描、SQL 和退出时的原有互斥。
  return Boolean(
    (state.ai_busy && !state.verify_queue?.running) ||
    state.scan?.running ||
    state.single_verify?.running ||
    (state.verify_queue?.running && state.verify_queue.cancel_requested) ||
    state.batch?.running ||
    state.sql_queue?.running,
  );
}

/**
 * 定位当前事项正在等待或验证的独立队列行，兼容旧版没有逐行版本的历史。
 * @param {object} item 当前表格事项；同名 ID 仍须匹配当前版本。
 * @returns {object|undefined} 正在处理的队列行；已经结束不遮盖原事项的真实结论。
 */
function verificationQueueRow(item) {
  const queue = state.verify_queue;
  if (!queue?.running) return;
  return queue.rows.find(
    (row) =>
      (row.version || queue.version) === currentVersion &&
      row.item_id === item.id &&
      ["等待中", "验证中"].includes(row.state),
  );
}

/**
 * 读取扫描或用户明确声明的人工方式，历史证据不足不自动改变分类。
 * @param {object} item 当前版本独立验证事项。
 * @returns {boolean} 关键结果需人工观察时为 true。
 */
function needsManualVerification(item) {
  return (
    item.kind === "verify" &&
    (item.data.verification_mode === "手动验证" ||
      (!item.data.verification_mode && item.verify_state === "需手动验证"))
  );
}

/**
 * 为状态悬停提供本项关键结论与证据，不把上轮结论当作本轮结果。
 * @param {object} item 当前版本的验证事项，历史按实际保存顺序追加。
 * @returns {string} 可滚动预览的纯文本；没有结果时明确说明尚待取证。
 */
function verificationStatusPreview(item) {
  const row = verificationQueueRow(item);
  const status = fieldValue(item, "verification");
  const latest = item.verify_runs?.at(-1);
  const parts = [`当前状态：${status}`];
  if (needsManualVerification(item))
    parts.push(
      `需手动验证：${item.data.manual_reason || "关键结果无法通过自动日志取证确认"}`,
      `操作方向：${item.data.steps || item.data.expected || "编辑事项补充人工验证步骤"}`,
    );
  // 正在排队或取证时先展示本轮进展，历史依据另行标识。
  if (row)
    parts.push(row.progress?.at(-1)?.summary || row.detail || "等待开始取证");
  if (latest) {
    if (row || status === "待验证")
      parts.push("历史结论（不代表当前验证已通过）：");
    parts.push(verificationResultText(latest));
  } else if (item.data.result && !row && status !== "待验证") {
    // 兼容没有结构化历史的早期记录，保持当时保存的原文。
    parts.push(item.data.result);
  } else
    parts.push(
      needsManualVerification(item)
        ? "请按人工步骤核对；本项不支持自动验证。"
        : "尚无验证结论或证据，可点击行尾“验证”开始取证。",
    );
  return parts.join("\n\n");
}

/**
 * 生成仅针对一个等待项的撤销入口，不停止同队列其他事项。
 * @param {object} row 有独立 ID 的本轮队列行；已开始的行不生成撤销按钮。
 * @returns {string} 绑定队列与行身份的按钮 HTML，旧历史或非等待项为空。
 */
function queuedVerificationCancelButton(row) {
  if (!state.verify_queue?.running || row.state !== "等待中" || !row.id)
    return "";
  return `<button class="text-link" data-action="cancel-queued-verify" data-queue-id="${escapeHTML(state.verify_queue.id)}" data-row-id="${escapeHTML(row.id)}" title="取消此项排队，不调用 Codex，不影响其他事项；保留原有验证结论。">取消</button>`;
}

/**
 * 为待验证事项及未通过的结论生成行内入口。
 * @param {object} item 当前行事项，待验证可直接启动，失败或证据不足可重试。
 * @returns {string} 绑定稳定事项 ID 的按钮；其他类型或结论返回空字符串。
 */
function verificationRetryButton(item) {
  if (item.kind !== "verify") return "";
  // 手动项只保留编辑及复制，不显示任何付费自动验证入口。
  if (needsManualVerification(item)) return "";
  // 只禁用当前正在处理的事项，其他独立事项仍可继续加入队列。
  const row = verificationQueueRow(item);
  if (row?.state === "等待中") return queuedVerificationCancelButton(row);
  if (row?.state === "验证中")
    return '<button class="text-link" disabled title="此项正在验证，其他事项仍可加入队列。">验证中</button>';
  if (
    !["待验证", "证据不足", "失败", "未发现异常"].includes(
      item.verify_state || "待验证",
    )
  )
    return "";
  // 点击后仍需确认查询时间；历史结论只追加，不因打开弹窗而清除。
  const busy = verificationBusy();
  const retry = ["证据不足", "失败", "未发现异常"].includes(item.verify_state);
  const help = busy
    ? "其他任务正在运行，完成或停止后可重试；历史验证结果保留。"
    : "重新选择日志时间范围，由 AI 结合代码与日志再次验证；保留历史结果，会使用 Codex 额度。";
  return `<button class="text-link" data-action="${retry ? "retry-verify" : "verify-item"}" data-id="${escapeHTML(item.id)}" title="${escapeHTML(help)}" ${busy ? "disabled" : ""}>${retry ? "重试" : "验证"}</button>`;
}

/**
 * 按当前类型渲染事项表并保持选中项。
 * @returns {*} 当前展示结果；渲染方法只更新界面，不自动执行数据库或发布。
 */
function renderVersion() {
  const v = versionData();
  if (!v) {
    currentVersion = "";
    return renderOverview();
  }
  const items = v.items.filter((r) => r.kind === currentKind);
  // 稳定分组保留各组原有顺序；人工项统一位于自动验证列表尾部。
  if (currentKind === "verify")
    items.sort(
      (a, b) =>
        Number(needsManualVerification(a)) - Number(needsManualVerification(b)),
    );
  syncBulkSelection(v);
  const canBulk = ["sql", "verify"].includes(currentKind);
  if (currentKind === "sql")
    items.sort(
      (a, b) =>
        sqlPhaseOrder.indexOf(a.data.phase) -
          sqlPhaseOrder.indexOf(b.data.phase) ||
        Number(a.data.order || 1) - Number(b.data.order || 1),
    );
  const visible = items.filter((r) =>
    JSON.stringify(r).toLowerCase().includes(searchText.toLowerCase()),
  );
  if (!visible.some((r) => r.id === selectedID))
    selectedID = visible[0]?.id || "";
  const done = v.items.filter((r) => r.status === "已完成").length;
  const online = v.items.filter(
    (r) => ["job", "config"].includes(r.kind) && r.online === "已上线",
  ).length;
  // 页头只承载简短业务摘要；扫描依据和提交清单保留在独立说明中，不撑大布局。
  const notes = (v.version.notes || "").trim();
  const summary =
    notes && notes.length <= 80 && !/[\r\n]/.test(notes)
      ? notes
      : "发布事项与上线验证";
  $("#content").innerHTML =
    `<div class="page-head version-header"><div class="version-intro"><div class="eyebrow">VERSION / RELEASE CHECKLIST</div><h1>${escapeHTML(currentVersion)} ${badge(v.version.status)}${v.version.archived ? badge("已归档") : ""}</h1><div class="version-meta"><span class="subtitle">${escapeHTML(summary)}</span><span class="version-date">${escapeHTML(v.version.date || "未设置发布日期")}</span>${notes ? '<button class="text-link version-notes-link" data-action="version-notes">版本说明 ↗</button>' : ""}</div></div><div class="actions">${button("scan-version", "AI 扫描", "spark")}${archiveButton(v.version)}${button("edit-version", "版本设置", "settings")}${button("export", "导出清单", "download")}</div></div>
    <div class="stats">${stat("本版变更", v.items.length, "定时任务 / 配置 / SQL / Topic / 验证", "file")}${stat("已完成事项", done, `共 ${v.items.length} 项 · ${v.items.length ? Math.round((done / v.items.length) * 100) : 0}% 完成`, "check", "green")}${stat("已标记上线", online, "定时任务与配置 · 人工核对", "upload", "blue")}${stat("待验证事项", v.items.filter((r) => r.kind === "verify" && r.verify_state !== "通过").length, "AI 分析代码与实际证据后回填结论", "spark", "amber")}</div>
    <div class="tabs">${kindOrder.map((k) => `<button class="tab ${currentKind === k ? "active" : ""}" data-action="tab" data-kind="${k}">${icon({ job: "clock", config: "file", sql: "database", topic: "message", verify: "check" }[k])}${titles[k]}<span class="tab-count">${v.items.filter((r) => r.kind === k).length}</span></button>`).join("")}</div>
    <div class="panel"><div class="panel-head"><div class="panel-title">${titles[currentKind]}<small>${currentKind === "sql" ? "创建表 → 修改表 → 初始化数据" : "双击单元格复制完整内容"}</small></div><div class="tools">${currentKind === "sql" ? sqlEnvironmentControl() : ""}<label class="search">${icon("search")}<input id="table-search" placeholder="搜索本页记录…" value="${escapeHTML(searchText)}"></label>${currentKind === "sql" ? button("refresh-sql", "刷新 SQL", "refresh") + button("import-git", "从 Git 导入", "git") : ""}${button("new-item", `新增${titles[currentKind]}`, "plus", "primary")}</div></div>${bulkControls()}
    <div class="table-wrap"><table><thead><tr>${canBulk ? '<th class="table-check-cell"><input type="checkbox" data-bulk-all aria-label="全选当前筛选结果"></th>' : ""}${columns[currentKind].map((c) => `<th style="width:${c[2]}%">${c[1]}</th>`).join("")}<th style="width:12%">${["job", "config"].includes(currentKind) ? "线上状态" : "操作"}</th></tr></thead><tbody>${visible
      .map(
        (r) =>
          `<tr data-select="${escapeHTML(r.id)}" class="${r.id === selectedID ? "selected" : ""}">${bulkRowCheckbox(r)}${columns[
            currentKind
          ]
            .map(([key]) => {
              const value = fieldValue(r, key);
              // 频率解释属于派生展示；双击仍复制纯表达式，便于粘贴到调度平台。
              const display =
                r.kind === "job" && key === "frequency"
                  ? ReleaseCron.display(value)
                  : value;
              const preview =
                key === "verification"
                  ? ` data-preview="${escapeHTML(verificationStatusPreview(r))}" data-preview-title="关键结论与证据"`
                  : key === "frequency"
                    ? ` data-preview="${escapeHTML(display)}"`
                    : "";
              return `<td data-copy="${escapeHTML(value)}"${preview}>${["execution", "verification", "action"].includes(key) ? badge(value) : `<span class="cell-text ${["handler", "frequency", "prefix", "topic", "group"].includes(key) ? "mono" : ""}">${escapeHTML(display || "—")}</span>`}</td>`;
            })
            .join(
              "",
            )}<td>${["job", "config"].includes(currentKind) ? `<button class="state-button" data-action="online" data-id="${escapeHTML(r.id)}" title="点击切换线上状态">${badge(r.online || "待上线")}</button>` : `<div class="row-actions"><button class="text-link" data-action="edit-item" data-id="${escapeHTML(r.id)}">编辑</button>${verificationRetryButton(r)}</div>`}</td></tr>`,
      )
      .join(
        "",
      )}</tbody></table>${visible.length ? "" : empty(`还没有${titles[currentKind]}记录`, "新建一项，或让 AI 按模板填写版本文件。")}</div><div class="panel-foot"><span>共 ${visible.length} 条记录</span><span>${currentKind === "sql" ? "数据成功库跳过 · 结构成功库重新核验" : "自动保存到本地版本文件"}</span></div></div><div id="detail"></div>`;
  renderDetail();
  updateBulkChecks();
}

/**
 * 对 SQL 或配置原文分词并安全高亮。
 * @param {*} code 待高亮原文，渲染前逐段转义。
 * @param {*} type sql 或配置等正文类型，选择相应高亮规则。
 * @returns {*} 当前展示结果；渲染方法只更新界面，不自动执行数据库或发布。
 */
function highlight(code, type) {
  const regex =
    type === "sql"
      ? /(--[^\n]*|\/\*[\s\S]*?\*\/|'(?:''|\\.|[^'\\])*'|\b(?:SELECT|FROM|WHERE|ALTER|TABLE|ADD|COLUMN|CREATE|INSERT|INTO|VALUES|UPDATE|SET|DELETE|DROP|INDEX|KEY|PRIMARY|NOT|NULL|DEFAULT|COMMENT|INT|VARCHAR|BIGINT|IF|EXISTS|UNIQUE|ON|AND|OR|JOIN|AS|ORDER|BY)\b)/gi
      : /(#.*$|\btrue\b|\bfalse\b|"[^"\n]*"|'[^'\n]*')/gm;
  let html = "",
    last = 0;
  // 先按原文分词再逐段转义，SQL 字符串和配置不能注入页面标签。
  for (const match of code.matchAll(regex)) {
    html += escapeHTML(code.slice(last, match.index));
    const word = match[0];
    const cls =
      word.startsWith("--") || word.startsWith("/*") || word.startsWith("#")
        ? "comment"
        : word.startsWith("'") || word.startsWith('"')
          ? "str"
          : "kw";
    html += `<span class="${cls}">${escapeHTML(word)}</span>`;
    last = match.index + word.length;
  }
  return html + escapeHTML(code.slice(last));
}

/**
 * 展示选中记录完整正文与操作。
 * @returns {*} 当前展示结果；渲染方法只更新界面，不自动执行数据库或发布。
 */
function renderDetail() {
  const r = selectedItem();
  if (!r) {
    $("#detail").innerHTML = "";
    return;
  }
  const primary =
    currentKind === "sql"
      ? "sql"
      : currentKind === "config"
        ? "content"
        : currentKind === "verify"
          ? "steps"
          : "description";
  let code = r.data[primary] || "暂无详细内容";
  if (currentKind === "verify") code = verificationItemText(r);
  const codeHTML = highlight(code, currentKind)
    .split("\n")
    .map(
      (line, i) =>
        `<span class="code-line" data-line="${i + 1}">${line || " "}</span>`,
    )
    .join("");
  const sideFields =
    currentKind === "sql"
      ? [
          ["所属端", r.data.side],
          ["执行顺序", r.data.order],
          [sqlEnvironmentLabel() + "执行", sqlStatus(r)],
          ["来源提交", r.data.source_commit?.slice(0, 12)],
          ["来源作者", r.data.source_author],
          ["回滚说明", r.data.rollback],
        ]
      : currentKind === "config"
        ? [
            ["文件名称", r.data.filename],
            ["配置前缀", r.data.prefix],
            ["是否必须配置", fieldValue(r, "required")],
            ["描述", r.data.description || "暂无描述"],
            ["线上状态", r.online],
            ["标记时间", r.online_at],
          ]
        : currentKind === "verify"
          ? [
              ["涉及服务", r.service],
              ["验证状态", r.verify_state],
              ["预期结果", r.data.expected],
              ["验证历史", `${r.verify_runs?.length || 0} 次`],
            ]
          : [
              ["涉及服务", r.service],
              [currentKind === "job" ? "操作类型" : "变更类型", r.action],
              ["待办状态", r.status],
              ["线上状态", r.online],
              ["标记时间", r.online_at],
            ];
  $("#detail").innerHTML =
    `<div class="panel detail-panel"><div class="detail-head"><div><div class="detail-name">${icon(currentKind === "sql" ? "database" : "file")}　${escapeHTML(itemTitle(r))}</div><div class="detail-meta">${escapeHTML(r.service || "未指定服务")} · ${escapeHTML(r.action)} · 记录详情</div></div><div class="actions">${currentKind === "sql" ? button("execute", "一键执行", "play", "primary") + button("retry", "重试失败库", "refresh") + button("check-structure", "仅核验结构", "check") + button("history", "执行明细", "clock") : ""}${currentKind === "verify" ? verificationDetailActions(r) : button("copy-detail", currentKind === "config" ? "复制配置" : "复制内容", "copy")}${button("edit-item", "编辑", "edit")}${button("delete-item", "删除", "", "danger small")}</div></div><div class="detail-body"><div class="code-wrap"><div class="code-toolbar"><span>${currentKind === "sql" ? "SQL PREVIEW" : currentKind === "config" ? "CONFIGURATION" : currentKind === "verify" ? "RELEASE VERIFICATION" : "DETAILS"}</span><span>UTF-8 · ${code.split("\n").length} 行</span></div><pre class="code">${codeHTML}</pre></div><dl class="detail-aside">${sideFields
      .filter(([, v]) => v)
      .map(([k, v]) => `<dt>${k}</dt><dd>${escapeHTML(v)}</dd>`)
      .join(
        "",
      )}${r.data.source_path ? `<dt>Git 文件</dt><dd class="source-note">${escapeHTML(r.data.source_path)}</dd>` : ""}</dl></div></div>`;
}

/**
 * 刷新主工作区并保留滚动位置。
 * @returns {*} 当前展示结果；渲染方法只更新界面，不自动执行数据库或发布。
 */
function render() {
  const scroll = $("#content").scrollTop;
  renderSidebar();
  tasksVisible
    ? renderTaskCenter()
    : currentVersion
      ? renderVersion()
      : renderOverview();
  $("#content").scrollTop = scroll;
  renderScanStatus();
  $("#footer-note").textContent = state.ai_busy
    ? state.ai_progress
    : state.batch?.running
      ? "测试 SQL 正在逐库执行…"
      : state.notice || "已保存 · 本地版本文件自动同步";
}

/**
 * 合并并发轮询与保存后的强制刷新。
 * @param {*} force 是否在已有请求完成后仍强制渲染，保存流程需要此保证。
 * @returns {Promise<*>} 操作完成结果；失败抛出错误，由界面统一提示，不当作成功。
 */
async function refresh(force = false) {
  // 保存动作可能与后台轮询重叠，等待正在进行的刷新后仍要执行强制渲染。
  // 不能直接跳过，否则版本已保存但界面仍停留在旧页面。
  if (refreshTask) {
    await refreshTask;
    if (force && state) render();
    return;
  }
  refreshTask = (async () => {
    const next = await api("state");
    const changed =
      !state ||
      state.revision !== next.revision ||
      state.notice !== next.notice;
    state = next;
    if (changed || force) render();
    // 即使阶段未变化，耗时也跟随轮询刷新；收起弹窗后仍能找到任务。
    renderScanStatus();
    // 更新信息拥有独立生命周期，不依赖业务记录的修订号。
    renderUpdateStatus();
    if (modalType === "progress") renderProgress();
    if (modalType === "verify-running") renderVerifyRunning();
    if (modalType === "scan-running") renderScanProgress();
    if (modalType === "bulk-progress") renderBulkProgress();
    if (modalType === "task-result") await renderTaskDetail();
  })();
  try {
    await refreshTask;
  } finally {
    refreshTask = null;
  }
}

/**
 * 打开独立的编辑或执行确认面板。
 * @param {*} title 面板或空状态标题。
 * @param {*} subtitle 面板处理边界或当前对象的说明。
 * @param {*} body 由本应用渲染函数生成的面板内容，业务字段均已转义。
 * @param {*} foot 面板底部的受控操作按钮。
 * @param {*} wide 是否使用较宽面板以阅读 SQL 和执行明细。
 * @param {*} type 面板生命周期标识，用于轮询更新进度或验证面板。
 * @returns {*} 当前展示结果；渲染方法只更新界面，不自动执行数据库或发布。
 */
function showModal(title, subtitle, body, foot = "", wide = false, type = "") {
  document.dispatchEvent(new Event("release-desk:modal"));
  modalType = type;
  $("#modal-root").innerHTML =
    `<div class="modal-overlay"><section class="modal ${wide ? "wide" : ""}"><div class="modal-head"><div><h2>${title}</h2><p>${subtitle}</p></div><button class="icon-btn" data-action="close-modal">×</button></div><div class="modal-body" id="modal-body">${body}</div><div class="modal-foot">${foot || button("close-modal", "关闭")}</div></section></div>`;
}
/**
 * 收起面板而不终止后台执行任务。
 * @returns {*} 当前展示结果；渲染方法只更新界面，不自动执行数据库或发布。
 */
function closeModal() {
  document.dispatchEvent(new Event("release-desk:modal"));
  $("#modal-root").innerHTML = "";
  modalType = "";
}
/**
 * 生成带明确字段名的编辑控件。
 * @param {*} label 按钮、字段或卡片的可见文字。
 * @param {*} name 后端字段元数据中的键名，用于保存该项业务值。
 * @param {*} value 该字段需要显示或编辑的完整值。
 * @param {*} options 控件配置，如多行、只读用途和有限选项列表。
 * @returns {*} 当前展示结果；渲染方法只更新界面，不自动执行数据库或发布。
 */
function formField(label, name, value = "", options = {}) {
  const {
    multiline = false,
    full = false,
    select = null,
    type = "text",
    tall = false,
    placeholder = "",
  } = options;
  let control = select
    ? `<select name="${name}">${select.map((x) => `<option ${x === value ? "selected" : ""}>${escapeHTML(x)}</option>`).join("")}</select>`
    : multiline
      ? `<textarea name="${name}" class="${tall ? "tall" : ""}" placeholder="${escapeHTML(placeholder)}">${escapeHTML(value)}</textarea>`
      : `<input name="${name}" type="${type}" value="${escapeHTML(value)}" placeholder="${escapeHTML(placeholder)}">`;
  return `<label class="form-field ${full ? "full" : ""}">${label}${control}</label>`;
}

/**
 * 生成支持搜索、手动输入和键盘选择的 Git 版本字段。
 * @returns {string} 新建版本专用的输入框和候选列表，不影响已有版本名称只读规则。
 */
function versionNameField() {
  // 候选列表只提供名称选择，不在选中时自动创建版本。
  return `<div class="form-field version-picker"><label for="version-name">版本名称</label><div class="version-picker-input"><input id="version-name" name="name" autocomplete="off" role="combobox" aria-autocomplete="list" aria-controls="version-options" aria-expanded="false" placeholder="搜索 Git 版本，或手动输入"><button type="button" class="version-picker-toggle" data-action="toggle-version-options" aria-label="展开 Git 版本列表">⌄</button></div><div id="version-options" class="version-options" role="listbox" aria-label="Git 版本列表" hidden></div></div>`;
}

/**
 * 根据当前搜索词渲染倒序的 Git 候选，不改变用户输入。
 * @returns {void} 仅更新当前新建表单的候选项；未打开表单时不处理。
 */
function renderVersionOptions() {
  const input = $("#version-name"),
    list = $("#version-options");
  if (!input || !list) return;
  // 不按工作区目录扫描；数据来自当前分支的 Git 版本查询接口。
  const query = input.value.trim().toLowerCase();
  const matches = [...catalog]
    .sort((a, b) => b.name.localeCompare(a.name, undefined, { numeric: true }))
    .filter((version) => version.name.toLowerCase().includes(query));
  // 使用纯名称筛选；已经记录的版本仍可选择，保存时直接打开原记录。
  list.innerHTML =
    matches
      .map(
        (version, index) =>
          `<button type="button" id="version-option-${index}" role="option" aria-selected="false" data-action="pick-version" data-name="${escapeHTML(version.name)}">${escapeHTML(version.name)}${state.versions.some((v) => v.version.name === version.name) ? "<small>已记录</small>" : ""}</button>`,
      )
      .join("") ||
    '<div class="version-options-empty">没有匹配的 Git 版本，也可以手动输入。</div>';
  list.hidden = false;
  input.setAttribute("aria-expanded", "true");
  // 搜索词变化后清除旧的键盘选中位置，避免 Enter 选到不可见候选。
  input.removeAttribute("aria-activedescendant");
}

/**
 * 收起版本候选列表，保留已经填写的版本名。
 * @returns {void} 不创建版本、不清空表单，其余编辑字段保持原样。
 */
function hideVersionOptions() {
  // 点击表单其他字段时不应继续遮挡状态和发布日期。
  const list = $("#version-options");
  if (list) list.hidden = true;
  $("#version-name")?.setAttribute("aria-expanded", "false");
}

/**
 * 处理版本搜索框的方向键、Enter 选择及 Escape 收起。
 * @param {KeyboardEvent} event 发生在新建版本搜索框中的键盘事件。
 * @returns {boolean} 已消费的候选操作返回 true，普通输入继续按默认方式处理。
 */
function handleVersionPickerKey(event) {
  const list = $("#version-options");
  if (!list) return false;
  // Escape 优先收起列表，再次按下才关闭整个新建表单。
  if (event.key === "Escape" && !list.hidden) {
    hideVersionOptions();
    return true;
  }
  // 列表重新展开会创建新节点，应先展开再取得候选引用。
  if (list.hidden && ["ArrowDown", "ArrowUp"].includes(event.key))
    renderVersionOptions();
  const options = [...list.querySelectorAll('[role="option"]')];
  const selected = options.findIndex(
    (option) => option.getAttribute("aria-selected") === "true",
  );
  // Enter 仅选择当前高亮项；没有选中时仍可保留手工版本名称。
  if (event.key === "Enter" && !list.hidden && selected >= 0) {
    options[selected].click();
    return true;
  }
  if (!["ArrowDown", "ArrowUp"].includes(event.key)) return false;
  if (!options.length) return true;
  // 焦点保留在输入框，方向键浏览后仍可直接继续输入搜索词。
  const index =
    selected < 0
      ? event.key === "ArrowDown"
        ? 0
        : options.length - 1
      : (selected + (event.key === "ArrowDown" ? 1 : -1) + options.length) %
        options.length;
  options.forEach((option, i) =>
    option.setAttribute("aria-selected", String(i === index)),
  );
  event.target.setAttribute("aria-activedescendant", options[index].id);
  options[index].scrollIntoView({ block: "nearest" });
  return true;
}

/**
 * 展示新建或编辑版本的表单。
 * @param {*} name 已有版本的精确名称，空字符串表示新建。
 * @returns {Promise<void>} 新建时刷新当前 Git 候选后展示表单；读取失败仍允许手动输入。
 */
async function showVersionForm(name = "") {
  // 每次新建重新读取当前 Git 列表，不依赖启动时缓存，也不隐式 fetch 或切分支。
  if (!name) {
    try {
      const result = await api("git/versions");
      catalog = result.versions;
      gitBranch = result.branch;
    } catch (error) {
      catalog = [];
      toast(`Git 版本读取失败，可手动输入：${error.message}`, true);
    }
  }
  const version = state.versions.find((v) => v.version.name === name)
    ?.version || { name, status: "计划中", date: "", notes: "" };
  showModal(
    name ? "版本设置" : "新建版本",
    "版本名称创建后固定；每个版本对应一个可供 AI 更新的文件。",
    `<form id="version-form" class="form-grid" data-creating="${!name}">${name ? formField("版本名称", "name", version.name) : versionNameField()}${formField("版本状态", "status", version.status, { select: ["计划中", "进行中", "已发布"] })}${formField("计划发布日期", "date", version.date, { type: "date" })}${formField("版本备注", "notes", version.notes, { multiline: true, full: true })}${!name ? '<label class="scan-option full"><input type="checkbox" name="ai_scan"><span><b>保存后让 AI 扫描此版本</b><small>复用已登录的 Codex，按设置中的作者读取代码和 Git，整理五类事项；会使用账号额度。</small></span></label>' : ""}</form>`,
    `${name ? button("delete-version", "删除版本", "", "danger") : '<span class="left">保存后自动生成版本 JSON 文件</span>'}${button("close-modal", "取消")}${button("save-version", "保存版本", "check", "primary")}`,
  );
  if (name) $("[name=name]", $("#version-form")).readOnly = true;
  else $("#version-name").focus();
}

/**
 * 展示单一业务类型自己的编辑字段。
 * @param {*} item 当前类型的独立事项快照。
 * @returns {*} 当前展示结果；渲染方法只更新界面，不自动执行数据库或发布。
 */
function showItemForm(item = null) {
  const r = item || {
    id: "",
    kind: currentKind,
    status: "待处理",
    action: "新增",
    service: "",
    data: {},
  };
  const fs = state.fields[r.kind];
  const dataFields = fs
    .map(([key, label]) => {
      if (r.kind === "job" && key === "frequency") {
        // 独占一行，窄窗口时解释自动换行，避免长表达式把输入框挤窄。
        return `<div class="form-field full cron-field"><label for="job-cron">${label}</label><div class="cron-input-row"><input id="job-cron" name="frequency" value="${escapeHTML(r.data.frequency || "")}" placeholder="0 0 10 * * ?" autocomplete="off" spellcheck="false"><span id="cron-preview" class="cron-preview" role="status" aria-live="polite"></span></div></div>`;
      }
      const multiline = [
        "sql",
        "rollback",
        "description",
        "content",
        "change",
        "steps",
        "keywords",
        "expected",
        "manual_reason",
        "result",
      ].includes(key);
      const opts = {
        multiline,
        full: multiline,
        tall: ["sql", "content"].includes(key),
        select:
          key === "verification_mode"
            ? ["自动验证", "手动验证"]
            : key === "side"
              ? ["租户端", "服务商端", "平台端"]
              : r.kind === "config" && key === "required"
                ? state.config_requirements
                : null,
      };
      return formField(
        label,
        key,
        r.data[key] ||
          (key === "verification_mode"
            ? "自动验证"
            : key === "order"
              ? "1"
              : key === "required"
                ? state.config_requirements[0]
                : ""),
        opts,
      );
    })
    .join("");
  showModal(
    item ? "编辑记录" : `新增${titles[currentKind]}`,
    "业务内容自动保存；线上状态、测试执行和验证历史独立维护。",
    `<form id="item-form" class="form-grid"><input type="hidden" name="id" value="${escapeHTML(r.id)}">${formField("涉及服务", "service", r.service)}${formField(r.kind === "job" ? "操作类型" : "变更类型", "action", r.action, { select: state.action_options[r.kind] })}${formField("待办状态", "status", r.status, { select: ["待处理", "进行中", "已完成"] })}${r.kind === "sql" ? `<div class="form-field">从文件读取${button("read-sql", "选择 .sql 文件", "folder")}</div>` : ""}${dataFields}</form>`,
    `${button("close-modal", "取消")}${button("save-item", "保存记录", "check", "primary")}`,
    r.kind === "sql" || r.kind === "verify",
    "edit-item",
  );
  $("#item-form").dataset.kind = r.kind;
  if (r.kind === "job") ReleaseCron.updatePreview($("#job-cron"));
  // 类型属于 SQL 正文的派生信息，目录名和手工标签都不能覆盖识别结果。
  const phaseField = $('#item-form [name="phase"]');
  if (phaseField) {
    phaseField.readOnly = true;
    if (!r.data.phase) phaseField.value = "保存后解析 SQL 自动识别";
  }
  // 隐藏的来源字段保留，编辑 SQL 时不会丢失 Git 提交定位信息。
  $("#item-form").originalData = r.data;
}

/**
 * 读取非敏感连接摘要并展示设置表单。
 * @returns {Promise<*>} 操作完成结果；失败抛出错误，由界面统一提示，不当作成功。
 */
async function showSettings() {
  const profiles = await Promise.all(
    state.sql_environments.map(({ id }) =>
      api("db/settings", { environment: id }),
    ),
  );
  const config = await api("settings/get");
  showModal(
    "连接与设置",
    "测试和 UAT 连接独立保存，密码使用当前 Windows 用户加密；不使用的环境可留空。",
    `<form id="settings-form">${sqlConnectionFields(profiles)}<div class="section-label">Git 与 Codex</div><div class="form-grid">${formField("SQL 仓库目录", "sql_repository", config.sql_repository, { full: true })}${formField("代码仓库目录", "code_repository", config.code_repository, { full: true })}${formField("Git 提交作者", "git_author", config.git_author)}${formField("日志技能文件", "log_skill", config.log_skill, { full: true })}${formField("允许查询的日志域名（逗号或换行分隔）", "verification_log_domains", (config.verification_log_domains || []).join("\n"), { full: true, multiline: true })}</div><div class="notice info" style="margin-top:18px;margin-bottom:0">数据库匹配规则在本地 settings.json 中集中维护。修改后重启软件生效。<br>记录目录：${escapeHTML(state.data_dir)}</div></form>`,
    `${button("files", "版本文件夹", "folder")}${button("open-data", "数据目录", "folder")}${button("close-modal", "取消")}${button("save-settings", "保存设置", "check", "primary")}`,
  );
  $("#settings-form").config = config;
}

/**
 * 展示当前作者的 SQL 提交候选供用户选择。
 * @returns {Promise<*>} 操作完成结果；失败抛出错误，由界面统一提示，不当作成功。
 */
async function showGitImport() {
  toast("正在读取当前分支中指定作者的提交…");
  currentScripts = await api("git/scripts", { name: currentVersion });
  showModal(
    "从 Git 导入 SQL",
    `版本 ${escapeHTML(currentVersion)} · 读取作者提交时的文件，保留当前工作区。`,
    `<div class="notice info">相同文件使用稳定 ID，重复导入不会新增重复记录。仅导入 SQL 文本，不自动执行。</div><table><thead><tr><th class="check-col"><input id="all-scripts" type="checkbox"></th><th>SQL 文件</th><th style="width:100px">所属端</th><th style="width:110px">提交日期</th><th style="width:100px">作者 / 提交</th></tr></thead><tbody>${currentScripts.map((s, i) => `<tr><td><input type="checkbox" class="script-check" value="${i}"></td><td data-copy="${escapeHTML(s.path)}"><span class="cell-text">${escapeHTML(s.path)}</span></td><td>${escapeHTML(s.side)}</td><td>${escapeHTML(s.date)}</td><td>${escapeHTML(s.author)}<div class="source-note">${s.commit.slice(0, 8)}</div></td></tr>`).join("")}</tbody></table>${currentScripts.length ? "" : empty("该版本没有匹配的提交", "检查版本号、当前分支和设置中的作者名称。")}`,
    `${button("close-modal", "取消")}${button("confirm-git-import", "导入已选 SQL", "download", "primary")}`,
    true,
  );
}

/**
 * 只读发现并预览选中 SQL 的完整目标。
 * @param {*} retry 是否只预览明确失败的数据库，相同内容成功库会跳过。
 * @param {boolean} verifyOnly 是否仅查询结构，允许复核历史待核验目标且不执行脚本。
 * @returns {Promise<*>} 操作完成结果；失败抛出错误，由界面统一提示，不当作成功。
 */
async function showPlan(retry = false, verifyOnly = false) {
  const r = selectedItem();
  if (!r) return;
  const environment = selectedSQLEnvironment;
  toast(`正在检查 SQL 并识别${sqlEnvironmentLabel(environment)}目标库…`);
  currentPlan = await api("db/plan", {
    version: currentVersion,
    id: r.id,
    retry,
    environment,
    verify_only: verifyOnly,
  });
  // 只读发现尚未返回时允许切换环境，迟到的预览不能覆盖新的选择。
  if (environment !== selectedSQLEnvironment) {
    currentPlan = null;
    return;
  }
  const readOnly = currentPlan.verify_only;
  const notice = readOnly
    ? "本次仅查询表、字段、索引及字段顺序。不会发送此脚本中的 DDL 或数据语句；结果独立记录为结构核验。"
    : "MySQL DDL 会隐式提交；单库失败时前序 DDL 可能已生效。失败后继续其他库，重试前请核对失败库。";
  showModal(
    `确认${sqlEnvironmentLabel(currentPlan.environment)}${readOnly ? "只读结构核验" : "执行"}`,
    `${escapeHTML(r.data.name)} · ${escapeHTML(currentPlan.side)} · ${currentPlan.databases.length} 个目标库`,
    `<div class="notice">本次执行环境：${escapeHTML(sqlEnvironmentLabel(currentPlan.environment))}。${escapeHTML(notice)}</div><div class="source-note">服务器：${escapeHTML(currentPlan.server)}</div><div class="section-label">${readOnly ? "将核验的全部目标库" : "将执行的全部目标库"}</div><div style="display:flex;gap:7px;flex-wrap:wrap">${currentPlan.databases.map((n) => `<span class="badge purple">${escapeHTML(n)}</span>`).join("")}</div>${currentPlan.skipped.length ? `<div class="section-label">已跳过 ${currentPlan.skipped.length} 个库</div><div class="source-note">${escapeHTML(currentPlan.skipped.join("、"))}</div>` : ""}<div class="section-label">SQL 内容</div><pre class="code" style="border:1px solid var(--line);border-radius:6px;max-height:200px">${highlight(currentPlan.sql, "sql")}</pre><label class="checkbox-row"><input type="checkbox" id="execute-understood">${readOnly ? "我已核对环境和目标库，确认仅查询结构。" : "我已核对目标库和 SQL，理解 DDL 不能保证整体回滚。"}</label>`,
    `${button("close-modal", "取消")}${button("confirm-execute", `${readOnly ? "核验" : "执行"} ${currentPlan.databases.length} 个${sqlEnvironmentLabel(currentPlan.environment)}库`, "play", "primary", "disabled")}`,
    true,
    "plan",
  );
}

/**
 * 打开逐库状态或历史明细面板。
 * @param {*} history 是否展示保存的历史结果而不是当前运行批次。
 * @returns {*} 当前展示结果；渲染方法只更新界面，不自动执行数据库或发布。
 */
function showProgress(history = false) {
  const readOnly =
    !history && state.batch?.rows?.some((row) => row.verification_only);
  showModal(
    `${sqlEnvironmentLabel(history ? selectedSQLEnvironment : state.batch?.environment)}${history ? "执行明细" : readOnly ? "只读结构核验" : "逐库执行"}`,
    readOnly
      ? "仅查询实际结构，不执行 DDL/DML；逐库保存核验结果。"
      : "逐库展示全部目标，单个库失败不会中断后续库。",
    "",
    button("close-modal", "收起，后台继续"),
    true,
    "progress",
  );
  $("#modal-body").dataset.history = history ? "yes" : "";
  renderProgress();
}

/**
 * 更新全部目标的状态与错误原因。
 * @returns {*} 当前展示结果；渲染方法只更新界面，不自动执行数据库或发布。
 */
function renderProgress() {
  const body = $("#modal-body");
  if (!body) return;
  const item = selectedItem();
  const useBatch =
    !body.dataset.history &&
    state.batch?.item_id === item?.id &&
    state.batch?.version === currentVersion &&
    (state.batch.environment || "test") === selectedSQLEnvironment;
  const rows = orderedDatabaseRows(
    useBatch
      ? state.batch.rows
      : state.history
          .filter(
            (r) =>
              r.version === currentVersion &&
              r.item_id === item?.id &&
              (r.environment || "test") === selectedSQLEnvironment,
          )
          .slice()
          .reverse(),
  );
  const runningTask = useBatch
    ? (state.tasks || []).find(
        (task) =>
          task.kind === "sql" &&
          task.running &&
          task.version === currentVersion,
      )
    : null;
  $(".modal-foot").innerHTML =
    `${runningTask ? button("task-stop", "停止任务", "", "danger", `data-id="${escapeHTML(runningTask.id)}" ${runningTask.status === "正在停止" ? "disabled" : ""}`) : ""}${button("tasks", "任务中心", "clock")}${button("close-modal", "收起")}`;
  const success = rows.filter((r) => r.state === "成功").length,
    failed = rows.filter((r) =>
      ["失败", "结构不一致"].includes(r.state),
    ).length,
    finished = rows.filter(
      (r) => !["执行中", "等待中"].includes(r.state),
    ).length;
  body.innerHTML = `<div class="progress-head"><span class="panel-title">${useBatch && state.batch.running ? '<span class="spinner"></span> 正在逐库处理' : "执行记录"} <small>共 ${rows.length} 条</small></span><span class="inline-status">成功 ${success}　失败 ${failed}　待完成 ${rows.length - finished}</span></div><div class="progress-track"><span style="width:${rows.length ? (finished / rows.length) * 100 : 0}%"></span></div><table><thead><tr><th style="width:18%">目标数据库</th><th style="width:12%">状态</th><th style="width:19%">开始时间</th><th>执行详情 / 错误原因</th></tr></thead><tbody>${rows.map((r) => `<tr><td data-copy="${escapeHTML(r.database)}" class="mono">${escapeHTML(r.database)}</td><td>${badge(r.state)}</td><td class="source-note">${escapeHTML(r.started || "—")}</td><td class="execution-detail" data-copy="${escapeHTML(r.detail)}">${escapeHTML(r.detail || "等待前面的库执行完成")}</td></tr>`).join("")}</tbody></table>${rows.length ? "" : empty("暂无执行记录", "点击一键执行前，软件会先列出目标数据库。")}`;
}

/**
 * 生成日志查询控件使用的时间文本。
 * @param {*} date 日志查询时间对应的 Date 对象。
 * @returns {*} 当前展示结果；渲染方法只更新界面，不自动执行数据库或发布。
 */
function dateTimeLocal(date) {
  const pad = (n) => String(n).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}
/**
 * 展示单项生产日志验收条件与时间选择。
 * @param {boolean} retry 是否从未通过记录重新验证；只调整弹窗说明，不复用旧结论。
 * @returns {*} 当前展示结果；渲染方法只更新界面，不自动执行数据库或发布。
 */
function showVerify(retry = false) {
  const r = selectedItem();
  if (!r || r.kind !== "verify") return;
  if (needsManualVerification(r))
    return toast("此项需手动验证，请查看原因和人工验证步骤", true);
  // 重试与首次验证共用后端流程，确认前不改状态、不消耗模型额度。
  if (retry && verificationBusy())
    return toast("请等待当前任务完成或停止后重试", true);
  showModal(
    retry ? "重试 AI 验证" : "AI 智能验证",
    retry
      ? "重新取证并追加验证历史，原有结论在新结果保存前保留。"
      : "先集合对应版本代码与生产日志，再由 AI 综合判断业务目标。",
    `${retry ? '<div class="notice info">本次重新分析代码与日志，保留所有历史结果。可调整下方时间范围，或取消后编辑事项以补充取证线索。</div>' : ""}<div class="notice info">AI 先核对代码改动与生产日志，再自主判断是否达到业务目的。预填条件仅供参考，可按真实证据调整；未命中关键词会尝试其他相关取证方式，查不到日志会说明原因。结果分别保留代码判断、日志发现和综合理由。会使用已登录的 Codex 额度。</div><div class="form-grid">${verificationRangeControl("verify")}${formField("开始时间（北京时间）", "verify-start", dateTimeLocal(new Date(Date.now() - 3600000)), { type: "datetime-local" })}${formField("结束时间（北京时间）", "verify-end", dateTimeLocal(new Date()), { type: "datetime-local" })}</div><div class="section-label">${escapeHTML(itemTitle(r))} · ${escapeHTML(r.service || "由 AI 定位涉及服务")}</div><pre class="code">${escapeHTML(`修改：${r.data.change || "由 AI 结合版本代码分析"}\n参考验证方式：${r.data.steps || "由 AI 自主选择"}\n参考日志线索：${r.data.keywords || "由 AI 按需定位"}\n参考预期：${r.data.expected || "由 AI 推导业务目的"}`)}</pre>`,
    // 确认按钮固定目标，后台刷新选中行后也不能验证成另一条记录。
    `${button("close-modal", "取消")}${button("start-verify", retry ? "加入重试队列" : "启动 Codex 验证", "spark", "primary", `data-version="${escapeHTML(currentVersion)}" data-id="${escapeHTML(r.id)}" data-retry="${retry}"`)}`,
  );
}
/**
 * 展示已启动 Codex 任务的等待面板。
 * @returns {*} 当前展示结果；渲染方法只更新界面，不自动执行数据库或发布。
 */
function showVerifyRunning() {
  showModal(
    "AI 智能验证",
    "点击 － 收进托盘可继续验证；点击 × 将取消任务并退出软件。",
    "",
    button("close-modal", "收起"),
    false,
    "verify-running",
  );
  renderVerifyRunning();
}

/**
 * 组装单项及批量验证的完整回看文本，兼容没有分项分析的旧结果。
 * @param {object} result 本轮结构化结果，内容可能包含多行日志，调用方展示前必须转义。
 * @returns {string} 包含结论、代码、日志、综合理由及补证方向的纯文本。
 */
function verificationResultText(result) {
  const parts = [
    result.status,
    result.confirmation ? `确认方式：${result.confirmation}` : "",
    result.blocker ? `不足原因：${result.blocker}` : "",
    result.summary,
  ];
  // 展示模型实际给出的分析，不在界面按关键词重新计算通过或失败。
  const analysis = result.analysis;
  if (analysis) {
    for (const [label, value] of [
      ["验证目标", analysis.goal],
      ["代码判断", analysis.code],
      ["日志发现", analysis.logs],
      ["综合理由", analysis.reasoning],
    ]) {
      if (value) parts.push(`${label}：${value}`);
    }
  }
  // 证据和窗口属于本轮快照，不能用现在的表单条件替换旧记录。
  if (result.environment) parts.push(`环境：${result.environment}`);
  if (result.window) parts.push(`窗口：${result.window}`);
  if (result.evidence?.length) parts.push("证据：", ...result.evidence);
  if (result.next_steps) parts.push(`后续：${result.next_steps}`);
  // 返回纯文本，由各展示入口统一 escapeHTML，避免日志被解释为页面内容。
  return parts.filter(Boolean).join("\n");
}
/**
 * 回显 Codex 的当前查询状态或最终结论。
 * @returns {*} 当前展示结果；渲染方法只更新界面，不自动执行数据库或发布。
 */
function renderVerifyRunning() {
  const r = selectedItem();
  const task = state.single_verify;
  const own = task?.version === currentVersion && task.item_id === r?.id;
  if (own) {
    const result = task.result;
    $("#modal-body").innerHTML = task.running
      ? `<div class="empty"><span class="spinner"></span><b style="margin-top:15px">${task.cancel_requested ? "正在停止验证" : "AI 正在分析代码与核验证据"}</b><p>${escapeHTML(task.condition)}</p></div>`
      : `<pre class="code">${escapeHTML(verificationResultText(result))}</pre>`;
  } else {
    $("#modal-body").innerHTML = state.ai_busy
      ? `<div class="empty"><span class="spinner"></span><b style="margin-top:15px">AI 正在分析代码与核验证据</b>${escapeHTML(state.ai_progress)}<p>验证可能需要几分钟；最小化到托盘可后台继续，关闭软件会取消任务。</p></div>`
      : `<pre class="code">${escapeHTML(r?.data.result || state.ai_progress || "等待验证结果")}</pre>`;
  }
  const runningTask = (state.tasks || []).find(
    (record) =>
      own &&
      record.id === task.id &&
      record.kind === "verify" &&
      record.running &&
      record.version === currentVersion,
  );
  $(".modal-foot").innerHTML =
    `${runningTask ? button("task-stop", "停止任务", "", "danger", `data-id="${escapeHTML(runningTask.id)}" ${runningTask.status === "正在停止" ? "disabled" : ""}`) : ""}${button("tasks", "任务中心", "clock")}${button("close-modal", "收起")}`;
}

/**
 * 扫描前解释目标、额度和写入影响；新增版本勾选扫描也复用此确认入口。
 * @returns {void} 只展示确认弹窗，不消耗 Codex 额度或修改版本事项。
 */
function confirmVersionScan() {
  // 确认按钮固定版本，避免后台刷新或页面变化导致扫描范围漂移。
  showModal(
    "确认 AI 扫描",
    "确认扫描范围与结果写入方式后开始。",
    `<div class="notice info">扫描版本：<b>${escapeHTML(currentVersion)}</b><br>使用已登录的 Codex，按设置中的仓库和作者读取代码与 Git 改动，会消耗账号额度。<br>扫描结果校验后自动备份并增量写入此版本，保留已有人工状态和执行历史。<br>任务可能需要几分钟，可在任务中心查看进度或停止。</div>`,
    button("close-modal", "取消") +
      button(
        "confirm-scan",
        "确认扫描",
        "spark",
        "primary",
        `data-name="${escapeHTML(currentVersion)}"`,
      ),
    false,
    "scan-confirm",
  );
}

/**
 * 启动已经确认的版本扫描，保存失败时仍保留已创建版本供重试。
 * @param {string} name 确认按钮绑定的精确版本名，不从当前页面重新推断。
 * @returns {Promise<void>} 后台接收成功后显示进度；错误由统一界面入口提示。
 */
async function startVersionScan(name) {
  // 版本已先落盘，模型启动失败不会撤销用户刚创建的版本。
  await api("ai/scan", { name });
  await refresh(true);
  showScanProgress();
}

/**
 * 展开已有任务，完成或终止后查看结果不会再次调用扫描接口。
 * @returns {void} 只展示状态；关闭面板不取消后台任务。
 */
function showScanProgress() {
  showModal(
    "AI 扫描版本",
    "结果校验后自动备份并增量写入；终止扫描会保留已有版本记录。",
    "",
    button("close-modal", "收起"),
    true,
    "scan-running",
  );
  renderScanProgress();
}

/**
 * 显示真实已耗时，不估算百分比或虚构完成时间。
 * @param {Object} scan 当前任务，时间由同一台本机软件生成。
 * @returns {string} 已结束任务固定显示总耗时，运行时随轮询更新。
 */
function scanElapsed(scan) {
  const start = Date.parse((scan.started || "").replace(" ", "T"));
  const end = scan.running
    ? Date.now()
    : Date.parse((scan.finished || "").replace(" ", "T"));
  if (!Number.isFinite(start) || !Number.isFinite(end)) return "计时中";
  const seconds = Math.max(0, Math.floor((end - start) / 1000));
  return `${Math.floor(seconds / 60)} 分 ${seconds % 60} 秒`;
}

/**
 * 为状态栏和弹窗使用同一任务状态，取消收尾期间保持占用。
 * @param {Object} scan 当前扫描的结构化状态，不包含模型原始输出。
 * @returns {string} 可展示的阶段名称。
 */
function scanPhase(scan) {
  if (scan.cancelled) return "已终止";
  if (scan.running)
    return scan.cancel_requested
      ? "正在终止扫描"
      : scan.phase || "正在扫描代码与 Git";
  return scan.error ? "扫描失败" : "扫描完成";
}

/**
 * 生成绑定稳定扫描 ID 的停止入口，旧按钮不能影响另一轮任务。
 * @param {Object} scan 当前扫描的结构化状态。
 * @returns {string} 运行时可停止，终止收尾时禁用，结束后不再显示。
 */
function scanCancelButton(scan) {
  if (!scan.running) return "";
  return button(
    "cancel-scan",
    scan.cancel_requested ? "正在终止…" : "终止扫描",
    "",
    "danger small",
    `data-scan-id="${escapeHTML(scan.id || "")}" ${scan.cancel_requested || !scan.id ? "disabled" : ""}`,
  );
}

/**
 * 在工作区外持续展示扫描阶段和耗时，切换版本或收起面板后仍可终止。
 * @returns {void} 不触发任务，也不输出未经转义的模型摘要。
 */
function renderScanStatus() {
  const container = $("#scan-status");
  if (!container) return;
  const scan = state?.scan;
  container.hidden = !scan;
  if (!scan) return;
  container.innerHTML = `<div class="scan-status-copy">${scan.running ? '<span class="spinner"></span>' : icon(scan.cancelled || scan.error ? "clock" : "check")}<div><b>版本 ${escapeHTML(scan.version)} · ${escapeHTML(scanPhase(scan))}</b><small>${escapeHTML(scan.author ? "仅整理 " + scan.author + " · " : "")}已耗时 ${escapeHTML(scanElapsed(scan))}${scan.cancelled ? " · 已有记录未修改" : ""}</small></div></div><div class="actions">${button("scan-progress", scan.running ? "查看进度" : "查看结果", "")}${scanCancelButton(scan)}</div>`;
}

/**
 * 展示扫描运行状态、五类候选数量和待确认信息。
 * @returns {void} 只渲染结构化状态，不启动新的 Codex 任务。
 */
function renderScanProgress() {
  const scan = state.scan;
  if (!scan) {
    $("#modal-body").textContent = "正在读取扫描状态…";
    return;
  }
  // 所有模型摘要按纯文本展示，不能成为界面脚本或新的工具指令。
  const counts = kindOrder
    .map((kind) => `${titles[kind]} ${scan.counts?.[kind] || 0}`)
    .join(" · ");
  $("#modal-body").innerHTML = scan.running
    ? `<div class="empty"><span class="spinner"></span><b>版本 ${escapeHTML(scan.version)} · ${escapeHTML(scanPhase(scan))}</b><p>${scan.cancel_requested ? "已请求终止，正在等待本次 Codex 后台进程退出，已有记录不会被覆盖。" : "读取本地代码与 Git，通常需要几分钟；可收起面板继续操作，也可单独终止本次扫描。"}</p><span>已耗时 ${escapeHTML(scanElapsed(scan))} · ${escapeHTML(scan.started)} 开始</span></div>`
    : `<div class="notice ${scan.error ? "" : "info"}">${escapeHTML(scan.cancelled ? "扫描已终止 · 已有版本记录未修改" : scan.error ? "扫描未写入：" + scan.error : "扫描已合并保存 · " + counts)} · 耗时 ${escapeHTML(scanElapsed(scan))}</div><pre class="code scan-summary">${escapeHTML(scan.summary || "未生成摘要")}</pre>${scan.warnings?.length ? `<div class="section-label">待确认信息</div><pre class="code scan-summary">${escapeHTML(scan.warnings.join("\n"))}</pre>` : ""}`;
  $("#modal-root .modal-foot").innerHTML =
    `${button("close-modal", scan.running ? "收起" : "关闭")}${scanCancelButton(scan)}`;
}

/**
 * 生成模板并提供可直接发给 Codex 的当前版本指令。
 * @returns {Promise<void>} 展示文件位置与复制入口；生成失败抛出错误。
 */
async function showTemplateGuide() {
  const result = await api("ai/template", { name: currentVersion });
  templateInstructions = result.prompt;
  showModal(
    "AI 模板与填写指令",
    "复制指令到 Codex 对话即可扫描；也可直接使用版本页的 AI 扫描。",
    `<div class="section-label">给 AI 的指令</div><pre class="code scan-summary">${escapeHTML(result.prompt)}</pre><dl class="template-paths"><dt>模板文件</dt><dd>${escapeHTML(result.template)}</dd><dt>协议说明</dt><dd>${escapeHTML(result.guide)}</dd></dl>`,
    `${button("open-templates", "打开模板目录", "folder")}${button("copy-template", "复制填写指令", "copy", "primary")}`,
    true,
  );
}

/**
 * 按业务操作派发界面事件并等待对应职责完成。
 * @param {*} action 内部操作标识，由统一事件入口分派。
 * @param {*} element 用户点击的内部操作控件，含稳定事项或版本标识。
 * @returns {Promise<*>} 操作完成结果；失败抛出错误，由界面统一提示，不当作成功。
 */
async function handleAction(action, element) {
  // 更新功能独立派发，网络检查不会进入业务任务队列。
  if (await handleUpdateAction(action)) return;
  if (await handleTaskAction(action, element)) return;
  if (
    [
      "overview",
      "archived-versions",
      "open-version",
      "new-version",
      "save-version",
    ].includes(action)
  )
    tasksVisible = false;
  if (await handleBatchAction(action)) return;
  switch (action) {
    case "software-info": {
      const info = state.software;
      if (!info) throw new Error("当前构建尚未提供软件版本记录");
      showModal(
        `Release Desk · v${info.current}`,
        "软件自身版本与工作台里的业务发布版本独立管理。",
        `<div class="notice info">${escapeHTML(info.versioning)}</div><dl class="template-paths"><dt>构建编号</dt><dd>${escapeHTML(info.build_id)}</dd><dt>构建时间</dt><dd>${escapeHTML(info.built_at || "本地开发构建")}</dd><dt>源码校验值</dt><dd>${escapeHTML(info.source_sha256 || "未归档")}</dd></dl>${info.releases.map((release) => `<details class="bulk-preview-item" ${release.version === info.current ? "open" : ""}><summary><b>v${escapeHTML(release.version)}</b><span>${escapeHTML(release.date || "历史补录")}</span><span>${escapeHTML(release.summary)}</span></summary><ul class="bulk-selection-list">${release.changes.map((text) => `<li>${escapeHTML(text)}</li>`).join("")}</ul></details>`).join("")}`,
        button("close-modal", "关闭"),
        true,
      );
      break;
    }
    case "overview":
      archivedOnly = false;
      currentVersion = "";
      selectedID = "";
      searchText = "";
      render();
      break;
    case "archived-versions":
      archivedOnly = true;
      currentVersion = "";
      selectedID = "";
      searchText = "";
      render();
      break;
    case "open-version":
      currentVersion = element.dataset.name;
      selectedID = "";
      searchText = "";
      render();
      break;
    case "tab":
      currentKind = element.dataset.kind;
      selectedID = "";
      searchText = "";
      render();
      break;
    case "new-version":
      await showVersionForm();
      break;
    case "edit-version":
      await showVersionForm(currentVersion);
      break;
    case "version-notes":
      // 原始备注完整保留，只在用户主动查看时按纯文本展示。
      showModal(
        "版本说明",
        currentVersion,
        `<div class="version-notes-content">${escapeHTML(versionData()?.version.notes || "暂无说明")}</div>`,
        button("close-modal", "关闭"),
        true,
      );
      break;
    case "archive-version":
      confirmVersionArchive(
        element.dataset.name,
        element.dataset.archived === "true",
      );
      break;
    case "confirm-archive-version": {
      // 只有当前确认弹窗中的按钮可以提交，关闭弹窗后不接受遗留动作。
      if (modalType !== "archive-confirm") return;
      const name = element.dataset.name;
      const archived = element.dataset.archived === "true";
      await api("version/archive", { name, archived });
      closeModal();
      archivedOnly = false;
      currentVersion = archived ? "" : name;
      selectedID = "";
      searchText = "";
      await refresh(true);
      toast(archived ? "版本已归档，可在归档版本中恢复" : "版本已恢复到左侧");
      break;
    }
    case "close-modal":
      closeModal();
      break;
    case "save-version": {
      const data = Object.fromEntries(new FormData($("#version-form")));
      const scanAfterSave = data.ai_scan === "on";
      delete data.ai_scan;
      // 选择已记录的版本只打开原记录，不能用新建表单的默认值覆盖原备注和状态。
      if (
        $("#version-form").dataset.creating === "true" &&
        state.versions.some((v) => v.version.name === data.name)
      ) {
        closeModal();
        currentVersion = data.name;
        await refresh(true);
        toast("已打开已有版本，原有记录保持不变");
        if (scanAfterSave) confirmVersionScan();
        break;
      }
      await api("version/save", data);
      closeModal();
      currentVersion = data.name;
      await refresh(true);
      toast("版本已保存");
      if (scanAfterSave) confirmVersionScan();
      break;
    }
    case "toggle-version-options":
      if ($("#version-options").hidden) renderVersionOptions();
      else hideVersionOptions();
      break;
    case "pick-version":
      $("#version-name").value = element.dataset.name;
      $("#version-name").focus();
      hideVersionOptions();
      break;
    case "add-git-version":
      await api("version/save", {
        name: element.dataset.name,
        status: "计划中",
        date: "",
        notes: "",
      });
      currentVersion = element.dataset.name;
      await refresh(true);
      toast("版本已加入，可以开始记录");
      break;
    case "sync-git": {
      toast("正在同步远程分支，保留本地工作区…");
      const data = await api("git/sync");
      catalog = data.versions;
      gitBranch = data.branch;
      render();
      toast("Git 已同步，版本按倒序展示");
      break;
    }
    case "new-item":
      showItemForm();
      break;
    case "edit-item":
      if (element.dataset.id) selectedID = element.dataset.id;
      showItemForm(selectedItem());
      break;
    case "save-item": {
      const form = $("#item-form"),
        data = Object.fromEntries(new FormData(form)),
        kind = form.dataset.kind;
      // 不将编辑中的错误输入转成空频率保存，留在原表单供用户修正。
      if (kind === "job") {
        const cron = ReleaseCron.describe(data.frequency);
        if (!cron.valid && !cron.empty) {
          $("#job-cron").focus();
          throw new Error(
            "Cron 无法解析；频率只填写表达式，其他说明请放到描述中",
          );
        }
      }
      const fieldsData = { ...form.originalData };
      for (const [key] of state.fields[kind]) fieldsData[key] = data[key] || "";
      if (fieldsData.phase === "自动识别") fieldsData.phase = "";
      await api("item/save", {
        version: currentVersion,
        item: {
          id: data.id,
          kind,
          service: data.service,
          action: data.action,
          status: data.status,
          data: fieldsData,
        },
      });
      closeModal();
      await refresh(true);
      toast("记录已保存");
      break;
    }
    case "delete-item": {
      const r = selectedItem();
      if (!confirm(`删除“${itemTitle(r)}”？删除前会自动备份，执行审计保留。`))
        return;
      await api("item/delete", { version: currentVersion, id: r.id });
      selectedID = "";
      await refresh(true);
      toast("记录已删除");
      break;
    }
    case "delete-version":
      if (!confirm(`删除版本 ${currentVersion} 及其记录？删除前会自动备份。`))
        return;
      await api("version/delete", { name: currentVersion });
      currentVersion = "";
      closeModal();
      await refresh(true);
      break;
    case "online":
      await api("item/online", {
        version: currentVersion,
        id: element.dataset.id,
      });
      await refresh(true);
      toast("线上状态已更新");
      break;
    case "copy-detail": {
      const r = selectedItem();
      await copyText(
        r.data[
          currentKind === "config"
            ? "content"
            : currentKind === "sql"
              ? "sql"
              : "description"
        ] || itemTitle(r),
      );
      break;
    }
    case "copy-verification":
      await copyText(verificationItemText(selectedItem()));
      break;
    case "copy-prompt": {
      const now = new Date();
      const prompt = await api("ai/prompt", {
        version: currentVersion,
        id: selectedID,
        start: dateTimeLocal(new Date(now - 3600000)),
        end: dateTimeLocal(now),
      });
      await copyText(prompt);
      break;
    }
    case "scan-version":
      // 已有任务时打开其进度，重复点击不会产生重复收费任务。
      if (state.scan?.running) {
        showScanProgress();
      } else confirmVersionScan();
      break;
    case "confirm-scan":
      if (modalType !== "scan-confirm") return;
      // 确认期间若已存在扫描，复用其进度，后端也会拒绝并发启动。
      if (state.scan?.running) showScanProgress();
      else await startVersionScan(element.dataset.name);
      break;
    case "scan-progress":
      showScanProgress();
      break;
    case "cancel-scan":
      await api("ai/scan/cancel", { id: element.dataset.scanId });
      await refresh(true);
      break;
    case "open-templates":
      await api("folder", { kind: "templates" });
      toast("已打开模板目录");
      break;
    case "copy-template":
      await copyText(templateInstructions);
      toast("填写指令已复制，可粘贴到 Codex 对话");
      break;
    case "files":
      await api("folder", { kind: "versions" });
      toast("已打开版本文件夹");
      break;
    case "open-data":
      await api("folder", { kind: "data" });
      toast("已打开数据目录");
      break;
    case "settings":
      await showSettings();
      break;
    case "save-settings": {
      const form = $("#settings-form"),
        data = Object.fromEntries(new FormData(form));
      await saveSQLConnections(data);
      await api("settings/save", {
        ...form.config,
        sql_repository: data.sql_repository,
        code_repository: data.code_repository,
        git_author: data.git_author,
        log_skill: data.log_skill,
        verification_log_domains: data.verification_log_domains
          .split(/[，,\s]+/)
          .filter(Boolean),
      });
      closeModal();
      toast("设置已保存");
      break;
    }
    case "import-git":
      await showGitImport();
      break;
    case "refresh-sql": {
      toast("正在同步 Git 并检查已关联 SQL…");
      const result = await api("git/refresh", { name: currentVersion });
      await refresh(true);
      toast(
        `更新 ${result.updated} 条，未变更 ${result.unchanged} 条；${result.conflicts.length ? `冲突需手动核对：${result.conflicts.join("、")}` : "新增文件请从 Git 导入"}`,
        result.conflicts.length > 0,
      );
      break;
    }
    case "confirm-git-import": {
      const chosen = [
        ...document.querySelectorAll(".script-check:checked"),
      ].map((input) => currentScripts[Number(input.value)]);
      if (!chosen.length) return toast("请先勾选 SQL 文件");
      const result = await api("git/import", {
        version: currentVersion,
        scripts: chosen,
      });
      closeModal();
      await refresh(true);
      toast(`已处理 ${result.selected} 个文件，新增 ${result.added} 条记录`);
      break;
    }
    case "read-sql":
      $("#sql-file").click();
      break;
    case "execute":
      await showPlan(false);
      break;
    case "retry":
      await showPlan(true);
      break;
    case "check-structure":
      await showPlan(false, true);
      break;
    case "confirm-execute":
      if (!$("#execute-understood").checked)
        return toast("请先核对并勾选执行说明");
      await api("db/execute", {
        token: currentPlan.token,
        confirm: $("#execute-understood").checked,
      });
      await refresh(true);
      showProgress();
      break;
    case "history":
      showProgress(true);
      break;
    case "verify":
      showVerify();
      break;
    case "verify-item":
    case "retry-verify":
      // 行内操作优先定位点击的事项，不能误用原先展开详情的行。
      selectedID = element.dataset.id;
      showVerify(action === "retry-verify");
      break;
    case "cancel-queued-verify":
      // 稳定行身份防止旧页面撤销同一事项后来创建的新一轮重试。
      await api("ai/verify/cancel-queued", {
        queue_id: element.dataset.queueId,
        row_id: element.dataset.rowId,
      });
      await refresh(true);
      toast("已取消此项排队，其他任务继续处理");
      break;
    case "start-verify": {
      // 提交时使用弹窗固定的版本与事项，回显进度也跟随同一目标。
      currentVersion = element.dataset.version;
      selectedID = element.dataset.id;
      const receipt = await api("ai/verify", {
        version: currentVersion,
        id: selectedID,
        start: $("[name=verify-start]").value,
        end: $("[name=verify-end]").value,
      });
      await refresh(true);
      if (receipt?.queue_id) {
        // 重试提交后回到列表，用户可直接继续点击其他项，无需逐个等待弹窗结束。
        closeModal();
        toast(
          receipt.accepted
            ? "已加入验证队列，可继续添加其他事项"
            : "此项已在队列中，不会重复执行",
        );
        if (element.dataset.retry !== "true") showBulkProgress("verify");
      } else showVerifyRunning();
      break;
    }
    case "backup": {
      const path = await api("backup");
      toast(`备份已导出：${path}`);
      break;
    }
    case "restore":
      $("#restore-file").click();
      break;
    case "export": {
      const path = await api("export", { name: currentVersion });
      toast(`清单已导出：${path}`);
      break;
    }
  }
}

document.addEventListener("click", async (event) => {
  // 勾选是批量选择，不改变用于查看详情的当前行，也不触发双击复制。
  if (event.target.closest("[data-bulk-check], [data-bulk-all]")) return;
  const control = event.target.closest("[data-action]");
  if (control) {
    if (control.disabled) return;
    control.disabled = true;
    try {
      await handleAction(control.dataset.action, control);
    } catch (error) {
      toast(error.message, true);
    } finally {
      if (control.isConnected) {
        // 停止请求回显可能已结束，不能把完成后的停止按钮重新启用。
        const queue =
          bulkProgressKind === "sql" ? state.sql_queue : state.verify_queue;
        control.disabled =
          control.dataset.action === "bulk-cancel" && !queue?.running;
      }
    }
    return;
  }
  // 后台状态刷新可能替换行节点，按业务单元格识别连续点击，避免原生 dblclick 因节点重建丢失。
  const cell = event.target.closest("[data-copy]");
  if (cell) {
    const key = `${currentVersion}|${currentKind}|${cell.closest("[data-select]")?.dataset.select || ""}|${cell.cellIndex}|${cell.dataset.copy}`;
    const now = Date.now();
    if (lastCopyClick.key === key && now - lastCopyClick.at < 500) {
      lastCopyClick = { key: "", at: 0 };
      copyText(cell.dataset.copy).catch((error) => toast(error.message, true));
    } else lastCopyClick = { key, at: now };
  }
  const row = event.target.closest("[data-select]");
  if (row) {
    selectedID = row.dataset.select;
    document
      .querySelectorAll("[data-select]")
      .forEach((tr) => tr.classList.toggle("selected", tr === row));
    renderDetail();
  }
});
document.addEventListener("dblclick", (event) => {
  if (event.target.closest("[data-copy]")) event.preventDefault();
});
document.addEventListener("input", (event) => {
  if (event.target.id === "job-cron") ReleaseCron.updatePreview(event.target);
  if (event.target.id === "version-name") renderVersionOptions();
  if (event.target.id === "table-search") {
    searchText = event.target.value;
    const position = event.target.selectionStart;
    currentVersion ? renderVersion() : renderOverview();
    $("#table-search").focus();
    $("#table-search").setSelectionRange(position, position);
  }
});
document.addEventListener("change", (event) => {
  if (applyVerificationRange(event.target)) return;
  if (event.target.id === "sql-environment") {
    // 切换视图清除旧预览；运行任务的连接由后端快照固定，不受选择框影响。
    selectedSQLEnvironment = event.target.value;
    currentPlan = null;
    bulkPreview = null;
    if (["plan", "progress"].includes(modalType)) closeModal();
    renderVersion();
  }
  if (event.target.id === "execute-understood")
    $('[data-action="confirm-execute"]').disabled = !event.target.checked;
  if (event.target.id === "all-scripts")
    document
      .querySelectorAll(".script-check")
      .forEach((input) => (input.checked = event.target.checked));
});
document.addEventListener("keydown", (event) => {
  if (event.target.id === "version-name" && handleVersionPickerKey(event)) {
    event.preventDefault();
    return;
  }
  if (event.key === "Escape") closeModal();
});

document.addEventListener("focusin", (event) => {
  if (event.target.id === "version-name") renderVersionOptions();
});
document.addEventListener("click", (event) => {
  if (!event.target.closest(".version-picker")) hideVersionOptions();
});
document.addEventListener("submit", (event) => event.preventDefault());
$("#sql-file").addEventListener("change", async (event) => {
  const file = event.target.files[0];
  if (!file) return;
  try {
    if (file.size > 5 * 1024 * 1024)
      throw new Error("SQL 文件请控制在 5 MB 内");
    const text = await file.text();
    $("[name=sql]", $("#item-form")).value = text.replace(/^\uFEFF/, "");
    const name = $("[name=name]", $("#item-form"));
    if (name && !name.value) name.value = file.name;
  } catch (error) {
    toast(error.message, true);
  }
  event.target.value = "";
});
$("#restore-file").addEventListener("change", async (event) => {
  const file = event.target.files[0];
  if (!file) return;
  try {
    if (file.size > 15 * 1024 * 1024) throw new Error("备份文件过大");
    const data = JSON.parse(await file.text());
    if (
      !confirm(
        "将恢复备份中的同名版本，其他版本保留。当前文件会先自动备份，是否继续？",
      )
    )
      return;
    await api("restore", data);
    await refresh(true);
    toast("备份已恢复");
  } catch (error) {
    toast(error.message, true);
  }
  event.target.value = "";
});

/**
 * 初始化工作台并启动轻量本机轮询。
 * @returns {Promise<*>} 操作完成结果；失败抛出错误，由界面统一提示，不当作成功。
 */
async function initialize() {
  document
    .querySelectorAll("[data-icon]")
    .forEach((element) => (element.outerHTML = icon(element.dataset.icon)));
  await refresh(true);
  try {
    const result = await api("git/versions");
    catalog = result.versions;
    gitBranch = result.branch;
    render();
  } catch (error) {
    toast(error.message, true);
  }
  setInterval(() => refresh().catch(() => {}), 1200);
}
initialize().catch((error) => toast(error.message, true));
