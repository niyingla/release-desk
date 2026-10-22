/**
 * 回显后台更新状态，网络故障不打断工作区操作。
 * @returns {void} 仅更新按钮和已打开的更新弹窗，不发起下载。
 */
function renderUpdateStatus() {
  const update = state?.software_update;
  const button = document.querySelector("#update-button");
  if (!update || !button) return;
  // 检查状态独立于业务修订号，每次轮询均回显；不将远端文本解释为 HTML。
  button.textContent = update.checking
    ? "正在检查…"
    : update.available
      ? `发现新版 ${update.latest}`
      : "检查更新";
  button.title = update.available
    ? "有新的 Windows EXE，可查看说明并下载。"
    : "从 GitHub 检查新版软件，不上传本机业务记录。";
  if (modalType !== "software-update") return;
  const message = update.checking
    ? "正在连接 GitHub…"
    : update.error ||
      (update.available
        ? "发现可用新版本"
        : update.latest
          ? "当前没有可安装的新版本"
          : update.note || "尚未检查更新");
  // 所有版本说明和错误使用统一转义，远端 Release 正文不能执行脚本。
  $("#modal-body").innerHTML =
    `<div class="notice info">${escapeHTML(message)}</div><dl class="template-paths"><dt>当前版本</dt><dd>${escapeHTML(update.current || "—")}</dd><dt>最新发布</dt><dd>${escapeHTML(update.latest || "—")}</dd><dt>检查时间</dt><dd>${escapeHTML(update.checked_at ? new Date(update.checked_at).toLocaleString() : "尚未成功检查")}</dd></dl><pre class="code scan-summary">${escapeHTML(update.note || "")}</pre><div class="source-note">通过 GitHub Releases 检查正式版；不会上传版本清单或数据库连接。下载后关闭旧版，再运行新 EXE，本机记录仍保留。</div>`;
  $("#modal-root .modal-foot").innerHTML = softwareUpdateButtons(update);
}

/**
 * 生成当前状态允许的更新操作。
 * @param {object} update 后端已校验的发布状态，不使用远端正文拼接命令或链接。
 * @returns {string} 可安全插入的内部按钮 HTML。
 */
function softwareUpdateButtons(update) {
  // 下载按钮只在完整正式版 EXE 可用时展示，尚无附件只允许看发布页。
  return (
    button(
      "check-software-update",
      update.checking ? "检查中…" : "重新检查",
      "refresh",
      "",
      update.checking ? "disabled" : "",
    ) +
    (update.release_url
      ? button("open-software-release", "发布说明", "")
      : "") +
    (update.available
      ? button(
          "download-software-update",
          "下载新版 EXE",
          "download",
          "primary",
        )
      : "") +
    button("close-modal", "关闭")
  );
}

/**
 * 派发软件更新操作，检查和下载都与 SQL、AI 任务分开。
 * @param {string} action 用户点击的内部操作标识。
 * @returns {Promise<boolean>} 已处理时为 true，其他动作交回原事件入口。
 */
async function handleUpdateAction(action) {
  if (action === "software-update") {
    // 打开面板即请求检查；即使断网也保留可关闭的本地界面。
    showModal(
      "软件更新",
      "从 GitHub 获取正式版发布信息。",
      "",
      "",
      false,
      "software-update",
    );
    await api("update/check");
  } else if (action === "check-software-update") {
    await api("update/check");
  } else if (
    ["open-software-release", "download-software-update"].includes(action)
  ) {
    // 地址在后端读取和验证，不允许 DOM 或记录里的自定义 URL 启动系统程序。
    await api("update/open", {
      kind: action === "download-software-update" ? "download" : "release",
    });
  } else return false;
  await refresh(true);
  renderUpdateStatus();
  return true;
}
