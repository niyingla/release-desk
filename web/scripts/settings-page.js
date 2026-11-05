// 设置分区仅切换展示，所有输入保留在同一表单中，统一保存时不会遗漏隐藏页字段。

/**
 * 展示按功能分类的本机设置，初次打开默认选择数据库。
 * @returns {Promise<void>} 连接摘要与非敏感设置读取完成后打开弹窗，失败交给统一提示。
 */
async function showSettings() {
  // 测试与 UAT 均使用各自摘要，不将未配置环境回退到另一环境。
  // 任一摘要读取失败时先提示错误，不用不完整配置覆盖已有连接。
  // 连接端点只返回摘要，数据库密码不会因展开设置而回填到页面。
  const profiles = await Promise.all(
    state.sql_environments.map(({ id }) =>
      api("db/settings", { environment: id }),
    ),
  );
  // 设置使用同一份快照，各标签不是分别读取和覆盖的独立表单。
  const config = await api("settings/get");
  // 数据库页沿用原字段前缀，保存时仍能精确定位到对应执行环境。
  // 初始化接口入口单独归类，避免混入数据库或日志配置。
  const sections = [
    ["database", "数据库", sqlConnectionFields(profiles)],
    // 日志页只管理网络范围和补充说明，避免把访问密钥当作普通字段导出。
    ["logs", "日志", settingsLogFields(config)],
    // 仓库和作者属于同一代码上下文，AI 接入入口在此归类。
    ["code", "代码与 AI", settingsCodeFields(config)],
    // 接口基础地址不改变数据库连接，独立面板减少两类环境的混淆。
    ["initialization", "接口调用", settingsInitializationFields(config)],
  ];
  // 标签和面板使用固定内部 ID，动态设置值仅出现在转义后的输入框中。
  const tabs = sections
    // 初始只允许数据库标签进入顺序焦点，其余通过方向键或点击访问。
    .map(
      ([id, label], index) =>
        `<button type="button" id="settings-tab-${id}" role="tab" aria-selected="${index === 0}" aria-controls="settings-panel-${id}" tabindex="${index === 0 ? 0 : -1}" data-action="settings-tab" data-tab="${id}">${label}</button>`,
    )
    .join("");
  // 隐藏页保留原节点，切换不丢失密码、路径或尚未保存的编辑。
  const panels = sections
    // 面板标题和标签建立显式关联，辅助设备可以识别当前设置分类。
    .map(
      ([id, label, content], index) =>
        `<section id="settings-panel-${id}" role="tabpanel" aria-labelledby="settings-tab-${id}" ${index ? "hidden" : ""}><h3 class="settings-section-title">${label}</h3>${content}</section>`,
    )
    .join("");
  // 底部保留统一保存和取消，标签切换本身没有写盘或连接副作用。
  showModal(
    "连接与设置",
    "按功能切换设置，编辑内容统一保存；凭据不会写入版本记录。",
    `<form id="settings-form"><div class="settings-tabs" role="tablist" aria-label="设置分类">${tabs}</div><div class="settings-panels">${panels}</div></form>`,
    `${button("open-data", "数据目录", "folder")}${button("close-modal", "取消")}${button("save-settings", "保存设置", "check", "primary")}`,
  );
  // 分区样式限定在当前设置弹窗，其他编辑和执行确认窗口不受影响。
  // 保存流程仍从完整快照增量写入，保留当前页面未管理的其他配置字段。
  // 此处只保存页面快照，真正的连接加密和配置校验仍在受控后端完成。
  $("#settings-form").config = config;
  $(".modal").classList.add("settings-modal");
  // 打开设置不会自动连接数据库或启动日志查询，用户可以先检查再决定是否保存。
  // 点击切换由统一事件入口处理；这里仅补充标签组的键盘导航。
  // 方向键和 Home/End 遵循标签控件习惯，不阻碍输入框原来的键盘行为。
  $(".settings-tabs").addEventListener("keydown", (event) => {
    const controls = [
      ...document.querySelectorAll(".settings-tabs [role=tab]"),
    ];
    const index = controls.indexOf(event.target);
    // 只消费真实标签上的导航键，普通输入和表单快捷键保持浏览器默认行为。
    if (
      index < 0 ||
      !["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)
    )
      return;
    // 左右键循环遍历，Home 和 End 直接定位首尾，无需用户滚动整张设置表单。
    const next =
      event.key === "Home"
        ? 0
        : event.key === "End"
          ? controls.length - 1
          : (index + (event.key === "ArrowRight" ? 1 : -1) + controls.length) %
            controls.length;
    event.preventDefault();
    // 展示和焦点同时移动，避免键盘选中标题与实际显示面板不一致。
    selectSettingsTab(controls[next].dataset.tab);
    controls[next].focus();
  });
}

/**
 * 生成代码仓库和 AI 接入字段，保持 SQL 与业务源码仓库职责分离。
 * @param {object} config 当前非敏感设置快照，不包含数据库或日志密钥。
 * @returns {string} 原有路径和作者字段，以及准备学习材料的显式入口。
 */
function settingsCodeFields(config) {
  // 下拉分支来自代码仓库，SQL 正文及作者提交仍从 SQL 仓库读取。
  // Codex 安装登录属于本机环境，接入指令不复制另一用户的登录态。
  return `<div class="form-grid">${formField("代码仓库目录", "code_repository", config.code_repository, { full: true })}${formField("SQL 仓库目录", "sql_repository", config.sql_repository, { full: true })}${formField("Git 提交作者", "git_author", config.git_author)}</div><div class="notice info">新建版本仅列出代码库 release- 开头的分支。扫描读取所选版本的固定提交，不切换当前工作分支。Codex CLI 需在本机安装并登录。</div><div class="settings-materials">${button("ai-onboarding", "AI 一键接入", "spark")}${button("files", "版本文件夹", "folder")}<small>内置技能随软件更新；可复制指令让 AI 配置本机设置和发布记录。</small></div>`;
}

/**
 * 生成日志取证的现有非敏感设置，明确规则和连接授权的边界。
 * @param {object} config 当前日志补充技能和允许主机设置，不含访问密钥。
 * @returns {string} 可选本机补充入口、网络范围和实际连接方式说明。
 */
function settingsLogFields(config) {
  // 当前通过 MCP 或已有只读脚本取数，不能展示尚未实现的直连密钥保存功能。
  // 补充技能留空仍使用内置规则，允许主机也不代表该数据源已完成鉴权。
  return `<div class="notice info">日志查询与环境路由规则已内置，随软件一起更新。当前复用本机已配置的 MCP 或只读查询脚本；连接和密钥由相应工具管理，软件不会读取或复制到 AI 指引中。</div><div class="form-grid">${formField("本机补充日志技能（可选，留空使用内置规则）", "log_skill", config.log_skill, { full: true })}${formField("允许查询的日志域名（逗号或换行分隔）", "verification_log_domains", (config.verification_log_domains || []).join("\n"), { full: true, multiline: true })}</div><p class="settings-help">没有可用工具或查询授权时，验证会说明配置缺口；不会把连接失败当作业务没有日志。请勿在上述字段填写 AK、SK、密码或 Token。</p>`;
}

/**
 * 生成初始化接口的环境入口字段，凭据仍在每次手动请求时临时填写。
 * @param {object} config 测试和生产三端基础地址的非敏感配置快照。
 * @returns {string} 保留完整环境结构的 JSON 编辑字段及调用边界说明。
 */
function settingsInitializationFields(config) {
  // 环境地址与 Token 分开保存；基础地址不改变手动调用默认使用测试环境的规则。
  return `<div class="form-grid">${formField("环境入口（JSON，测试 / 线上三端）", "initialization_base_urls", JSON.stringify(config.initialization_base_urls || {}, null, 2), { full: true, multiline: true })}</div><p class="settings-help">这里只保存基础地址。Bearer Token 每次调用临时填写，切换环境、成功或失败后清空，不随版本记录导出。</p>`;
}

/**
 * 切换设置标签的可见与键盘状态，不重新读取或销毁输入内容。
 * @param {string} id 设置页已有的固定分区标识，不接受任意 DOM 选择器。
 * @returns {void} 有效标签更新展示；过期弹窗或未知标识不处理。
 */
function selectSettingsTab(id) {
  const tabs = [...document.querySelectorAll(".settings-tabs [role=tab]")];
  // 只允许页面已存在的标签，外部文本不能指定其他页面元素。
  if (!tabs.some((tab) => tab.dataset.tab === id)) return;
  for (const tab of tabs) {
    const selected = tab.dataset.tab === id;
    // ARIA 状态与视觉选中态共用一个判断，不维护两套可能分叉的分类状态。
    // 隐藏保持原始输入节点，统一 FormData 仍会提交各页的完整设置。
    tab.setAttribute("aria-selected", String(selected));
    tab.tabIndex = selected ? 0 : -1;
    // 不禁用隐藏字段，保存完整表单时仍能取得其他分类中未提交的输入。
    document.getElementById(tab.getAttribute("aria-controls")).hidden =
      !selected;
  }
}
