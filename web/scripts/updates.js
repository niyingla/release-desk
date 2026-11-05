/**
 * 回显后台更新状态，网络故障不打断工作区操作。
 * @returns {void} 仅更新按钮和已打开的更新弹窗，不发起下载。
 */
function renderUpdateStatus() {
  // 更新服务有独立状态，不能用业务文件修订号决定是否刷新。
  // 还没有后台快照时保留现有界面，不把未查询解释为已经最新。
  // 按钮分别显示检查中、可更新和普通检查入口。
  // 检测到新版只提示可下载，不自动退出或替换正在运行的 EXE。
  // 没有打开更新弹窗时不改动其他业务窗口正文。
  // 网络错误单独显示，不能回退成没有可用更新。
  // 检查时间按用户电脑时区展示，后台仍保存统一时间格式。
  // 远端说明只作为文字，下载地址和允许操作继续由后端控制。
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
  // 检查进行中禁用重复检查，避免界面并行触发多份请求。
  // 发布说明按钮需要已经验证过的发布页地址。
  // 只有附件已经完整上传，才提供对应 Windows EXE 的下载入口。
  // 标签存在但附件尚未就绪时不伪造可下载安装状态。
  // 按钮只携带内部用途标识，不把远端 URL 放入可修改的 DOM 属性。
  // 打开发布页与打开下载资源分别处理。
  // 关闭按钮始终保留，网络检查不会阻塞用户回到离线工作台。
  // 本方法只生成操作 HTML，不执行浏览器跳转。
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
  // 软件更新独立于版本业务，不能占用 SQL 或模型任务队列。
  // 打开窗口先显示本地框架，断网时仍可关闭。
  // 主动检查调用后台端点，不在界面读取 GitHub 凭据。
  // 再次检查沿用同一个更新服务，不从用户记录读取自定义地址。
  // 发布页和下载是两种用途，由服务端选择已经验证的 URL。
  // 其他动作返回未处理，让业务派发器继续处理。
  // 后台请求提交后刷新快照，窗口展示的是服务端的实际检查状态。
  // 下载动作不修改本机版本清单或数据库连接。
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
