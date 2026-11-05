// 内置日志连接仅在设置页临时接收密钥，摘要与项目列表都不回填 AK/SK。

/**
 * 渲染团队默认日志连接和环境项目映射，不再展示个人技能目录。
 * @param {object} connection 后端提供的无凭据摘要及团队默认路由元数据。
 * @returns {string} 日志连接设置 HTML，密钥输入始终为空。
 */
function logConnectionFields(connection) {
  // 跨用户复制导致密文不可解密时保留设置入口，明确提示重新填写整对密钥。
  // 已配置仅指本机已保存，不能把上次鉴权状态冒充本次测试结果。
  // 文件损坏提示优先于普通状态，用户需要知道为何必须重填。
  const connectionStatus =
    connection.credential_error ||
    (connection.configured
      ? "密钥已保存，尚未测试本次连接。"
      : "尚未保存密钥；填写后可先测试，再保存设置。");
  // 初始映射来自随软件分发的独立资源；读取云端项目后仍保留用户当前选择。
  // 团队映射只含可共享资源，个人凭据由独立后端接口处理。
  const routes = connection.team_routes.environments;
  // 初次打开不自动联网，默认列表不能被表述为当前账号可访问。
  const projects = Object.values(routes).map((route) => ({
    id: route.id,
    name: route.name,
  }));
  const mappings = [
    ["prod", "生产环境"],
    ["test", "测试环境"],
    ["uat", "UAT 环境"],
  ]
    .map(([environment, label]) => {
      // 环境留空表示未启用，不能在打开页面时自动借用另一套项目。
      const selected = connection.projects?.[environment] || "";
      // 自定义项目即使尚未出现在默认列表里也保留，不因打开设置而被清空。
      const options = [...projects];
      if (selected && !options.some((option) => option.id === selected))
        options.push({ id: selected, name: "当前配置 · " + selected });
      // 已保存主题与默认资源按 ID 对齐，名称仅用于展示。
      const topic = connection.topics?.[environment];
      const topicName =
        routes[environment]?.topics.find((option) => option.id === topic)
          ?.name ||
        topic ||
        "未配置 Topic";
      return `<label class="log-mapping"><span class="log-environment ${environment}"><i></i>${label}</span><span><select name="log_project_${environment}" class="log-project-select"><option value="">未配置此环境</option>${options.map((option) => `<option value="${escapeHTML(option.id)}" ${selected === option.id ? "selected" : ""}>${escapeHTML(option.name)}</option>`).join("")}</select><small>固定 Topic：${escapeHTML(topicName)}</small></span></label>`;
    })
    .join("");
  // 普通配置只显示连接与环境，底层主题 ID 放在折叠区域避免占据主要空间。
  // AK/SK 始终使用空密码框，已有状态只影响提示文字。
  // 主区域展示常用字段，主题 ID 放入高级区域供明确修正。
  // 所有动态元数据按文本转义，不允许资源名称成为 HTML。
  return `<div class="log-settings-intro"><div><h3>日志数据源</h3><p>连接火山 TLS，为 AI 验证提供只读日志证据</p></div><span class="log-rules-ready">● 扫描与日志规则已内置</span></div><section class="log-settings-card"><div class="log-settings-card-head"><h4>服务接入</h4><span>火山引擎 TLS</span></div><div class="form-grid">${formField("地域 Region", "log_region", connection.region)}${formField("服务地址 Endpoint", "log_endpoint", connection.endpoint)}${formField("Access Key ID（AK）", "log_access_key", "", { type: "password", placeholder: connection.configured ? "留空保留已保存的 AK" : "填写 AK" })}${formField("Secret Access Key（SK）", "log_secret_key", "", { type: "password", placeholder: connection.configured ? "留空保留已保存的 SK" : "填写 SK" })}</div><p class="log-security-note">密钥仅在本机加密保存，AI 只接收脱敏查询结果。</p></section><section class="log-settings-card"><div class="log-settings-card-head"><h4>环境与日志项目</h4>${button("logs-projects", "读取项目", "refresh")}</div><div class="log-mappings">${mappings}</div><p class="settings-help">已预填团队当前后端服务日志映射；生产验收固定使用生产项目与 Topic，不自动切换到其他环境。</p></section><div id="log-connection-result" class="log-connection-result" role="status">${escapeHTML(connectionStatus)}</div><details class="log-routing-details"><summary>Topic ID 映射 · 高级设置</summary>${formField("环境到后端日志 Topic ID（JSON）", "log_topics", JSON.stringify(connection.topics || {}, null, 2), { full: true, multiline: true })}</details>`;
}

/**
 * 从当前设置表单取得本次日志连接，留空密钥交给后端保留处理。
 * @param {HTMLFormElement} form 已展示的完整设置表单，不能读取其他页面的临时 Token。
 * @returns {object} 仅供本次鉴权请求使用的连接对象，不写入普通 settings.json。
 */
function logConnectionRequest(form) {
  // 隐藏标签仍属于同一表单，切换页面不会丢失尚未保存的地域与环境选择。
  const data = Object.fromEntries(new FormData(form));
  // 主题 ID 必须是合法 JSON，解析错误在发送任何凭据之前提示用户修正。
  const topics = JSON.parse(data.log_topics);
  // 明确列举凭据字段，避免把初始化接口 Token 或其他设置一起发给日志端点。
  return {
    endpoint: data.log_endpoint,
    region: data.log_region,
    access_key: data.log_access_key,
    secret_key: data.log_secret_key,
    // 项目与主题分别提交，云端查询前还会核对它们的实际归属。
    // 不从环境显示名称推断项目 ID，避免中英文标签影响查询范围。
    projects: Object.fromEntries(
      ["prod", "test", "uat"].map((environment) => [
        environment,
        data[`log_project_${environment}`],
      ]),
    ),
    // 主题来自已确认的后端映射，不读取浏览器中其他版本事项的数据。
    topics,
    // 测试使用当前明确填写的精确主机范围，避免切换地域时被旧表单快照阻断。
    // 页面明确给出的主机范围与本次连接一起校验，不沿用模型参数。
    allowed_domains: data.verification_log_domains
      .split(/[，,\s]+/)
      .filter(Boolean),
  };
}

/**
 * 将当前连接可见的项目填入环境下拉框，保留尚未保存的选择和密钥。
 * @returns {Promise<void>} 读取成功更新下拉；失败留在当前表单，由统一提示处理。
 */
async function readLogProjects() {
  // 捕获当前表单，异步返回时需核对它仍处于页面中。
  const form = $("#settings-form");
  // 读取项目只发出元数据请求，不保存连接，也不检索任何业务日志正文。
  const response = await api("logs/projects", logConnectionRequest(form));
  // 用户等待期间可能关闭设置，迟到响应不能写入另一份新表单。
  if (!form.isConnected) return;
  // 三个环境共用可见项目列表，各自保留独立选择。
  for (const select of form.querySelectorAll(".log-project-select")) {
    const selected = select.value;
    // 每个下拉使用副本，补入当前 ID 不污染其他环境的选项。
    const projects = [...response.projects];
    // 权限导致当前项目不在列表时保留原 ID，实际可用性由测试连接核对。
    if (selected && !projects.some((project) => project.id === selected))
      projects.push({ id: selected, name: "当前配置 · " + selected });
    // 云端名称按文本转义，项目元数据不能成为可执行页面内容。
    select.innerHTML =
      '<option value="">未配置此环境</option>' +
      projects
        .map(
          (project) =>
            `<option value="${escapeHTML(project.id)}" ${project.id === selected ? "selected" : ""}>${escapeHTML(project.name)}</option>`,
        )
        .join("");
  }
  // 项目列表刷新不会自动选择生产项目，也不会替用户保存映射。
  // 云端资源没有被完整读取时明确提示，不用采样列表冒充账号全部项目。
  $("#log-connection-result").textContent = response.truncated
    ? "已读取部分项目，当前配置保留；可用性请测试连接。"
    : "项目列表已更新，环境选择和未保存输入已保留。";
}

/**
 * 检查当前尚未保存连接的各环境权限，不把测试成功当作业务验收。
 * @returns {Promise<void>} 展示独立环境结果；不会保存密钥或回传测试日志样本。
 */
async function testLogConnection() {
  const form = $("#settings-form");
  // 明确只查询最近一分钟最多一条，用于确认检索权限，不输出样本内容。
  $("#log-connection-result").textContent =
    "正在核对各环境资源与只读权限（最近 1 分钟，最多 1 条，不保存日志）…";
  // 测试请求使用当前输入，用户无需先覆盖已保存连接。
  const response = await api("logs/test", logConnectionRequest(form));
  if (!form.isConnected) return;
  // 标识只转成显示标签，不用当前选中页替代返回结果所属环境。
  const labels = { prod: "生产", test: "测试", uat: "UAT" };
  // 每个环境独立展示，生产成功不能掩盖测试鉴权失败，反之亦然。
  // 每一行保留对应环境的安全摘要，页面不会获得测试检索的日志正文。
  // 测试失败仍保留未保存输入，用户无需重新填写整对密钥。
  $("#log-connection-result").innerHTML =
    response.checks
      .map(
        (check) =>
          `<div class="log-check ${check.success ? "success" : "failed"}"><b>${escapeHTML(labels[check.environment] || check.environment)} · ${check.success ? "连接可用" : "检查未通过"}</b><span>${escapeHTML(check.detail)}</span></div>`,
      )
      .join("") || "没有可测试的环境映射。";
}

/**
 * 保存用户已经配置或刚填写的日志连接，其他功能页保存时不强制填写密钥。
 * @param {HTMLFormElement} form 当前完整设置表单，连接摘要来自打开时的固定状态。
 * @param {object} request 点击保存时固定的连接对象，避免等待其他设置保存时输入发生变化。
 * @returns {Promise<void>} 已配置连接加密保存；从未填写的空连接保持未配置。
 */
async function saveLogConnection(form, request) {
  // 新用户只修改 Git 或数据库时，不因未启用日志直连而阻止原有设置保存。
  if (!form.logConfig.configured && !request.access_key && !request.secret_key)
    return;
  // 后端留空保留既有密钥；失败不清空输入，便于用户修正后再次保存。
  await api("logs/save", request);
  // 保存后清除页面明文，后续打开只显示配置状态和留空保留提示。
  form.elements.log_access_key.value = "";
  form.elements.log_secret_key.value = "";
}

/**
 * 派发日志设置的显式只读操作，与版本扫描和 SQL 的任务入口分离。
 * @param {string} action 当前按钮的内部操作标识。
 * @returns {Promise<boolean>} 已处理返回 true，其余交给原有界面入口。
 */
async function handleLogSettingsAction(action) {
  // 只有用户主动点击后才读取项目或测试连接，不在打开设置时自动联网。
  if (action === "logs-projects") {
    // 元数据读取与测试连接分开，查看项目不会被显示为业务验证通过。
    await readLogProjects();
    return true;
  }
  if (action === "logs-test") {
    // 连接检查不进入 AI 验证队列，也不占用 SQL 任务状态。
    await testLogConnection();
    return true;
  }
  // 保存统一由原设置入口执行，此处不能绕开完整表单的校验流程。
  return false;
}
