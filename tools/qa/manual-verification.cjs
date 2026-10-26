// 使用虚构事项覆盖人工禁选、尾置、确认标签和时间窗口；不调用模型或生产服务。
const { chromium } = require("playwright");
const assert = require("node:assert/strict");

(async () => {
  const browser = await chromium.launch({ channel: "msedge", headless: true });
  const page = await browser.newPage({
    viewport: { width: 1440, height: 960 },
  });
  const errors = [],
    version = "qa-manual-policy";
  const make = (id, manual, state = "待验证", result = null) => ({
    id,
    kind: "verify",
    action: "修改",
    status: "待处理",
    verify_state: manual ? "需手动验证" : state,
    verify_runs: result ? [result] : [],
    data: {
      title: id,
      change: "QA 核心目标",
      verification_mode: manual ? "手动验证" : "自动验证",
      manual_reason: manual ? "没有可观察结果日志，需核对页面" : "",
      steps: "QA 验证步骤",
    },
  });
  const items = [
    make("manual-a", true),
    make("auto-pending", false),
    make("auto-neutral", false, "未发现异常", {
      status: "未发现异常",
      confirmation: "未确认",
      log_query_succeeded: true,
      summary: "QA 已查询，无适用异常，仍未确认目标",
    }),
    make("manual-b", true),
    make("auto-probable", false, "通过", {
      status: "通过",
      confirmation: "高概率确认",
      summary: "QA 间接证据确认",
    }),
    make("auto-blocked", false, "证据不足", {
      status: "证据不足",
      blocker: "取证受阻",
      summary: "QA 网络失败",
    }),
  ];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.route("**/api/state", async (route) => {
    const response = await route.fetch(),
      body = await response.json();
    Object.assign(body.data, {
      versions: [{ version: { name: version, status: "计划中" }, items }],
      revision: 810,
      ai_busy: false,
      single_verify: null,
      verify_queue: null,
      scan: null,
      batch: null,
      sql_queue: null,
    });
    await route.fulfill({ response, json: body });
  });
  try {
    await page.goto(process.argv[2]);
    await page.locator(`#side-versions [data-name="${version}"]`).click();
    await page.locator('[data-action="tab"][data-kind="verify"]').click();
    const rows = page.locator("tr[data-select]");
    assert.deepEqual(
      await rows.evaluateAll((nodes) => nodes.map((n) => n.dataset.select)),
      [
        "auto-pending",
        "auto-neutral",
        "auto-probable",
        "auto-blocked",
        "manual-a",
        "manual-b",
      ],
    );
    for (const id of ["manual-a", "manual-b"]) {
      const row = page.locator(`tr[data-select="${id}"]`);
      assert.equal(
        await row.locator('input[type="checkbox"]').isDisabled(),
        true,
      );
      assert.equal(
        await row.locator('[data-action="retry-verify"]').count(),
        0,
      );
      assert.equal(await row.locator('[data-action="verify-item"]').count(), 0);
    }
    await page.locator('[data-action="bulk-select-all"]').click();
    assert.match(await page.locator(".bulk-count").innerText(), /已选 4 项/);
    assert.equal(await page.locator("[data-bulk-check]:checked").count(), 4);
    assert.match(
      await page.locator('tr[data-select="auto-probable"]').innerText(),
      /通过（高概率）/,
    );
    assert.match(
      await page.locator('tr[data-select="auto-blocked"]').innerText(),
      /取证受阻/,
    );
    assert.equal(
      await page
        .locator('tr[data-select="auto-neutral"] [data-action="retry-verify"]')
        .count(),
      1,
    );
    assert.equal(
      await page.locator('tr[data-select="auto-neutral"] .badge.green').count(),
      0,
    );
    await page
      .locator('tr[data-select="auto-blocked"] [data-action="retry-verify"]')
      .click();
    const before = await page.locator('[name="verify-start"]').inputValue();
    await page.locator('[data-verification-range="verify"]').selectOption("24");
    const start = await page.locator('[name="verify-start"]').inputValue(),
      end = await page.locator('[name="verify-end"]').inputValue();
    assert.notEqual(start, before);
    assert.equal((Date.parse(end) - Date.parse(start)) / 3600000, 24);
    assert.deepEqual(errors, []);
    console.log(
      "PASS: manual rows last/disabled/excluded, probability/blocker labels and explicit time range.",
    );
  } finally {
    await browser.close();
  }
})().catch((error) => {
  console.error(error.message);
  process.exitCode = 1;
});
