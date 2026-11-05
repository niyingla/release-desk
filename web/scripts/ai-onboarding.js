// 接入指引只准备本机学习材料；复制指令不会自动扫描、配置或调用业务接口。
let aiOnboardingPrompt = "";

/**
 * 生成并展示本机 AI 接入指引，说明内置规则和数据源授权的区别。
 * @returns {Promise<void>} 材料准备后展示弹窗；写入失败交给统一错误提示。
 */
async function showAIOnboarding() {
  // 使用当前数据目录和版本，换电脑后不复制开发者的盘符或用户名。
  const result = await api("ai/onboarding", { name: currentVersion });
  // 仅缓存本次可复制指令，数据库连接或日志密钥不会进入接口响应。
  aiOnboardingPrompt = result.prompt;
  // 路径来自本次材料生成结果，不依赖全局个人技能目录是否存在。
  // 所有动态文本转义后展示，路径和版本备注不能注入弹窗结构。
  const paths = [
    ["接入指引", result.guide],
    ["版本扫描技能", result.skill],
    ["日志查询技能", result.log_skill],
    ["环境路由技能", result.routing_skill],
  ];
  // 学习材料留在软件管理目录；不会安装或覆盖用户已有个人技能。
  const locations = paths
    .map(([label, value]) => `<dt>${label}</dt><dd>${escapeHTML(value)}</dd>`)
    .join("");
  // 文件位置默认折叠，主视图优先展示可复制指令，避免整页被长路径占满。
  // 明确三步操作与配置边界，避免把规则已内置误解为数据源已经授权。
  showModal(
    "AI 一键接入",
    "扫描、日志查询和环境路由规则随软件更新，无需另发技能文件。",
    `<div class="notice info">① 复制下方指令　② 粘贴给本机 AI 助手　③ 核对设置和版本记录<br>AI 会填写非敏感软件设置并增量整理发布事项，保留人工状态与历史。日志连接与访问授权需在当前电脑单独配置；本操作不执行 SQL、接口或生产查询。</div><div class="section-label">交给 AI 的指令</div><pre class="code scan-summary">${escapeHTML(result.prompt)}</pre><details><summary>查看内置技能与指引位置</summary><dl class="template-paths">${locations}</dl></details>`,
    `${button("open-templates", "打开指引目录", "folder")}${button("close-modal", "关闭")}${button("copy-ai-onboarding", "复制接入指令", "copy", "primary")}`,
    true,
  );
  // 弹窗只提供复制和打开目录，没有隐式的执行或自动配置按钮。
}

/**
 * 处理接入指引的显式展示与复制操作，避免混入业务任务队列。
 * @param {string} action 软件内定义的操作名称，不包含外部命令或路径。
 * @returns {Promise<boolean>} 已处理返回 true，其他操作继续由原入口派发。
 */
async function handleAIOnboardingAction(action) {
  // 展示材料不会读取凭据，也不会自动保存设置或启动模型。
  if (action === "ai-onboarding") {
    await showAIOnboarding();
    return true;
  }
  // 复制仅使用最近一次成功生成的指令，不拼接当前界面的 Token 或表单值。
  if (action === "copy-ai-onboarding") {
    await copyText(aiOnboardingPrompt);
    return true;
  }
  // 与现有软件更新、扫描和 SQL 操作保持独立派发。
  return false;
}
