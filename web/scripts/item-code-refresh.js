/**
 * 为单条变更生成更新入口，手动验证项也可重新评估代码可观察性。
 * @param {object} item 当前版本的已保存事项，稳定 ID 用于限定更新范围。
 * @returns {string} 单项按钮 HTML；其他类型为空，不启动任何后台调用。
 */
function itemCodeRefreshButton(item) {
  // 更新代码只属于变更验证事项，其他清单不提供此操作。
  // 是否正在更新必须同时匹配版本和稳定 ID，不能只比较标题。
  // SQL 与扫描可独立运行，但两个 AI 任务仍遵循原来的互斥规则。
  // 按钮状态只是界面提示，后端还会重新检查任务占用和来源。
  if (item.kind !== "verify") return "";
  const updating =
    state.scan?.running &&
    state.scan.version === currentVersion &&
    state.scan.item_id === item.id;
  const help = state.ai_busy
    ? "请等待当前扫描或 AI 验证结束；SQL 可独立继续执行。"
    : "只读本地 Git 引用，更新此事项的代码说明、来源与验证条件；保留历史，不自动验证。";
  return `<button class="text-link" data-action="refresh-item-code" data-id="${escapeHTML(item.id)}" title="${escapeHTML(help)}" ${state.ai_busy ? "disabled" : ""}>${updating ? "更新中" : "更新代码"}</button>`;
}

/**
 * 更新前展示固定目标及本地只读范围，取消不会消耗额度或修改引用。
 * @param {string} id 用户点击的事项 ID，不使用当前选中行推断目标。
 * @returns {void} 仅展示确认弹窗；实际任务由明确的确认操作触发。
 */
function confirmItemCodeRefresh(id) {
  // 按点击行的稳定 ID 定位，不依赖详情区原先展开的事项。
  // 记录已删除或类型变化时直接提示，不创建空白扫描。
  // 确认窗口先展示目标与影响，打开窗口本身不调用模型。
  // 版本与 ID 写入确认按钮，提交时不会追随新的行选择。
  // 提示明确只读本地引用，不暗中同步远程代码。
  // 旧验证历史仍保留，条件更新可能使旧结论不再适用。
  // 业务标题先转义，版本文件不能借此改变确认按钮。
  // 取消只关闭窗口，不消费 Codex 额度或修改版本文件。
  // 真正候选保存仍由后端的单项范围校验决定。
  const item = versionData()?.items.find(
    (row) => row.id === id && row.kind === "verify",
  );
  if (!item) return toast("事项已变化，请刷新后重试");
  showModal(
    "更新此项代码情况",
    "仅更新当前事项，会使用已登录的 Codex 额度。",
    `<div class="notice info"><b>${escapeHTML(item.data.title)}</b><br>版本：${escapeHTML(currentVersion)}<br>只读本地 Git 引用，核对提交前后差异及后续修复，更新这一项的修改内容、代码来源和验证条件。保留历史；条件变化后旧结论不再代表当前内容。<br>本地引用由你在 Git 工具中同步；软件读取时不拉取远程、不切换分支、不覆盖工作区。</div>`,
    button("close-modal", "取消") +
      button(
        "confirm-item-code-refresh",
        "确认更新",
        "refresh",
        "primary",
        `data-name="${escapeHTML(currentVersion)}" data-id="${escapeHTML(id)}"`,
      ),
    false,
    "item-code-confirm",
  );
}

/**
 * 提交确认弹窗绑定的事项，后端独立固定 SHA 并校验候选范围。
 * @param {HTMLElement} element 用户确认的按钮，携带版本名与事项 ID 快照。
 * @returns {Promise<void>} 接收成功后打开可收起、可停止的扫描进度。
 */
async function startItemCodeRefresh(element) {
  // 只有匹配的确认窗口可以启动本轮操作，旧按钮点击不再有效。
  // 版本与事项来自确认按钮快照，不从当前高亮行重新取值。
  // 先刷新后台接收状态再展示进度，避免窗口关联到旧扫描。
  if (modalType !== "item-code-confirm") return;
  await api("ai/item/refresh", {
    version: element.dataset.name,
    id: element.dataset.id,
  });
  await refresh(true);
  showScanProgress();
}
