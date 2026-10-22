// 界面回归只操作隔离的 QA 数据目录，不启动 Codex、不执行数据库 SQL。
const { chromium } = require("playwright");
const fs = require("fs");
const path = require("path");
const { execFileSync } = require("child_process");
let tearingDown = false;

/**
 * 隐去测试异常中的本机能力令牌，避免请求诊断把认证信息写进日志。
 * @param {unknown} error 浏览器或路由回调抛出的异常。
 * @returns {string} 保留定位原因的脱敏消息。
 */
function safeTestError(error) {
  return String(error?.message || error).replace(
    /(x-release-token:\s*)[^\s'"\]]+/gi,
    "$1[redacted]",
  );
}
process.on("unhandledRejection", (error) => {
  // 浏览器结束时丢弃仍在途的轮询；功能检查之前的异常仍使测试失败。
  if (
    tearingDown &&
    /Request context disposed|Target.*closed/i.test(String(error?.message))
  )
    return;
  console.error(safeTestError(error));
  process.exitCode = 1;
});

(async () => {
  const base = process.argv[2];
  const output = path.resolve(__dirname, "../../.work/qa/artifacts");
  fs.mkdirSync(output, { recursive: true });
  // Edge 可能安装在系统或用户目录；只复用本机浏览器，不为测试安装新浏览器。
  const edgeBinary = [
    process.env.EDGE_BINARY,
    path.join(
      process.env["ProgramFiles(x86)"] || "",
      "Microsoft/Edge/Application/msedge.exe",
    ),
    path.join(
      process.env.ProgramFiles || "",
      "Microsoft/Edge/Application/msedge.exe",
    ),
    path.join(
      process.env.LOCALAPPDATA || "",
      "Microsoft/Edge/Application/msedge.exe",
    ),
  ].find((candidate) => candidate && fs.existsSync(candidate));
  const browser = await chromium.launch({
    channel: edgeBinary ? undefined : "msedge",
    executablePath: edgeBinary,
    headless: true,
  });
  const context = await browser.newContext({
    viewport: { width: 1440, height: 960 },
    deviceScaleFactor: 1,
    permissions: ["clipboard-read", "clipboard-write"],
  });
  const page = await context.newPage();
  const failures = [];
  page.on("pageerror", (error) => failures.push(error.message));
  await page.goto(base);
  await page.getByRole("heading", { name: "版本总览", exact: true }).waitFor();
  await require("./task-center.cjs")(page, output);
  const softwareVersion = await page.evaluate(() => state.software.current);
  await page.locator('[data-action="software-info"]').click();
  await page
    .getByRole("heading", {
      name: `Release Desk · v${softwareVersion}`,
      exact: true,
    })
    .waitFor();
  await page
    .locator(".bulk-preview-item summary")
    .filter({ hasText: "v1.0.0" })
    .click();
  if (!(await page.locator("#modal-body").innerText()).includes("统一补录基线"))
    throw new Error("缺少此前 1.0 软件功能记录");
  await page.screenshot({ path: path.join(output, "software-releases.png") });
  await page.locator('[data-action="close-modal"]').first().click();
  // 主题落入软件偏好文件，刷新与随机端口无关；所有配色均应能即时应用。
  const sidebarPalettes = [];
  for (const theme of ["graphite", "purple", "forest", "light", "night"]) {
    await page.locator("#theme-select").selectOption(theme);
    await page.waitForFunction(
      () => !document.querySelector("#theme-select").disabled,
    );
    if ((await page.locator("html").getAttribute("data-theme")) !== theme)
      throw new Error("主题切换失败");
    // 检查实际渲染颜色，防止仅切换主题标识而侧栏仍保留固定深蓝色。
    const palette = await page.evaluate(() => {
      const background = (selector) =>
        getComputedStyle(document.querySelector(selector)).backgroundColor;
      return {
        sidebar: background(".sidebar"),
        main: background("body"),
        workspace: background(".workspace"),
        active: background(".sidebar .nav.active"),
      };
    });
    if (
      palette.sidebar === palette.main ||
      palette.sidebar === palette.workspace ||
      palette.sidebar === palette.active
    )
      throw new Error(`${theme} 侧栏、主区、卡片或选中态缺少颜色层次`);
    sidebarPalettes.push(palette.sidebar);
    await page.screenshot({ path: path.join(output, `theme-${theme}.png`) });
  }
  if (new Set(sidebarPalettes).size !== sidebarPalettes.length)
    throw new Error("侧栏未跟随所有主题分别改变色系");
  await page.reload();
  await page.getByRole("heading", { name: "版本总览", exact: true }).waitFor();
  if ((await page.locator("#theme-select").inputValue()) !== "night")
    throw new Error("主题刷新后未保留");
  // 从界面创建版本，确认表单、接口和本地保存形成完整闭环。
  await page.locator('[data-action="new-version"]').last().click();
  if (await page.locator('[name="ai_scan"]').isChecked())
    throw new Error("AI 扫描不应默认启动");
  await page.locator('#version-form [name="name"]').fill("5.7.3");
  await page
    .locator('#version-form [name="notes"]')
    .fill("界面示例 · 发布事项管理与上线验证");
  await page.locator('[data-action="save-version"]').click();
  await page.locator(".tab.active").waitFor();
  // 上千字的扫描依据应收进说明弹窗；页头高度和完整内容都需要保留。
  const versionBeforeNotes = await page.evaluate(() => versionData().version);
  const longNotes =
    "扫描对象：5.7.3；来源 SHA 与排除记录。".repeat(100) + "完整说明末尾";
  await page.evaluate(
    async (version) => {
      await api("version/save", version);
      await refresh(true);
    },
    { ...versionBeforeNotes, notes: longNotes },
  );
  if ((await page.locator(".version-header").boundingBox()).height > 180)
    throw new Error("长版本说明撑大了页头");
  if (
    (await page.locator(".version-header").innerText()).includes("完整说明末尾")
  )
    throw new Error("完整扫描流水账仍展示在页头");
  await page.locator('[data-action="version-notes"]').click();
  if ((await page.locator(".version-notes-content").innerText()) !== longNotes)
    throw new Error("版本说明弹窗丢失了原始备注");
  await page.screenshot({
    path: path.join(output, "version-notes-dialog.png"),
  });
  await page.locator('[data-action="close-modal"]').first().click();
  await page.screenshot({
    path: path.join(output, "version-header-compact.png"),
  });
  await page.evaluate(async (version) => {
    await api("version/save", version);
    await refresh(true);
  }, versionBeforeNotes);
  const items = [
    {
      kind: "job",
      data: {
        handler: "syncCustomerStatusHandler",
        frequency: "0 0/10 * * * ?",
        description: "示例：每 10 分钟同步客户状态，避免遗漏未完成任务。",
      },
      service: "qiyu-douyin-customer",
    },
    {
      kind: "job",
      data: {
        handler: "cleanupExpiredTaskHandler",
        frequency: "0 0 2 * * ?",
        description: "示例：每日清理过期任务，保留必要审计信息。",
      },
      service: "qiyu-douyin-system",
    },
    {
      kind: "config",
      data: {
        filename: "qiyu-douyin-customer.yml",
        prefix: "customer.followup",
        content:
          "# 客户跟进策略（界面示例）\ncustomer:\n  followup:\n    enabled: true\n    intervalMinutes: 10\n    batchSize: 200",
        description: "示例：启用新版客户跟进策略，按批次处理待办。",
      },
      service: "qiyu-douyin-customer",
    },
    {
      kind: "config",
      data: {
        filename: "qiyu-douyin-system.yml",
        prefix: "release.verification",
        content: "release:\n  verification:\n    enabled: true",
        description: "示例：开启版本验证日志。",
      },
      service: "qiyu-douyin-system",
    },
    {
      kind: "sql",
      data: {
        name: "客户跟进策略字段调整（示例）",
        side: "租户端",
        order: "1",
        sql: "-- 界面示例，未执行\nALTER TABLE cst_followup_record\n  ADD COLUMN followup_status INT NOT NULL DEFAULT 0\n  COMMENT '跟进状态';\n\nCREATE INDEX idx_followup_status\n  ON cst_followup_record (followup_status);",
        rollback: "上线前保留备份，回滚前核对新增字段使用情况。",
        description: "示例：新增跟进状态字段及查询索引。",
      },
      service: "qiyu-douyin-customer",
    },
    {
      kind: "topic",
      data: {
        topic: "customer_followup_changed",
        partitions: "3",
        group: "customer-followup-group",
        description: "示例：新增跟进状态变更消息。",
      },
      service: "qiyu-douyin-customer",
    },
    {
      kind: "verify",
      data: {
        title: "客户跟进状态自动同步（示例）",
        change: "同步任务改为分批处理，补充处理数量和耗时日志。",
        steps:
          "1. 上线后触发客户状态同步。\n2. 查询指定服务在上线后的日志。\n3. 核对批次处理数量、结果和异常信息。",
        keywords: "[syncCustomerStatus] completed",
        expected: "出现同步完成日志；处理数量与本批数据一致；无异常堆栈。",
        result: "",
      },
      service: "qiyu-douyin-customer",
    },
  ];
  for (let i = 0; i < items.length; i++)
    await page.evaluate(
      async (item) =>
        api("item/save", {
          version: "5.7.3",
          item: { ...item, action: "新增", status: "待处理" },
        }),
      { ...items[i], id: "qa-" + items[i].kind + "-" + i },
    );
  await page.evaluate(() => refresh(true));
  await page.screenshot({ path: path.join(output, "01-jobs.png") });
  // 验证点击切换线上状态，双击配置复制时得到未截断的原文。
  await page.locator('[data-action="online"]').first().click();
  await page.locator('[data-action="tab"][data-kind="config"]').click();
  await page
    .locator("td[data-copy]")
    .filter({ hasText: "customer:" })
    .first()
    .dblclick();
  await page.getByText("已复制完整内容", { exact: true }).waitFor();
  // Headless Chromium 使用自己的虚拟剪贴板；桌面程序应核对真实 Windows 剪贴板。
  const clipboardBase64 = execFileSync(
    "powershell.exe",
    [
      "-NoProfile",
      "-Command",
      "[Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes((Get-Clipboard -Raw)))",
    ],
    { encoding: "utf8" },
  ).trim();
  const copied = Buffer.from(clipboardBase64, "base64")
    .toString("utf8")
    .replace(/\r\n/g, "\n");
  if (copied !== items[2].data.content) throw new Error("配置复制内容不一致");
  await page.screenshot({ path: path.join(output, "02-config.png") });
  if (
    !(await page
      .locator(".detail-aside")
      .innerText()
      .then((text) => text.includes(items[2].data.description)))
  )
    throw new Error("配置详情缺少描述");
  await page.locator('[data-action="tab"][data-kind="sql"]').click();
  // 使用刻意相反的来源目录和顺序号，验证展示以 SQL 正文类型优先。
  for (const [id, sql, source_path, order] of [
    ["qa-data", "INSERT INTO demo VALUES(1);", "新增表/first.sql", "0"],
    ["qa-create", "CREATE TABLE demo(id INT);", "初始化/last.sql", "99"],
  ]) {
    await page.evaluate(async (data) => api("item/save", data), {
      version: "5.7.3",
      item: {
        id,
        kind: "sql",
        action: "新增",
        status: "待处理",
        data: { name: id, side: "租户端", sql, source_path, order },
      },
    });
  }
  await page.evaluate(() => refresh(true));
  const rows = await page.locator(".table-wrap tbody tr").allTextContents();
  if (
    !rows[0]?.includes("创建表") ||
    !rows[1]?.includes("修改表") ||
    !rows[2]?.includes("初始化数据")
  ) {
    throw new Error("SQL 表格未按正文类型排序：" + rows.join(" | "));
  }
  await page.screenshot({ path: path.join(output, "03-sql.png") });
  // 执行预览使用模拟目标，不连接数据库；只验证勾选与布尔请求协议。
  await page.route("**/api/db/plan", (route) =>
    route.fulfill({
      json: {
        data: {
          token: "mock-plan",
          side: "租户端",
          databases: ["newlink", "newlink1"],
          skipped: [],
          sql: "SELECT 1;",
          statements: 1,
        },
      },
    }),
  );
  let executePayload;
  await page.route("**/api/db/execute", (route) => {
    executePayload = route.request().postDataJSON();
    return route.fulfill({ json: { data: null } });
  });
  await page.locator('[data-action="execute"]').click();
  if (await page.locator('[name="confirm"]').count())
    throw new Error("仍显示执行文字输入框");
  if (!(await page.locator('[data-action="confirm-execute"]').isDisabled()))
    throw new Error("未勾选时可执行");
  await page.locator("#execute-understood").check();
  await page.locator('[data-action="confirm-execute"]').click();
  // 等待异步接口后的进度面板，不能误把旧预览框关闭后又被进度框覆盖。
  await page
    .getByRole("heading", { name: "测试环境逐库执行", exact: true })
    .waitFor();
  if (executePayload?.confirm !== true) throw new Error("执行未提交勾选确认");
  await page.locator('[data-action="close-modal"]').first().click();
  await page.unroute("**/api/db/plan");
  await page.unroute("**/api/db/execute");
  await page.locator('[data-action="tab"][data-kind="verify"]').click();
  await page.locator('[data-action="verify"]').click();
  await page.getByRole("heading", { name: "AI 智能验证" }).waitFor();
  // 只验证调用前的窗口，不发送生产查询请求，避免测试消耗用户账号额度。
  await page.locator('[data-action="close-modal"]').first().click();
  await page.screenshot({ path: path.join(output, "04-verification.png") });
  await require("./batches.cjs")(page, output);
  await page.locator('[data-action="overview"]').click();
  await page.screenshot({ path: path.join(output, "05-overview.png") });
  // 新建时重新读取 Git 候选，检查名称倒序、搜索和键盘选择。
  await page.locator('[data-action="new-version"]').last().click();
  await page.locator("#version-options").waitFor({ state: "visible" });
  const gitNames = await page
    .locator('#version-options [role="option"]')
    .evaluateAll((options) => options.map((option) => option.dataset.name));
  if (!gitNames.length) throw new Error("新建版本没有读取 Git 候选");
  const sortedNames = [...gitNames].sort((a, b) =>
    b.localeCompare(a, undefined, { numeric: true }),
  );
  if (JSON.stringify(gitNames) !== JSON.stringify(sortedNames))
    throw new Error("Git 下拉候选未按名字倒序");
  const query = gitNames[0].slice(0, -1);
  await page.locator("#version-name").fill(query);
  const filtered = await page
    .locator('#version-options [role="option"]')
    .evaluateAll((options) => options.map((option) => option.dataset.name));
  if (!filtered.length || filtered.some((name) => !name.includes(query)))
    throw new Error("Git 版本搜索结果不正确");
  await page.screenshot({ path: path.join(output, "07-version-picker.png") });
  await page.locator("#version-name").press("ArrowDown");
  await page.locator("#version-name").press("Enter");
  if ((await page.locator("#version-name").inputValue()) !== filtered[0])
    throw new Error("键盘未选择高亮 Git 版本");
  // 已记录的版本重新选择时只打开原记录，不用空表单覆盖原备注。
  await page.locator("#version-name").fill("5.7.3");
  await page.locator('#version-form [name="notes"]').fill("不应覆盖已有版本");
  await page.locator('[data-action="save-version"]').click();
  const original = await page.evaluate(() => versionData());
  if (!original.version.notes.includes("界面示例"))
    throw new Error("重新选择已有版本覆盖了原信息");

  // 长内容按纯文本展示，窗口边缘不裁剪，进入浮层后仍能滚动查看尾部。
  const longText =
    Array.from(
      { length: 80 },
      (_, i) => `第 ${i + 1} 行：保留全部内容和换行。`,
    ).join("\n") + '\n<img src=x onerror="window.previewInjected=true">';
  await page.evaluate(
    async (description) =>
      api("item/save", {
        version: "5.7.3",
        item: {
          id: "qa-hover",
          kind: "job",
          action: "新增",
          status: "待处理",
          data: {
            handler: "hoverFullContentHandler",
            frequency: "0 0 10 * * ?",
            description,
          },
        },
      }),
    longText,
  );
  await page.evaluate(() => refresh(true));
  await page.locator('[data-action="tab"][data-kind="job"]').click();
  await page.evaluate(() => {
    window.previewTrace = [];
    for (const type of ["pointerover", "pointerout", "scroll"])
      document.addEventListener(
        type,
        (event) => {
          window.previewTrace.push({
            type,
            target: event.target.className || event.target.nodeName,
            next: event.relatedTarget?.className,
            hidden: document.querySelector(".cell-preview")?.hidden,
          });
        },
        true,
      );
  });
  await page.locator('tr[data-select="qa-hover"] td[data-copy]').last().hover();
  const preview = page.locator(".cell-preview-content");
  await preview.waitFor({ state: "visible" });
  if (
    (await preview.textContent()) !== longText ||
    (await preview.locator("img").count())
  )
    throw new Error("悬停内容丢失或被解释成 HTML");
  const bounds = await page.locator(".cell-preview").boundingBox();
  // 滚动到单元格的通知可能迟到；位置未变时不能关闭刚显示的浮层。
  await page.evaluate(() =>
    document.querySelector("#content").dispatchEvent(new Event("scroll")),
  );
  if (!(await page.locator(".cell-preview").isVisible()))
    throw new Error("迟到的滚动通知关闭了位置未变的全文浮层");
  if (
    bounds.x < 0 ||
    bounds.y < 0 ||
    bounds.x + bounds.width > 1440 ||
    bounds.y + bounds.height > 960
  )
    throw new Error("悬停浮层超出窗口");
  try {
    await preview.hover({ timeout: 5000 });
  } catch (error) {
    console.error(await page.evaluate(() => window.previewTrace.slice(-25)));
    throw error;
  }
  await page.mouse.wheel(0, 500);
  try {
    await page.waitForFunction(
      () => document.querySelector(".cell-preview-content").scrollTop > 0,
      null,
      { timeout: 3000 },
    );
  } catch (error) {
    console.error(
      "HOVER DIAGNOSTIC",
      await page.evaluate(() => {
        const content = document.querySelector(".cell-preview-content");
        return {
          trace: window.previewTrace.slice(-30),
          top: content.scrollTop,
          height: content.clientHeight,
          scrollHeight: content.scrollHeight,
          text: content.textContent.slice(0, 100),
          hidden: document.querySelector(".cell-preview").hidden,
        };
      }),
    );
    await page.screenshot({ path: path.join(output, "hover-failure.png") });
    throw error;
  }
  await page.screenshot({
    path: path.join(output, "06-hover-full-content.png"),
  });
  await page.keyboard.press("Escape");
  await page.locator(".cell-preview").waitFor({ state: "hidden" });

  // 归档后快捷入口消失，归档列表可恢复；事项与执行审计不受导航操作影响。
  const beforeArchive = await page.evaluate(() => versionData().items);
  await page.locator('.page-head [data-action="archive-version"]').click();
  await page.locator('[data-action="confirm-archive-version"]').click();
  await page.waitForFunction(
    () => !document.querySelector('#side-versions [data-name="5.7.3"]'),
  );
  await page.locator('[data-action="archived-versions"]').click();
  await page.getByRole("heading", { name: "归档版本", exact: true }).waitFor();
  await page.screenshot({ path: path.join(output, "08-archived.png") });
  await page
    .locator('[data-action="archive-version"][data-name="5.7.3"]')
    .click();
  await page.locator('[data-action="confirm-archive-version"]').click();
  await page.locator('#side-versions [data-name="5.7.3"]').waitFor();
  const afterRestore = await page.evaluate(() => versionData().items);
  if (JSON.stringify(beforeArchive) !== JSON.stringify(afterRestore))
    throw new Error("归档恢复改变了版本事项");
  // 侧栏移除模板与文件夹入口；文件夹收进设置，测试不真正打开资源管理器。
  if (
    await page
      .locator('[data-action="templates"], .sidebar [data-action="files"]')
      .count()
  )
    throw new Error("侧栏仍显示已移除的入口");
  const folders = [];
  await page.route("**/api/folder", (route) => {
    folders.push(route.request().postDataJSON().kind);
    return route.fulfill({ json: { data: null } });
  });
  await page.locator('[data-action="settings"]').click();
  await page.getByRole("heading", { name: "连接与设置" }).waitFor();
  await page.screenshot({
    path: path.join(output, "scan-settings-folder.png"),
  });
  await page.locator('#modal-root [data-action="files"]').click();
  if (JSON.stringify(folders) !== JSON.stringify(["versions"]))
    throw new Error("设置中的版本文件夹入口没有打开正确目录");
  await page.locator('[data-action="close-modal"]').first().click();
  await page.unroute("**/api/folder");
  // 新建勾选和已有版本入口共用扫描接口；拦截请求避免测试调用收费 CLI。
  const scans = [];
  const cancelled = [];
  let fakeScan = null;
  let scanRevision = 100000;
  await page.route("**/api/state", async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    body.data.scan = fakeScan;
    body.data.ai_busy = !!fakeScan?.running;
    body.data.ai_progress = fakeScan?.phase || "";
    body.data.revision += scanRevision;
    await route.fulfill({ response, json: body });
  });
  await page.route("**/api/ai/scan", (route) => {
    const version = route.request().postDataJSON().name;
    scans.push(version);
    fakeScan = {
      id: `qa-scan-${scans.length}`,
      version,
      author: "huohuo",
      running: true,
      phase: "Codex 正在读取代码与 Git",
      started: new Date(Date.now() - 8000).toISOString(),
      finished: "",
      cancel_requested: false,
      cancelled: false,
      summary: "",
      warnings: [],
      counts: {},
      error: "",
    };
    scanRevision++;
    return route.fulfill({ json: { data: null } });
  });
  await page.route("**/api/ai/scan/cancel", (route) => {
    cancelled.push(route.request().postDataJSON().id);
    fakeScan.cancel_requested = true;
    fakeScan.phase = "正在终止扫描";
    scanRevision++;
    return route.fulfill({ json: { data: null } });
  });
  await page.locator('.page-head [data-action="scan-version"]').click();
  await page.locator('[data-action="confirm-scan"]').click();
  await page.getByRole("heading", { name: "AI 扫描版本" }).waitFor();
  await page
    .getByText("版本 5.7.3 · Codex 正在读取代码与 Git", { exact: true })
    .last()
    .waitFor();
  await page.screenshot({ path: path.join(output, "scan-running-modal.png") });
  await page.locator('[data-action="close-modal"]').first().click();
  await page.locator('#scan-status [data-action="scan-progress"]').click();
  if (scans.length !== 1) throw new Error("查看进度重复启动了扫描");
  await page.locator('[data-action="close-modal"]').first().click();
  // 状态条复用主题语义色，不能在深色主题中留下白底浅色字。
  if (
    !(await page.locator("#scan-status").evaluate((element) => {
      const probe = document.createElement("span");
      probe.style.backgroundColor = "var(--surface-raised)";
      element.append(probe);
      const matches =
        getComputedStyle(element).backgroundColor ===
        getComputedStyle(probe).backgroundColor;
      probe.remove();
      return matches;
    }))
  )
    throw new Error("扫描状态条未使用当前主题背景");
  await page.locator('#scan-status [data-action="cancel-scan"]').click();
  await page.waitForFunction(
    () =>
      document
        .querySelector("#scan-status b")
        ?.textContent.includes("正在终止扫描") &&
      document.querySelector('#scan-status [data-action="cancel-scan"]')
        ?.disabled,
  );
  if (JSON.stringify(cancelled) !== JSON.stringify(["qa-scan-1"]))
    throw new Error("终止扫描未绑定当前任务 ID");
  await page.screenshot({
    path: path.join(output, "scan-cancelling-banner.png"),
  });
  fakeScan = {
    ...fakeScan,
    running: false,
    cancelled: true,
    phase: "已终止",
    finished: new Date().toISOString(),
    summary: "扫描已终止，已有版本记录未修改。",
  };
  scanRevision++;
  await page.evaluate(() => refresh(true));
  await page.locator('#scan-status [data-action="scan-progress"]').click();
  if (
    !(await page.locator("#modal-body").innerText()).includes(
      "已有版本记录未修改",
    ) ||
    (await page.locator("#modal-body").innerText()).includes(
      "扫描已合并保存",
    ) ||
    (await page.locator('[data-action="cancel-scan"]').count())
  )
    throw new Error("终止状态错误地显示成功、修改或仍可停止");
  await page.screenshot({ path: path.join(output, "scan-cancelled.png") });
  await page.locator('[data-action="close-modal"]').first().click();
  await page.locator('[data-action="new-version"]').first().click();
  await page.locator("#version-name").fill("5.7.99-ui-scan");
  await page.locator('[name="ai_scan"]').check();
  await page.locator('[data-action="save-version"]').click();
  await page.locator('[data-action="confirm-scan"]').click();
  await page.getByRole("heading", { name: "AI 扫描版本" }).waitFor();
  if (JSON.stringify(scans) !== JSON.stringify(["5.7.3", "5.7.99-ui-scan"]))
    throw new Error("AI 扫描未绑定当前版本");
  await page.locator('[data-action="close-modal"]').first().click();
  await page.unroute("**/api/ai/scan");
  await page.unroute("**/api/ai/scan/cancel");
  await page.unroute("**/api/state");
  if (failures.length) throw new Error(failures.join("\n"));
  console.log(
    "UI PASS: 任务中心/进度/逐库结果/证据/精确停止、软件版本记录、SQL 与验证勾选/全选/批量处理、创建/持久化、五套主题与侧栏联动、长说明折叠、配置描述、完整复制、Git 搜索、归档恢复、扫描阶段/收起/终止；浏览器错误 0（Codex 和 SQL 调用均模拟）",
  );
  tearingDown = true;
  await page.unrouteAll({ behavior: "ignoreErrors" });
  await browser.close();
})().catch((error) => {
  console.error(safeTestError(error));
  process.exit(1);
});
