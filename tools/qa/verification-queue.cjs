// 使用隔离服务及模拟验证接口，覆盖连续入队、并行状态、撤销与实时进展；不调用模型。
const { chromium } = require("playwright");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");

(async () => {
  const browser = await chromium.launch({ channel: "msedge", headless: true });
  const page = await browser.newPage({
    viewport: { width: 1440, height: 960 },
  });
  const version = "qa-independent-verification",
    errors = [],
    requests = [],
    cancellations = [];
  const items = ["失败", "证据不足", "失败", "通过"].map((status, index) => ({
    id: `core-${index}`,
    kind: "verify",
    action: "修改",
    status: "待处理",
    verify_state: status,
    verify_runs: [{ status, summary: "原有结论" }],
    data: {
      title: ["重复创建修复", "核心查询修复", "主要状态流转", "已验证业务"][
        index
      ],
      change: "关键业务改动，比较修复前后并核对线上日志",
      result: "原有结论",
    },
  }));
  let queue = null,
    revision = 100;
  page.on("pageerror", (error) => errors.push(error.message));
  const record = () => ({
    id: "qa-queue",
    kind: "verify",
    version,
    title: "AI 验证队列",
    status: queue?.running ? "运行中" : "已完成",
    running: Boolean(queue?.running),
    progress: "独立验证进度",
    started: "2026-10-23T00:00:00+08:00",
    rows: (queue?.rows || []).map((row) => ({
      id: row.id,
      title: row.title,
      state: row.state,
      progress: row.progress,
      condition: "对比修复前后代码，核对本项关键业务日志",
    })),
  });
  await page.route("**/api/state", async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    Object.assign(body.data, {
      versions: [{ version: { name: version, status: "计划中" }, items }],
      revision,
      verify_queue: queue,
      ai_busy: Boolean(queue?.running),
      single_verify: null,
      scan: null,
      batch: null,
      sql_queue: null,
      tasks: queue ? [{ ...record(), rows: undefined }] : [],
    });
    await route.fulfill({ response, json: body });
  });
  await page.route("**/api/ai/verify", async (route) => {
    const request = route.request().postDataJSON();
    requests.push(request);
    queue ||= {
      id: "qa-queue",
      version,
      running: true,
      concurrency: 2,
      rows: [],
    };
    const item = items.find((item) => item.id === request.id);
    queue.rows.push({
      id: `row-${queue.rows.length}`,
      version,
      item_id: item.id,
      title: item.data.title,
      state: queue.rows.length < 2 ? "验证中" : "等待中",
      detail: "正在核对核心业务逻辑",
      progress: [
        {
          at: "2026-10-23T00:00:01+08:00",
          summary: "已对比修复前后核心分支，正在检索线上关键日志",
        },
      ],
    });
    revision++;
    await route.fulfill({
      json: { data: { queue_id: queue.id, accepted: true } },
    });
  });
  await page.route("**/api/ai/verify/cancel-queued", async (route) => {
    const request = route.request().postDataJSON();
    cancellations.push(request);
    queue.rows.find((row) => row.id === request.row_id).state = "已取消";
    revision++;
    await route.fulfill({ json: { data: null } });
  });
  await page.route("**/api/task/detail", (route) =>
    route.fulfill({ json: { data: record() } }),
  );
  try {
    await page.goto(process.argv[2]);
    await page.locator(`#side-versions [data-name="${version}"]`).click();
    await page.locator('[data-action="tab"][data-kind="verify"]').click();
    const concurrency = page.locator("#verification-concurrency");
    assert.equal(await concurrency.inputValue(), "6");
    await concurrency.selectOption("2");
    await page.waitForFunction(
      () => state.verification_default_concurrency === 2,
    );
    // 每次加入后自动返回列表，其他行保持可点击，不强迫用户等待上一项结束。
    for (let index = 0; index < 3; index++) {
      await page
        .locator(`tr[data-select="core-${index}"] [data-action="retry-verify"]`)
        .click();
      await page.locator('[data-action="start-verify"]').click();
      await page.waitForFunction(
        (count) => state.verify_queue?.rows.length === count,
        index + 1,
      );
      assert.equal(await page.locator(".modal").count(), 0);
      if (index < 2)
        assert.equal(
          await page
            .locator(
              `tr[data-select="core-${index + 1}"] [data-action="retry-verify"]`,
            )
            .isEnabled(),
          true,
        );
    }
    assert.equal(requests.length, 3);
    assert.equal(await concurrency.isDisabled(), true);
    assert.equal(await concurrency.inputValue(), "2");
    assert.match(
      await page.locator('tr[data-select="core-2"]').innerText(),
      /等待中/,
    );
    const output = path.resolve(__dirname, "../../.work/qa/artifacts");
    fs.mkdirSync(output, { recursive: true });
    await page.screenshot({
      path: path.join(output, "independent-verification-queue.png"),
    });
    await page
      .locator('tr[data-select="core-2"] [data-action="cancel-queued-verify"]')
      .click();
    assert.deepEqual(cancellations, [
      { queue_id: "qa-queue", row_id: "row-2" },
    ]);
    assert.equal(requests.length, 3);
    assert.equal(
      await page
        .locator('tr[data-select="core-2"] [data-action="retry-verify"]')
        .isEnabled(),
      true,
    );
    // 任务详情顶部持续更新摘要，模型文字必须按普通文本展示。
    queue.rows[0].progress.push({
      at: "2026-10-23T00:00:02+08:00",
      summary:
        "线上日志已关联业务结果 <script>window.progressInjected=true</script>",
    });
    revision++;
    await page.evaluate(() => refresh(true));
    await page.locator('[data-action="tasks"]').first().click();
    await page
      .locator('[data-action="task-detail"][data-id="qa-queue"]')
      .click();
    await page.locator(".verification-current").waitFor();
    assert.match(
      await page.locator(".verification-current").innerText(),
      /线上日志已关联业务结果/,
    );
    assert.equal(await page.evaluate(() => window.progressInjected), undefined);
    queue.rows[0].progress.push({
      at: "2026-10-23T00:00:03+08:00",
      summary: "已确认旧分支成因与修复机制，正在核对日志是否反映新逻辑",
    });
    revision++;
    await page.evaluate(() => refresh(true));
    assert.match(
      await page.locator(".verification-current").innerText(),
      /修复机制/,
    );
    await page.screenshot({
      path: path.join(output, "verification-live-progress.png"),
    });
    // 全部结束后允许再次调整，原有验证记录不因取消排队被清除。
    queue.running = false;
    for (const row of queue.rows.filter((row) => row.state === "验证中"))
      row.state = "通过";
    revision++;
    await page.locator('.modal-foot [data-action="close-modal"]').click();
    await page.locator(`#side-versions [data-name="${version}"]`).click();
    await page.evaluate(() => refresh(true));
    assert.equal(await concurrency.isEnabled(), true);
    await concurrency.selectOption("6");
    await page.waitForFunction(
      () => state.verification_default_concurrency === 6,
    );
    assert.equal(items[2].verify_runs.length, 1);
    assert.deepEqual(errors, []);
    console.log(
      "UI PASS: 连续追加、两项并行与等待、单项取消、直接设置并发、实时摘要与任务回看、XSS转义；模型调用模拟。",
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
