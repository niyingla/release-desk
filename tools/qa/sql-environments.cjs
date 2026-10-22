// 在独立 --data-dir 服务上测试；计划与执行均拦截，不连接真实数据库。
const { chromium } = require("playwright");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");

(async () => {
  const browser = await chromium.launch({ channel: "msedge", headless: true });
  const page = await browser.newPage({
    viewport: { width: 1600, height: 1100 },
  });
  const errors = [],
    plans = [],
    executions = [];
  let batch = null,
    digest = "",
    revision = 10000;
  page.on("pageerror", (error) => errors.push(error.message));
  try {
    await page.goto(process.argv[2]);
    await page
      .getByRole("heading", { name: "版本总览", exact: true })
      .waitFor();
    await page.evaluate(async () => {
      await api("version/save", { name: "qa-environments", status: "计划中" });
      await api("item/save", {
        version: "qa-environments",
        item: {
          id: "sql-env",
          kind: "sql",
          action: "新增",
          data: {
            name: "环境隔离回归",
            side: "服务商端",
            sql: "INSERT INTO demo(id) VALUES(1);",
          },
        },
      });
      await refresh(true);
    });
    await page.route("**/api/state", async (route) => {
      const response = await route.fetch();
      const body = await response.json();
      const item = body.data.versions.find(
        (v) => v.version.name === "qa-environments",
      ).items[0];
      digest = item.data.content_digest;
      // 相同服务器和库名，只有环境不同；旧缺省环境属于测试。
      const common = {
        version: "qa-environments",
        item_id: "sql-env",
        database: "sp",
        server: "same.invalid:3306",
        digest,
        started: "2026-10-19 10:00:00",
      };
      body.data.history = [
        { ...common, state: "成功", detail: "仅测试的旧审计" },
        { ...common, environment: "uat", state: "失败", detail: "仅UAT的审计" },
      ];
      body.data.sql_connections = { test: common.server, uat: common.server };
      body.data.batch = batch;
      body.data.revision = revision++;
      await route.fulfill({ response, json: body });
    });
    const makePlan = (environment) => ({
      environment,
      token: `${environment}-single-token`,
      version: "qa-environments",
      item_id: "sql-env",
      digest,
      server: `${environment}.invalid:3306`,
      sql: "INSERT INTO demo(id) VALUES(1);",
      side: "服务商端",
      databases: ["sp", "sp1"],
      skipped: [],
    });
    await page.route("**/api/db/plan", (route) => {
      const request = route.request().postDataJSON();
      plans.push(request);
      return route.fulfill({ json: { data: makePlan(request.environment) } });
    });
    await page.route("**/api/db/execute", (route) => {
      executions.push(route.request().postDataJSON());
      batch = {
        id: "uat-batch",
        environment: "uat",
        version: "qa-environments",
        item_id: "sql-env",
        running: false,
        rows: [
          {
            environment: "uat",
            database: "sp",
            state: "成功",
            detail: "模拟结果",
            started: "2026-10-19 10:00:00",
          },
        ],
      };
      return route.fulfill({ json: { data: null } });
    });
    await page.route("**/api/db/batch/plan", (route) => {
      const request = route.request().postDataJSON();
      plans.push(request);
      return route.fulfill({
        json: {
          data: {
            environment: request.environment,
            token: "uat-bulk-token",
            version: "qa-environments",
            items: [
              {
                name: "环境隔离回归",
                phase: "初始化数据",
                plan: makePlan(request.environment),
              },
            ],
          },
        },
      });
    });
    await page.evaluate(() => refresh(true));
    await page.locator('#side-versions [data-name="qa-environments"]').click();
    await page.locator('[data-action="tab"][data-kind="sql"]').click();
    assert.equal(await page.locator("#sql-environment").inputValue(), "test");
    const row = page.locator('tr[data-select="sql-env"]');
    assert.match(await row.innerText(), /成功/);
    await page.locator("#sql-environment").selectOption("uat");
    assert.match(await row.innerText(), /部分失败/);
    await page.locator('[data-action="history"]').click();
    await page.getByRole("heading", { name: "UAT 环境执行明细" }).waitFor();
    assert.match(await page.locator("#modal-body").innerText(), /仅UAT的审计/);
    assert.doesNotMatch(
      await page.locator("#modal-body").innerText(),
      /仅测试的旧审计/,
    );
    await page.locator('.modal-foot [data-action="close-modal"]').click();

    // UAT 单项与重试均携带所选环境，确认框仍默认未勾选。
    await page.locator('[data-action="execute"]').click();
    await page.getByRole("heading", { name: "确认UAT 环境执行" }).waitFor();
    assert.equal(plans.at(-1).environment, "uat");
    assert.match(
      await page.locator("#modal-body").innerText(),
      /uat\.invalid:3306/,
    );
    assert.equal(
      await page.locator('[data-action="confirm-execute"]').isDisabled(),
      true,
    );
    await page.locator('.modal-foot [data-action="close-modal"]').click();
    assert.equal(executions.length, 0);
    await page.locator('[data-action="retry"]').click();
    await page.getByRole("heading", { name: "确认UAT 环境执行" }).waitFor();
    assert.equal(plans.at(-1).retry, true);
    assert.equal(plans.at(-1).environment, "uat");
    await page.locator("#execute-understood").check();
    await page.locator('[data-action="confirm-execute"]').click();
    await page.getByRole("heading", { name: "UAT 环境逐库执行" }).waitFor();
    assert.deepEqual(executions, [
      { token: "uat-single-token", confirm: true },
    ]);
    await page.locator('.modal-foot [data-action="close-modal"]').click();
    await page.locator('[data-action="bulk-select-all"]').click();
    await page.locator('[data-action="bulk-execute"]').click();
    await page
      .getByRole("heading", { name: "确认批量执行UAT 环境 SQL" })
      .waitFor();
    assert.equal(plans.at(-1).environment, "uat");
    assert.deepEqual(plans.at(-1).ids, ["sql-env"]);
    assert.equal(
      await page.locator('[data-action="bulk-confirm-sql"]').isDisabled(),
      true,
    );
    const output = path.resolve(__dirname, "../../.work/qa/artifacts");
    fs.mkdirSync(output, { recursive: true });
    await page.screenshot({
      path: path.join(output, "uat-preview.png"),
      fullPage: true,
    });
    await page.locator('.modal-foot [data-action="close-modal"]').click();

    // 设置分别展示两套字段，不回填密码；仅修改配置，不尝试网络连接。
    await page.locator('[data-action="settings"]').click();
    await page.locator('[name="db_test_host"]').fill("fake-test.invalid");
    await page.locator('[name="db_test_user"]').fill("qa-test");
    await page.locator('[name="db_test_password"]').fill("fake-test-password");
    await page.locator('[name="db_uat_host"]').fill("fake-uat.invalid");
    await page.locator('[name="db_uat_user"]').fill("qa-uat");
    await page.locator('[name="db_uat_password"]').fill("fake-uat-password");
    await page.locator('[data-action="save-settings"]').click();
    await page.locator("#settings-form").waitFor({ state: "detached" });
    await page.locator('[data-action="settings"]').click();
    assert.equal(
      await page.locator('[name="db_test_host"]').inputValue(),
      "fake-test.invalid",
    );
    assert.equal(
      await page.locator('[name="db_uat_host"]').inputValue(),
      "fake-uat.invalid",
    );
    assert.equal(
      await page.locator('[name="db_test_password"]').inputValue(),
      "",
    );
    assert.equal(
      await page.locator('[name="db_uat_password"]').inputValue(),
      "",
    );
    await page.screenshot({
      path: path.join(output, "uat-settings.png"),
      fullPage: true,
    });
    await page.reload();
    await page.locator('#side-versions [data-name="qa-environments"]').click();
    await page.locator('[data-action="tab"][data-kind="sql"]').click();
    assert.equal(await page.locator("#sql-environment").inputValue(), "test");
    assert.deepEqual(errors, []);
    console.log(
      "UI PASS: 默认测试、UAT 切换、状态与明细隔离、单项及重试参数、确认令牌、批量预览、独立加密设置、密码不回填、刷新恢复测试；数据库执行全部模拟。",
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
