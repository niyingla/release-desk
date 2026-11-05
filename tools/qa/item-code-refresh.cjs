// 单项更新确认与范围绑定的界面回归；仅模拟任务端点，不调用 Codex 或远端 Git。
const { chromium } = require("playwright");
const assert = require("node:assert/strict");

(async () => {
  const browser = await chromium.launch({ channel: "msedge", headless: true });
  const page = await browser.newPage({
    viewport: { width: 1440, height: 960 },
  });
  const errors = [],
    requests = [];
  const version = "qa-code-refresh";
  let scan = null;
  const items = [false, true].map((manual, i) => ({
    id: `verify-${i}`,
    kind: "verify",
    action: "修改",
    status: "待处理",
    verify_state: manual ? "需手动验证" : "待验证",
    data: {
      title: manual ? "人工验证事项" : "自动验证事项",
      change: "原代码说明",
      verification_mode: manual ? "手动验证" : "自动验证",
      manual_reason: manual ? "没有结果日志" : "",
    },
  }));
  page.on("pageerror", (error) => errors.push(error.message));
  await page.route("**/api/state", async (route) => {
    const response = await route.fetch(),
      body = await response.json();
    Object.assign(body.data, {
      versions: [{ version: { name: version, status: "计划中" }, items }],
      revision: scan ? 1802 : 1801,
      ai_busy: Boolean(scan),
      scan,
      single_verify: null,
      verify_queue: null,
      sql_queue: null,
      batch: null,
    });
    await route.fulfill({ response, json: body });
  });
  await page.route("**/api/ai/item/refresh", async (route) => {
    const request = route.request().postDataJSON();
    requests.push(request);
    scan = {
      id: "qa-task",
      version,
      item_id: request.id,
      item_title: "自动验证事项",
      running: true,
      phase: "读取指定版本最新代码引用",
      started: "2026-11-03 10:00:00",
      counts: {},
      warnings: [],
    };
    await route.fulfill({ json: { data: null } });
  });
  try {
    await page.goto(process.argv[2]);
    await page.locator(`#side-versions [data-name="${version}"]`).click();
    await page.locator('[data-action="tab"][data-kind="verify"]').click();
    assert.equal(
      await page.locator('[data-action="refresh-item-code"]').count(),
      2,
    );
    await page
      .locator('[data-action="refresh-item-code"][data-id="verify-1"]')
      .click();
    assert.match(await page.locator("#modal-body").innerText(), /人工验证事项/);
    await page.locator('[data-action="close-modal"]').first().click();
    assert.equal(requests.length, 0, "取消确认仍然启动了任务");
    await page
      .locator('[data-action="refresh-item-code"][data-id="verify-0"]')
      .click();
    assert.equal(await page.locator("#refresh-code-remote").count(), 0);
    assert.match(
      await page.locator("#modal-body").innerText(),
      /只读本地 Git 引用/,
    );
    // 选择别的行不能改变确认弹窗已绑定的更新目标。
    await page.evaluate(() => {
      selectedID = "verify-1";
    });
    await page.locator('[data-action="confirm-item-code-refresh"]').click();
    await page.waitForFunction(() => modalType === "scan-running");
    assert.deepEqual(requests, [{ version, id: "verify-0" }]);
    assert.match(
      await page.locator(".modal-head").innerText(),
      /更新代码.*自动验证事项/,
    );
    await page.locator('[data-action="close-modal"]').first().click();
    for (const button of await page
      .locator('[data-action="refresh-item-code"]')
      .all())
      assert.equal(await button.isDisabled(), true);
    assert.deepEqual(errors, []);
    console.log(
      "PASS: 每行更新入口、确认取消、只读本地引用、目标绑定及独立进度。",
    );
  } finally {
    await browser.close();
  }
})().catch((error) => {
  console.error(error.stack);
  process.exitCode = 1;
});
