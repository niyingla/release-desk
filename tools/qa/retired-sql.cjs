// 只使用隔离版本与模拟历史；拦截所有数据库操作，回归不会访问真实连接。
const { chromium } = require("playwright");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");

(async () => {
  const browser = await chromium.launch({ channel: "msedge", headless: true });
  const page = await browser.newPage({
    viewport: { width: 1600, height: 1050 },
  });
  const errors = [],
    databaseRequests = [],
    copies = [];
  page.on("pageerror", (error) => errors.push(error.message));
  for (const endpoint of ["plan", "execute", "batch/plan", "batch/execute"])
    await page.route("**/api/db/" + endpoint, (route) => {
      databaseRequests.push(endpoint);
      return route.fulfill({ json: { error: "QA 不允许访问数据库" } });
    });
  await page.route("**/api/clipboard/copy", (route) => {
    copies.push(route.request().postDataJSON());
    return route.fulfill({ json: { data: null } });
  });
  try {
    await page.goto(process.argv[2]);
    await page
      .getByRole("heading", { name: "版本总览", exact: true })
      .waitFor();
    await page.evaluate(async () => {
      await api("version/save", { name: "qa-retired", status: "计划中" });
      for (const item of [
        { id: "active", name: "正常初始化" },
        { id: "legacy", name: "历史初始化【已退役，勿执行】" },
        { id: "explicit", name: "停用的数据修正", lifecycle: "已退役" },
      ])
        await api("item/save", {
          version: "qa-retired",
          item: {
            id: item.id,
            kind: "sql",
            data: {
              name: item.name,
              lifecycle: item.lifecycle || "",
              side: "服务商端",
              sql: "INSERT INTO demo(id) VALUES(1);",
            },
          },
        });
    });
    // 历史成功和失败不能重新赋予执行资格；仅在界面回显模拟审计。
    await page.route("**/api/state", async (route) => {
      const response = await route.fetch();
      const body = await response.json();
      const version = body.data.versions.find(
        (v) => v.version.name === "qa-retired",
      );
      body.data.sql_connections.test = "qa-history.invalid:3306";
      body.data.history = version.items
        .filter((item) => item.id !== "active")
        .map((item) => ({
          environment: "test",
          version: "qa-retired",
          item_id: item.id,
          digest: item.data.content_digest,
          server: "qa-history.invalid:3306",
          database: "sp",
          state: item.id === "legacy" ? "失败" : "成功",
          detail: "退役前的历史结果",
        }));
      await route.fulfill({ response, json: body });
    });
    await page.evaluate(() => refresh(true));
    await page.locator('#side-versions [data-name="qa-retired"]').click();
    await page.locator('[data-action="tab"][data-kind="sql"]').click();
    for (const id of ["legacy", "explicit"]) {
      const row = page.locator(`tr[data-select="${id}"]`);
      assert.equal(
        await row.locator('input[type="checkbox"]').isDisabled(),
        true,
        "退役项仍可勾选",
      );
      assert.match(await row.innerText(), /已退役/);
      await row.click();
      assert.equal(
        await page
          .locator(
            '#detail [data-action="execute"]:enabled, #detail [data-action="retry"]:enabled, #detail [data-action="check-structure"]:enabled',
          )
          .count(),
        0,
        "退役项仍可启动新任务",
      );
      assert.equal(
        await page.locator('#detail [data-action="history"]').isVisible(),
        true,
      );
      await page.locator('#detail [data-action="copy-detail"]').click();
    }
    assert.equal(copies.length, 2);
    assert.match(copies[0].text, /INSERT INTO demo/);
    await page.locator('[data-action="bulk-select-all"]').click();
    assert.deepEqual(
      await page
        .locator("[data-bulk-check]:checked")
        .evaluateAll((boxes) => boxes.map((b) => b.dataset.bulkCheck)),
      ["active"],
    );
    await page.locator('tr[data-select="active"]').click();
    assert.equal(
      await page.locator('#detail [data-action="execute"]').isEnabled(),
      true,
    );
    // 外部更新将已选项退役后，旧勾选立即失效，不等待用户手动取消。
    await page.evaluate(async () => {
      const item = structuredClone(
        versionData().items.find((i) => i.id === "active"),
      );
      item.data.lifecycle = "已退役";
      await api("item/save", { version: "qa-retired", item });
      await refresh(true);
    });
    assert.equal(await page.locator("[data-bulk-check]:checked").count(), 0);
    assert.equal(
      await page.locator('[data-action="bulk-execute"]').isDisabled(),
      true,
    );
    assert.deepEqual(databaseRequests, []);
    assert.deepEqual(errors, []);
    const output = path.resolve(".work/qa/retired-sql");
    fs.mkdirSync(output, { recursive: true });
    await page.screenshot({
      path: path.join(output, "retired.png"),
      fullPage: true,
    });
    console.log(
      "PASS：退役 SQL 禁选、全选跳过、执行入口禁用、旧勾选清理；保留复制和历史，未请求数据库。",
    );
  } finally {
    await page.unrouteAll({ behavior: "ignoreErrors" });
    await browser.close();
  }
})().catch((error) => {
  console.error(error.message);
  process.exitCode = 1;
});
