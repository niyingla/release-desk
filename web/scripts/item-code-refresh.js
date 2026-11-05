/**
 * 为单条变更生成更新入口，手动验证项也可重新评估代码可观察性。
 * @param {object} item 当前版本的已保存事项，稳定 ID 用于限定更新范围。
 * @returns {string} 单项按钮 HTML；其他类型为空，不启动任何后台调用。
 */
function itemCodeRefreshButton(item) {
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
  if (modalType !== "item-code-confirm") return;
  await api("ai/item/refresh", {
    version: element.dataset.name,
    id: element.dataset.id,
  });
  await refresh(true);
  showScanProgress();
}
