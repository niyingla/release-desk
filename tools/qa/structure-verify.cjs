// SQL 计划和确认接口均拦截；不连接数据库、不执行 DDL 或初始化数据。
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
    requests = [];
  let batch = null,
    revision = 5000;
  page.on("pageerror", (error) => errors.push(error.message));
  try {
    await page.goto(process.argv[2]);
    await page
      .getByRole("heading", { name: "版本总览", exact: true })
      .waitFor();
    await page.evaluate(async () => {
      await api("version/save", { name: "qa-structure", status: "计划中" });
      await api("item/save", {
        version: "qa-structure",
        item: {
          id: "sql-order",
          kind: "sql",
          data: {
            name: "AFTER 字段顺序核验",
            side: "租户端",
            sql: "ALTER TABLE demo ADD source_type int AFTER store_id;",
          },
        },
      });
      await refresh(true);
    });
    await page.route("**/api/state", async (route) => {
      const response = await route.fetch();
      const body = await response.json();
      if (batch) {
        body.data.batch = batch;
        body.data.history = batch.rows;
        body.data.sql_connections = { uat: "mock-uat.invalid:3306" };
        body.data.revision = revision++;
      }
      await route.fulfill({ response, json: body });
    });
    await page.route("**/api/db/plan", (route) => {
      const request = route.request().postDataJSON();
      requests.push(request);
      assert.equal(request.verify_only, true);
      assert.equal(request.environment, "uat");
      return route.fulfill({
        json: {
          data: {
            token: "readonly-token",
            environment: "uat",
            verify_only: true,
            side: "租户端",
            server: "mock-uat.invalid:3306",
            databases: ["newlink", "newlink1"],
            schema_check_only: ["newlink", "newlink1"],
            skipped: [],
            sql: "ALTER TABLE demo ADD source_type int AFTER store_id;",
          },
        },
      });
    });
    await page.route("**/api/db/execute", async (route) => {
      const request = route.request().postDataJSON();
      requests.push(request);
      assert.deepEqual(request, { token: "readonly-token", confirm: true });
      const digest = await page.evaluate(
        () => selectedItem().data.content_digest,
      );
      batch = {
        id: "mock-structure",
        environment: "uat",
        version: "qa-structure",
        item_id: "sql-order",
        running: false,
        rows: ["newlink", "newlink1"].map((database) => ({
          verification_only: true,
          schema_only: true,
          environment: "uat",
          version: "qa-structure",
          item_id: "sql-order",
          digest,
          database,
          server: "mock-uat.invalid:3306",
          state: "成功",
          detail: "只读结构核验通过 · 未执行 DDL 或数据语句",
        })),
      };
      await route.fulfill({ json: { data: null } });
    });
    await page.locator('#side-versions [data-name="qa-structure"]').click();
    await page.locator('[data-action="tab"][data-kind="sql"]').click();
    await page.locator("#sql-environment").selectOption("uat");
    const button = page.locator('[data-action="check-structure"]');
    assert.match(await button.getAttribute("title"), /只读/);
    await button.click();
    await page
      .getByRole("heading", { name: "确认UAT 环境只读结构核验" })
      .waitFor();
    assert.match(
      await page.locator("#modal-body").innerText(),
      /不会发送此脚本中的 DDL 或数据语句/,
    );
    assert.equal(
      await page.locator('[data-action="confirm-execute"]').isDisabled(),
      true,
    );
    assert.equal(requests.length, 1);
    const output = path.resolve(__dirname, "../../.work/qa/artifacts");
    fs.mkdirSync(output, { recursive: true });
    await page.screenshot({
      path: path.join(output, "structure-check-confirm.png"),
      fullPage: true,
    });
    await page.locator("#execute-understood").check();
    await page.locator('[data-action="confirm-execute"]').click();
    await page
      .getByRole("heading", { name: "UAT 环境只读结构核验", exact: true })
      .waitFor();
    assert.match(
      await page.locator("#modal-body").innerText(),
      /未执行 DDL 或数据语句/,
    );
    await page.locator('.modal-foot [data-action="close-modal"]').click();
    assert.match(
      await page.locator('tr[data-select="sql-order"]').innerText(),
      /结构核验通过/,
    );
    await page.screenshot({
      path: path.join(output, "structure-check-result.png"),
      fullPage: true,
    });
    assert.deepEqual(errors, []);
    console.log(
      "UI PASS: 只读结构核验入口、环境绑定、目标预览、确认勾选、只读任务文案和独立结构状态；执行全部模拟。",
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
