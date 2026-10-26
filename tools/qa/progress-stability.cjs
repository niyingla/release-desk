// 使用隔离服务和虚构验证项检查倒序、增量刷新及逐行验证，不调用真实模型。
const { chromium } = require("playwright");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");

(async () => {
  const browser = await chromium.launch({ channel: "msedge", headless: true });
  const page = await browser.newPage({
    viewport: { width: 1440, height: 960 },
  });
  const version = "qa-stable-progress",
    errors = [],
    requests = [];
  page.on("pageerror", (error) => errors.push(error.message));
  const progress = Array.from({ length: 18 }, (_, index) => ({
    at: `2026-10-23T00:00:${String(index).padStart(2, "0")}+08:00`,
    summary: `阶段 ${index}：核对关键业务代码及日志`,
  }));
  const queue = {
    id: "stable-queue",
    version,
    running: true,
    concurrency: 6,
    rows: [
      {
        id: "active-row",
        item_id: "active",
        version,
        title: "正在核验的核心业务",
        state: "验证中",
        detail: "结合代码与日志取证",
        progress,
      },
    ],
  };
  const items = ["active", "pending", "failed"].map((id, index) => ({
    id,
    kind: "verify",
    verify_state: ["待验证", "待验证", "失败"][index],
    action: "修改",
    status: "待处理",
    data: { title: id, change: "核心业务目标" },
  }));
  items[2].verify_runs = [
    {
      status: "失败",
      summary: "关键业务结果不符合预期",
      analysis: {
        code: "新代码已修改分支",
        logs: "线上仍出现旧分支结果",
        reasoning: "代码与实际业务结果不一致",
      },
      evidence: ["业务日志 <script>window.badEvidence=true</script>"],
      next_steps: "补充相同业务样本",
    },
  ];
  let revision = 200;
  // 可选加载当前源码资源，验证修复时无需中断正在运行的旧软件。
  if (process.argv.includes("--source")) {
    await page.route("**/scripts/*.js", async (route) =>
      route.fulfill({
        contentType: "application/javascript",
        body: fs.readFileSync(
          path.resolve(
            __dirname,
            "../../web/scripts",
            new URL(route.request().url()).pathname.split("/").at(-1),
          ),
          "utf8",
        ),
      }),
    );
    await page.route("**/", async (route) => {
      const response = await route.fetch();
      let html = await response.text();
      if (!html.includes("/scripts/progress-view.js"))
        html = html.replace(
          '<script src="/scripts/batch-actions.js">',
          '<script src="/scripts/progress-view.js"></script><script src="/scripts/batch-actions.js">',
        );
      await route.fulfill({ response, body: html });
    });
  }
  await page.route("**/api/state", async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    Object.assign(body.data, {
      versions: [{ version: { name: version, status: "计划中" }, items }],
      revision,
      verify_queue: queue,
      ai_busy: true,
      single_verify: null,
      scan: null,
      batch: null,
      sql_queue: null,
      tasks: [],
    });
    await route.fulfill({ response, json: body });
  });
  await page.route("**/api/ai/verify", async (route) => {
    requests.push(route.request().postDataJSON());
    await route.fulfill({
      json: { data: { queue_id: queue.id, accepted: true } },
    });
  });
  try {
    await page.goto(process.argv[2]);
    await page.locator(`#side-versions [data-name="${version}"]`).click();
    await page.locator('[data-action="tab"][data-kind="verify"]').click();
    // 状态预览绑定本行已保存的结构化结果，显示实际依据而非状态两个字。
    await page
      .locator('tr[data-select="failed"] td[data-preview-title]')
      .hover();
    const preview = page.locator(".cell-preview:visible");
    assert.match(await preview.innerText(), /关键结论与证据/);
    assert.match(await preview.innerText(), /关键业务结果不符合预期/);
    assert.match(await preview.innerText(), /新代码已修改分支/);
    assert.match(await preview.innerText(), /线上仍出现旧分支结果/);
    assert.match(await preview.innerText(), /补充相同业务样本/);
    assert.equal(await page.evaluate(() => window.badEvidence), undefined);
    await page
      .locator('tr[data-select="pending"] td[data-preview-title]')
      .hover();
    assert.match(await preview.innerText(), /尚无验证结论或证据/);
    await page
      .locator('tr[data-select="active"] td[data-preview-title]')
      .hover();
    assert.match(await preview.innerText(), /阶段 17/);
    await page
      .locator('tr[data-select="pending"] [data-action="verify-item"]')
      .click();
    await page.locator('[data-action="start-verify"]').click();
    await page.locator(".verification-live ol").waitFor();
    assert.equal(requests[0].id, "pending");
    assert.match(
      await page.locator(".verification-live li").first().innerText(),
      /阶段 17/,
    );
    // 保留 DOM 身份与滚动，检测无变更轮询是否拆掉整块面板。
    await page.evaluate(() => {
      clearInterval();
      window.qaRow = document.querySelector(".bulk-results tbody tr");
      window.qaList = document.querySelector(".verification-live ol");
      window.qaEntry = qaList.firstElementChild;
      qaList.scrollTop = 100;
      window.qaScroll = qaList.scrollTop;
      window.qaMutations = [];
      window.qaObserver = new MutationObserver((records) =>
        qaMutations.push(...records),
      );
      qaObserver.observe(document.querySelector("#modal-body"), {
        subtree: true,
        childList: true,
      });
    });
    await page.evaluate(async () => {
      for (let i = 0; i < 4; i++) await refresh(true);
    });
    assert.equal(await page.evaluate(() => qaMutations.length), 0);
    assert.equal(
      await page.evaluate(
        () =>
          qaRow === document.querySelector(".bulk-results tbody tr") &&
          qaList === document.querySelector(".verification-live ol") &&
          qaEntry.isConnected &&
          qaList.scrollTop === qaScroll,
      ),
      true,
    );
    progress.push({
      at: "2026-10-23T00:01:00+08:00",
      summary: "最新证据已返回 <script>window.badProgress=true</script>",
    });
    revision++;
    await page.evaluate(() => refresh(true));
    assert.match(
      await page.locator(".verification-live li").first().innerText(),
      /最新证据/,
    );
    assert.equal(
      await page.evaluate(
        () =>
          qaRow === document.querySelector(".bulk-results tbody tr") &&
          qaList === document.querySelector(".verification-live ol") &&
          qaEntry.isConnected,
      ),
      true,
    );
    assert.equal(await page.evaluate(() => window.badProgress), undefined);
    await page.locator('.modal-foot [data-action="close-modal"]').click();
    assert.equal(
      await page
        .locator('tr[data-select="pending"] [data-action="verify-item"]')
        .isEnabled(),
      true,
    );
    assert.equal(
      await page
        .locator('tr[data-select="failed"] [data-action="retry-verify"]')
        .innerText(),
      "重试",
    );
    assert.deepEqual(errors, []);
    console.log(
      "UI PASS: 待验证行直接启动、最新进展在前、连续轮询无节点移除、更新保留节点及滚动、文本安全转义。",
    );
  } finally {
    await page.unrouteAll({ behavior: "ignoreErrors" });
    await browser.close();
  }
})().catch((error) => {
  console.error(
    String(error.message).replace(
      /(x-release-token:\s*)[^\s'"\]]+/gi,
      "$1[redacted]",
    ),
  );
  process.exitCode = 1;
});
