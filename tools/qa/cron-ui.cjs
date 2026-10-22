// 使用独立 --data-dir 测试真实表单和文件，不运行调度器、SQL 或 AI。
const { chromium } = require("playwright");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");

(async () => {
  const browser = await chromium.launch({ channel: "msedge", headless: true });
  const page = await browser.newPage({
    viewport: { width: 1440, height: 1000 },
  });
  const errors = [];
  const remoteRequests = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("request", (request) => {
    if (!request.url().startsWith(new URL(process.argv[2]).origin))
      remoteRequests.push(request.url());
  });
  try {
    await page.goto(process.argv[2]);
    await page
      .getByRole("heading", { name: "版本总览", exact: true })
      .waitFor();
    await page.evaluate(async () => {
      await api("version/save", { name: "qa-cron", status: "计划中" });
      await api("item/save", {
        version: "qa-cron",
        item: {
          id: "dailyReport",
          kind: "job",
          action: "新增",
          service: "qa-job-service",
          data: {
            handler: "dailyReport",
            frequency: "0 0 10 * * ?（源码注明每天10点；Admin实际配置待核对）",
            description: "报告业务说明",
          },
        },
      });
      await refresh(true);
    });
    await page.locator('#side-versions [data-name="qa-cron"]').click();
    const cell = page.locator(
      'tr[data-select="dailyReport"] td[data-copy="0 0 10 * * ?"]',
    );
    assert.equal(await cell.innerText(), "0 0 10 * * ?（每天 10:00）");
    await cell.hover();
    await page.locator(".cell-preview:not([hidden])").waitFor();
    assert.equal(
      await page.locator(".cell-preview-content").innerText(),
      "0 0 10 * * ?（每天 10:00）",
    );
    await page.locator('#detail [data-action="edit-item"]').click();
    const input = page.locator("#job-cron");
    assert.equal(await input.inputValue(), "0 0 10 * * ?");
    assert.equal(await page.locator("#cron-preview").innerText(), "每天 10:00");
    const description = await page
      .locator('#item-form [name="description"]')
      .inputValue();
    assert.match(description, /报告业务说明/);
    assert.match(description, /Admin实际配置待核对/);

    // 每次输入立即更新，错误不能继续显示上一个成功解释。
    for (const [expression, expected] of [
      ["0 0/5 * * * ?", /每隔 5 分钟/],
      ["0 0 3 ? * 2", /星期一/],
      ["0 3 * * 1", /星期一/],
      ["0 0 10 * * ? 2027", /2027/],
      ["0 99 10 * * ?", /无法解析/],
      ["", /未填写 Cron/],
    ]) {
      await input.fill(expression);
      assert.match(await page.locator("#cron-preview").innerText(), expected);
    }
    await input.fill("0 0/0 * * * ?");
    assert.equal(await input.getAttribute("aria-invalid"), "true");
    await page.locator('[data-action="save-item"]').click();
    await page
      .getByText("Cron 无法解析；频率只填写表达式，其他说明请放到描述中", {
        exact: true,
      })
      .waitFor();
    assert.equal(await input.isVisible(), true);

    const output = path.resolve(__dirname, "../../.work/qa/artifacts");
    fs.mkdirSync(output, { recursive: true });
    await input.fill("0 0 10 * * ?");
    assert.equal(await input.getAttribute("aria-invalid"), "false");
    await page.screenshot({
      path: path.join(output, "cron-edit.png"),
      fullPage: true,
    });

    // 编辑后只写表达式；未知来源字段和迁移后的业务描述继续保留。
    await input.fill("0 0/5 * * * ?");
    await page.locator('[data-action="save-item"]').click();
    await page.locator("#item-form").waitFor({ state: "detached" });
    await page.reload();
    await page.locator('#side-versions [data-name="qa-cron"]').click();
    const row = page.locator('tr[data-select="dailyReport"]');
    assert.match(await row.innerText(), /0 0\/5 \* \* \* \?（每隔 5 分钟）/);
    assert.equal(await row.locator('td[data-copy="0 0/5 * * * ?"]').count(), 1);
    await page.screenshot({
      path: path.join(output, "cron-table.png"),
      fullPage: true,
    });
    if (process.argv[3]) {
      const saved = JSON.parse(
        fs.readFileSync(
          path.join(process.argv[3], "版本文件/qa-cron.json"),
          "utf8",
        ),
      );
      assert.equal(saved.items[0].data.frequency, "0 0/5 * * * ?");
      assert.equal(saved.items[0].data.description, description);
    }
    assert.deepEqual(errors, []);
    assert.deepEqual(remoteRequests, []);
    console.log(
      "UI PASS: 纯 Cron、列表括号解释、实时预览、无效输入保护、旧备注保留、复制值、刷新持久化、离线资源。",
    );
  } finally {
    await page.close();
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
