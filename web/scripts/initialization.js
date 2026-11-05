/**
 * 生成可复制的初始化调用说明，使用字段元数据完整展示模板。
 * @param {object} item 已保存的初始化事项，不包含运行时 Token。
 * @returns {string} 完整调用信息，复制本身不发起请求。
 */
function initializationText(item) {
  // 复制清单沿用字段元数据，避免新增业务字段只在编辑器可见、复制时却丢失。
  // 此处只读取已保存记录；运行时 Token 和响应没有进入字段映射的入口。
  const fields = state.fields.init_api
    .map(([key, label]) =>
      item.data[key] ? `${label}：\n${item.data[key]}` : "",
    )
    .filter(Boolean);
  return [
    // 人工待办状态与接口响应分开呈现，不能把上次 HTTP 成功解释为业务完成。
    `涉及服务：${item.service || "待确认"}`,
    `待办状态：${item.status}`,
    // 服务内路径即使已知，也不能在复制内容中冒充带网关前缀的可执行地址。
    ...(item.data.url?.trim()
      ? []
      : ["调用路径待确认：服务内路径需结合网关前缀，补全后才可发送。"]),
    ...fields,
    // 保留配置和参数的原始换行，便于用户单独交给 AI 核对调用条件。
    // 复制只生成文本，不复用发送函数，避免整理信息时产生业务请求。
  ].join("\n\n");
}

/**
 * 生成调用面板的请求区和折叠说明，长业务说明保留全文，不挤占输入区域。
 * @param {object} item 当前初始化事项快照；所有业务字段按纯文本转义。
 * @returns {string} 无运行时凭据的表单 HTML，实际发送仍由现有确认入口处理。
 */
function initializationRequestBody(item) {
  // 无参分类不渲染旧模板输入，避免用户误以为残留参数仍会随请求发送。
  const required = item.data.parameter_mode === "必填参数";
  // 空说明使用待核对提示，不能用默认文案暗示服务或数据变更已经完成。
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
  // Query 与 Body 沿用原协议字段名，界面调整不能改变服务端的参数完整性检查。
  // JSON 只作为原文放入输入框，不在展示阶段转换数字或补造业务默认值。
  const parameters = required
    ? `<section class="init-parameters"><div class="init-section-head"><h3>请求参数</h3><span class="init-note-tag">JSON</span></div><p class="init-input-hint">模板中的字段均需填写；按实际接口要求核对 Query 与 Body。</p><div class="init-parameter-grid">${formField("Query 参数", "params", item.data.params || "", { multiline: true })}${formField("Body 参数", "body", item.data.body || "", { multiline: true })}</div></section>`
    : `<div class="init-no-params"><span class="init-check" aria-hidden="true">${icon("check")}</span><div><strong>无需请求参数</strong><p>本次不发送 Query 或 Body</p></div></div>`;
  // 业务标题、路径和说明均转义后再进入 HTML，防止版本文件内容改变表单结构。
  // Token 输入始终没有初始值，不从模板、上次响应或浏览器状态回填凭据。
  // 说明区域使用原生折叠控件保留全文，视觉收起不会删减实际执行条件。
  // 响应位于表单外部，下一次发送时不会把上次业务响应混入 FormData。
  // 本方法只返回页面片段；环境设置读取和请求发送仍由各自入口负责。
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
  // 环境切换始终使用本次打开时的配置快照，发送前再由后端检查是否发生更新。
  // 在读取异步设置之前定位事项，后续窗口绑定这次选择，不追随后台切换版本。
  const item = selectedItem();
  if (!item || item.kind !== "init_api") return;
  // 未确认网关路径时先补全记录，不能展示一个仅有域名的虚假请求地址。
  if (!item.data.url?.trim()) {
    // 复用记录编辑入口，让补充地址经过原有保存及校验流程。
    showItemForm(item);
    toast("调用路径待确认，请补全含服务前缀的网关调用路径");
    return;
  }
  const version = currentVersion;
  // 设置只提供非凭据环境入口；本次 Token 仍由用户在打开后的空输入框填写。
  const config = await api("settings/get");
  // 页面生成和请求编排分开，避免长 HTML 淹没快照绑定及环境处理逻辑。
  showModal(
    "手动调用",
    // 版本事项名称是外部文本，作为副标题时仍需独立转义。
    escapeHTML(itemTitle(item)),
    initializationRequestBody(item),
    // 关闭仅收起窗口；是否再次发送始终由用户决定，不通过按钮布局引入自动重试。
    '<span class="init-footer-note">凭据不保存 · 每次手动发起一次</span>' +
      button("close-modal", "取消") +
      button("send-initialization", "发送请求", "play", "primary"),
    true,
    "initialization-request",
  );
  // 专用布局只作用于调用弹窗，其他编辑、SQL 与进度窗口不受影响。
  $("#modal-root .modal").classList.add("initialization-modal");
  const form = $("#initialization-form");
  // 后端用版本、稳定 ID 和内容摘要共同检查目标，防止弹窗打开后路由被扫描替换。
  form.initializationTarget = {
    version,
    id: item.id,
    digest: item.data.content_digest,
  };
  // 配置只保存非凭据路由，临时 Token 不放到全局 state 或表单元数据。
  const environment = form.elements.environment;
  // 显示名称与协议值分离，提交 test/production，不把中文选项文案当作环境标识。
  environment.options[0].value = "test";
  environment.options[1].value = "production";
  // 每次打开都从测试环境开始，不沿用上次选择的线上环境。
  environment.value = "test";
  const updateURL = () => {
    // 所属端与环境共同决定唯一入口；缺失时不给出跨环境或其他端的猜测地址。
    let base =
      config.initialization_base_urls?.[environment.value]?.[item.data.side] ||
      "";
    // 保存原始配置快照，服务端会再核对它与用户预览的入口是否一致。
    form.initializationBase = base;
    const path = item.data.url || "";
    if (base) {
      // 服务商入口本身可能含路径，拼接前必须先取得完整前缀边界。
      const prefix = new URL(base).pathname.replace(/\/$/, "");
      // 仅完整路径段相同时去重，不能把名称相近的业务路径误当成前缀。
      if (
        prefix &&
        (path === prefix ||
          path.startsWith(prefix + "/") ||
          path.startsWith(prefix + "?"))
      )
        base = base.slice(0, -prefix.length);
    }
    // 地址按纯文本展示，路径中的特殊字符不能被浏览器解释为新的页面元素。
    // Query 参数仍由服务端编码，不拼进这个用于审阅的基础地址。
    $("#initialization-url").textContent = base
      ? base.replace(/\/$/, "") + path
      : "未配置该环境的入口，请检查 settings.json";
    // 环境切换后必须重新填写 Token，避免将测试凭据误用于线上。
    form.elements.token.value = "";
  };
  // 监听器只附着本次表单，关闭弹窗后不会让旧窗口继续响应新的环境选择。
  environment.addEventListener("change", updateURL);
  // 首次计算只展示地址并清空凭据，不发起连接或业务请求。
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
  // 原生必填检查先于联网，空 Token 不会为了获取服务端错误而发送一次请求。
  if (!form.reportValidity()) return;
  // 参数与凭据只读取本次表单，不修改已经保存的模板或全局业务记录。
  const input = Object.fromEntries(new FormData(form));
  buttonElement.disabled = true;
  buttonElement.textContent = "发送中…";
  form.elements.environment.disabled = true;
  // 等待期间固定环境，避免响应尚未返回就切换入口，让用户误认结果所属环境。
  try {
    // 稳定目标及配置快照交由后端再次核验，前端预览不是最终的权限依据。
    const result = await api("initialization/send", {
      ...form.initializationTarget,
      ...input,
      base_url: form.initializationBase,
    });
    // 用户已关闭弹窗时不覆盖后来打开的页面，响应也不会写入持久化历史。
    if (form.isConnected && modalType === "initialization-request") {
      // 响应仅写入当前弹窗且经过转义，不持久化，也不执行其中可能出现的 HTML。
      $("#initialization-result").innerHTML =
        `<div class="init-result-head"><h3>调用响应</h3><span>HTTP ${result.status} · ${result.elapsed_ms} ms${result.truncated ? " · 已截断" : ""}</span></div><p class="init-input-hint">HTTP 状态只代表请求结果，请继续核对实际业务是否完成。</p><pre class="init-response-body">${escapeHTML(result.body || "（响应正文为空）")}</pre>`;
    } else toast(`初始化接口已返回 HTTP ${result.status}，请核对业务结果`);
  } finally {
    // 成功、服务端错误及网络故障都会清除 Token；下一次发送必须重新填写。
    form.elements.token.value = "";
    form.elements.environment.disabled = false;
    // 恢复按钮只允许用户重新决定，不调用自身重发可能已经执行过的初始化。
    // 即使业务返回错误也沿用同一清理出口，不能留下可被下一次发送复用的凭据。
    buttonElement.disabled = false;
    buttonElement.textContent = "发送请求";
  }
}
