/**
 * 生成可复制的初始化调用说明，使用字段元数据完整展示模板。
 * @param {object} item 已保存的初始化事项，不包含运行时 Token。
 * @returns {string} 完整调用信息，复制本身不发起请求。
 */
function initializationText(item) {
  const fields = state.fields.init_api
    .map(([key, label]) =>
      item.data[key] ? `${label}：\n${item.data[key]}` : "",
    )
    .filter(Boolean);
  return [
    `涉及服务：${item.service || "待确认"}`,
    `待办状态：${item.status}`,
    ...(item.data.url?.trim()
      ? []
      : ["调用路径待确认：服务内路径需结合网关前缀，补全后才可发送。"]),
    ...fields,
  ].join("\n\n");
}

/**
 * 生成调用面板的请求区和折叠说明，长业务说明保留全文，不挤占输入区域。
 * @param {object} item 当前初始化事项快照；所有业务字段按纯文本转义。
 * @returns {string} 无运行时凭据的表单 HTML，实际发送仍由现有确认入口处理。
 */
function initializationRequestBody(item) {
  const required = item.data.parameter_mode === "必填参数";
  const notes = [
    [
      "执行前置条件",
      item.data.prerequisites || "请先确认服务已发布及数据前置条件。",
    ],
    [
      "结果核对方式",
      item.data.expected || "根据响应和业务结果核对，再手动修改待办状态。",
    ],
  ];
  // 参数模板仍原样编辑；折叠仅影响说明的展示，不隐藏任何必填输入。
  const parameters = required
    ? `<section class="init-parameters"><div class="init-section-head"><h3>请求参数</h3><span class="init-note-tag">JSON</span></div><p class="init-input-hint">模板中的字段均需填写；按实际接口要求核对 Query 与 Body。</p><div class="init-parameter-grid">${formField("Query 参数", "params", item.data.params || "", { multiline: true })}${formField("Body 参数", "body", item.data.body || "", { multiline: true })}</div></section>`
    : `<div class="init-no-params"><span class="init-check" aria-hidden="true">${icon("check")}</span><div><strong>无需请求参数</strong><p>本次不发送 Query 或 Body</p></div></div>`;
  return `<form id="initialization-form" class="init-request-layout" autocomplete="off">
    <div class="init-request-main">
      <section class="init-request-card"><div class="init-route-meta"><div class="init-route-method"><span class="init-method">${escapeHTML(item.data.method || "未填写")}</span><span class="init-route-side">${escapeHTML(item.data.side || "所属端待确认")}</span></div>${formField("调用环境", "environment", "test", { select: ["测试环境", "线上环境"] })}</div><div class="init-route-label">请求地址</div><pre id="initialization-url" class="init-endpoint"></pre>${parameters}</section>
      <section class="init-request-card init-auth-card"><div class="init-section-head"><label for="initialization-token">访问 Token</label><span class="init-note-tag">仅用于本次请求</span></div><div class="init-token-input"><span>Bearer</span><input id="initialization-token" name="token" type="password" autocomplete="off" placeholder="粘贴本次登录 Token" required></div><p class="init-input-hint">自动使用 Authorization: Bearer · 请求结束后清空</p></section>
    </div>
    <aside class="init-request-notes"><h3>调用说明</h3><div class="init-service">${escapeHTML(item.service || "涉及服务待确认")}</div>${notes.map(([title, text]) => `<details><summary>${title}${icon("arrow")}</summary><p>${escapeHTML(text)}</p></details>`).join("")}<div class="init-after-send"><strong>发送之后</strong>查看响应并核对业务结果，再手动标记完成。</div></aside>
  </form><div id="initialization-result" class="init-result" aria-live="polite"></div>`;
}

/**
 * 展示本次调用确认，Token 始终从空输入开始，环境默认测试。
 * @returns {Promise<void>} 只读取本地设置并打开弹窗，不调用业务接口。
 */
async function showInitializationRequest() {
  const item = selectedItem();
  if (!item || item.kind !== "init_api") return;
  // 未确认网关路径时先补全记录，不能展示一个仅有域名的虚假请求地址。
  if (!item.data.url?.trim()) {
    showItemForm(item);
    toast("调用路径待确认，请补全含服务前缀的网关调用路径");
    return;
  }
  const version = currentVersion;
  const config = await api("settings/get");
  showModal(
    "手动调用",
    escapeHTML(itemTitle(item)),
    initializationRequestBody(item),
    '<span class="init-footer-note">凭据不保存 · 每次手动发起一次</span>' +
      button("close-modal", "取消") +
      button("send-initialization", "发送请求", "play", "primary"),
    true,
    "initialization-request",
  );
  // 专用布局只作用于调用弹窗，其他编辑、SQL 与进度窗口不受影响。
  $("#modal-root .modal").classList.add("initialization-modal");
  const form = $("#initialization-form");
  form.initializationTarget = {
    version,
    id: item.id,
    digest: item.data.content_digest,
  };
  // 配置只保存非凭据路由，临时 Token 不放到全局 state 或表单元数据。
  const environment = form.elements.environment;
  environment.options[0].value = "test";
  environment.options[1].value = "production";
  environment.value = "test";
  const updateURL = () => {
    let base =
      config.initialization_base_urls?.[environment.value]?.[item.data.side] ||
      "";
    form.initializationBase = base;
    const path = item.data.url || "";
    if (base) {
      const prefix = new URL(base).pathname.replace(/\/$/, "");
      if (
        prefix &&
        (path === prefix ||
          path.startsWith(prefix + "/") ||
          path.startsWith(prefix + "?"))
      )
        base = base.slice(0, -prefix.length);
    }
    $("#initialization-url").textContent = base
      ? base.replace(/\/$/, "") + path
      : "未配置该环境的入口，请检查 settings.json";
    // 环境切换后必须重新填写 Token，避免将测试凭据误用于线上。
    form.elements.token.value = "";
  };
  environment.addEventListener("change", updateURL);
  updateURL();
}

/**
 * 发送本次明确确认的初始化请求，不将 Token 或响应写入版本文件。
 * @param {HTMLElement} buttonElement 当前发送按钮，用于防止连续点击。
 * @returns {Promise<void>} 展示有界响应，结束后清空 Token；失败不自动重试。
 */
async function sendInitializationRequest(buttonElement) {
  // 统一点击入口已负责禁用按钮；此处只校验弹窗类型。
  if (modalType !== "initialization-request") return;
  const form = $("#initialization-form");
  if (!form.reportValidity()) return;
  const input = Object.fromEntries(new FormData(form));
  buttonElement.disabled = true;
  buttonElement.textContent = "发送中…";
  form.elements.environment.disabled = true;
  try {
    const result = await api("initialization/send", {
      ...form.initializationTarget,
      ...input,
      base_url: form.initializationBase,
    });
    // 用户已关闭弹窗时不覆盖后来打开的页面，响应也不会写入持久化历史。
    if (form.isConnected && modalType === "initialization-request") {
      $("#initialization-result").innerHTML =
        `<div class="init-result-head"><h3>调用响应</h3><span>HTTP ${result.status} · ${result.elapsed_ms} ms${result.truncated ? " · 已截断" : ""}</span></div><p class="init-input-hint">HTTP 状态只代表请求结果，请继续核对实际业务是否完成。</p><pre class="init-response-body">${escapeHTML(result.body || "（响应正文为空）")}</pre>`;
    } else toast(`初始化接口已返回 HTTP ${result.status}，请核对业务结果`);
  } finally {
    // 成功、服务端错误及网络故障都会清除 Token；下一次发送必须重新填写。
    form.elements.token.value = "";
    form.elements.environment.disabled = false;
    buttonElement.disabled = false;
    buttonElement.textContent = "发送请求";
  }
}
