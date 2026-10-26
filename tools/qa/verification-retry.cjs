// 只访问隔离测试服务；模拟 Codex 与验证状态，不查询生产日志或消耗账号额度。
const { chromium } = require("playwright");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");

(async () => {
  const browser = await chromium.launch({ channel: "msedge", headless: true });
  const page = await browser.newPage({
    viewport: { width: 1440, height: 960 },
  });
  const errors = [],
    requests = [];
  const version = "qa-verification-retry";
  const items = ["证据不足", "失败", "通过", "待验证"].map((status, index) => ({
    id: `verify-${index}`,
    kind: "verify",
    action: "修改",
    status: "待处理",
    service: "qa-service",
    verify_state: status,
    verify_runs: [{ status, summary: `历史结论 ${index}` }],
    data: {
      title: `测试事项 ${index}`,
      change: "验证改动的实际业务效果",
      result: `历史结论 ${index}`,
    },
  }));
  let busy = false,
    revision = 100,
    single = null;
  page.on("pageerror", (error) => errors.push(error.message));
  // 保留真实应用元数据，只用虚构版本替换清单，避免修改任何已有发布记录。
  await page.route("**/api/state", async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    Object.assign(body.data, {
      versions: [{ version: { name: version, status: "计划中" }, items }],
      revision,
      ai_busy: busy,
      single_verify: single,
      verify_queue: null,
      scan: null,
      batch: null,
      sql_queue: null,
    });
    await route.fulfill({ response, json: body });
  });
  await page.route("**/api/ai/verify", async (route) => {
    const request = route.request().postDataJSON();
    requests.push(request);
    busy = true;
    single = {
      id: "qa-retry",
      version,
      item_id: request.id,
      running: true,
      result: { status: "验证中" },
    };
    revision++;
    await route.fulfill({ json: { data: null } });
  });
  try {
    await page.goto(process.argv[2]);
    await page.locator(`#side-versions [data-name="${version}"]`).click();
    await page.locator('[data-action="tab"][data-kind="verify"]').click();
    const insufficient = page.locator('tr[data-select="verify-0"]');
    const failed = page.locator('tr[data-select="verify-1"]');
    // 两种非通过结论均可逐项重试；通过和未验证项不增加多余按钮。
    await insufficient
      .locator('[data-action="retry-verify"]')
      .waitFor({ timeout: 2000 });
    assert.equal(
      await failed.locator('[data-action="retry-verify"]').count(),
      1,
    );
    assert.equal(
      await page
        .locator('tr[data-select="verify-2"] [data-action="retry-verify"]')
        .count(),
      0,
    );
    assert.equal(
      await page
        .locator('tr[data-select="verify-3"] [data-action="retry-verify"]')
        .count(),
      0,
    );
    assert.match(
      await failed
        .locator('[data-action="retry-verify"]')
        .getAttribute("title"),
      /历史/,
    );
    const output = path.resolve(__dirname, "../../.work/qa/artifacts");
    fs.mkdirSync(output, { recursive: true });
    await page.screenshot({
      path: path.join(output, "verification-retry.png"),
    });
    // 最小窗口宽度下，操作不能被单元格裁剪，也不能互相遮挡。
    await page.setViewportSize({ width: 1100, height: 740 });
    const actionBounds = await failed
      .locator("td:last-child")
      .evaluate((cell) => {
        const buttons = [...cell.querySelectorAll("button")].map((button) =>
          button.getBoundingClientRect(),
        );
        const bounds = cell.getBoundingClientRect();
        return (
          buttons.every(
            (button) =>
              button.left >= bounds.left && button.right <= bounds.right,
          ) && buttons[0].right <= buttons[1].left
        );
      });
    assert.ok(actionBounds, "最小窗口下的编辑与重试应完整并排显示");
    await page.setViewportSize({ width: 1440, height: 960 });
    await failed.locator('[data-action="retry-verify"]').click();
    await page
      .getByRole("heading", { name: "重试 AI 验证", exact: true })
      .waitFor();
    assert.match(await page.locator("#modal-body").innerText(), /测试事项 1/);
    assert.match(await page.locator("#modal-body").innerText(), /历史/);
    assert.equal(requests.length, 0);
    // 取消不能消耗一次验证；再次打开允许调整时间窗口。
    await page.locator('.modal-foot [data-action="close-modal"]').click();
    assert.equal(requests.length, 0);
    await failed.locator('[data-action="retry-verify"]').click();
    await page.locator('[name="verify-start"]').fill("2026-10-22T10:00");
    await page.locator('[name="verify-end"]').fill("2026-10-22T11:00");
    // 模拟后台重绘改变选中行，确认请求仍绑定用户原先点击的事项。
    await page.evaluate(() => {
      selectedID = "verify-0";
    });
    await page.locator('[data-action="start-verify"]').click();
    await page.waitForFunction(() => state.single_verify?.running);
    assert.deepEqual(requests, [
      {
        version,
        id: "verify-1",
        start: "2026-10-22T10:00",
        end: "2026-10-22T11:00",
      },
    ]);
    await page.locator('.modal-foot [data-action="close-modal"]').click();
    assert.equal(
      await insufficient.locator('[data-action="retry-verify"]').isDisabled(),
      true,
    );
    assert.equal(
      await failed.locator('[data-action="retry-verify"]').isDisabled(),
      true,
    );
    // 独立结果回填后解除占用；成功项收起重试入口，其他项继续支持重试。
    busy = false;
    single.running = false;
    single.result = {
      status: "通过",
      summary: "补证后目标已达到",
      evidence: ["模拟代码与日志依据"],
    };
    items[1].verify_runs.push(single.result);
    items[1].verify_state = "通过";
    revision++;
    await page.evaluate(() => refresh(true));
    assert.equal(
      await failed.locator('[data-action="retry-verify"]').count(),
      0,
    );
    assert.equal(
      await insufficient.locator('[data-action="retry-verify"]').isEnabled(),
      true,
    );
    assert.equal(
      await page.evaluate(() => versionData().items[1].verify_runs.length),
      2,
    );
    assert.deepEqual(errors, []);
    console.log(
      "UI PASS: 证据不足/失败逐行重试、取消不调用、时间可调、目标绑定与忙碌禁用、完成后恢复；模型调用全部模拟。",
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
