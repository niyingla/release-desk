// 使用隔离 --data-dir 服务测试任务操作类型；不调用 XXL-Job 或 Codex。
const { chromium } = require("playwright");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");

(async () => {
  const browser = await chromium.launch({ channel: "msedge", headless: true });
  const page = await browser.newPage({
    viewport: { width: 1440, height: 960 },
  });
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  try {
    await page.goto(process.argv[2]);
    await page
      .getByRole("heading", { name: "版本总览", exact: true })
      .waitFor();
    await page.evaluate(async () => {
      await api("version/save", { name: "qa-job-actions", status: "计划中" });
      for (const [id, action] of [
        ["newJob", "新增"],
        ["changedJob", "修改"],
        ["deletedJob", "删除"],
      ]) {
        await api("item/save", {
          version: "qa-job-actions",
          item: {
            id,
            kind: "job",
            action,
            service: "qa-service",
            data: {
              handler: id,
              frequency: "0 0 * * * ?",
              description: "隔离测试事项",
            },
          },
        });
      }
      await refresh(true);
    });
    await page.locator('#side-versions [data-name="qa-job-actions"]').click();
    await page
      .getByRole("columnheader", { name: "操作类型", exact: true })
      .waitFor();
    assert.match(
      await page.locator('tr[data-select="newJob"]').innerText(),
      /新增/,
    );
    assert.match(
      await page.locator('tr[data-select="changedJob"]').innerText(),
      /修改/,
    );

    // 通过真实表单新增待关闭任务，线上状态仍是独立的待上线。
    await page.locator('[data-action="new-item"]').click();
    await page.locator('#item-form [name="action"]').selectOption("关闭");
    await page.locator('#item-form [name="handler"]').fill("closeLegacyJob");
    await page.locator('#item-form [name="frequency"]').fill("0 0 3 * * ?");
    await page
      .locator('#item-form [name="description"]')
      .fill("本版本停用旧任务调度，保留定义供核对。");
    await page.locator('[data-action="save-item"]').click();
    const closeRow = page
      .locator("tbody tr")
      .filter({ hasText: "closeLegacyJob" });
    await closeRow.getByText("关闭", { exact: true }).waitFor();
    assert.match(await closeRow.innerText(), /待上线/);
    assert.equal(await closeRow.locator('td[data-copy="关闭"]').count(), 1);
    await closeRow.click();
    assert.match(
      await page.locator(".detail-aside").innerText(),
      /操作类型[\s\S]*关闭/,
    );
    const output = path.resolve(__dirname, "../../.work/qa/artifacts");
    fs.mkdirSync(output, { recursive: true });
    await page.screenshot({
      path: path.join(output, "job-actions.png"),
      fullPage: true,
    });

    // 修改操作和重开页面都应保持原字段，不生成另一个“关闭状态”。
    await page.locator('#detail [data-action="edit-item"]').click();
    assert.equal(
      await page.locator('#item-form [name="action"]').inputValue(),
      "关闭",
    );
    await page.locator('#item-form [name="action"]').selectOption("修改");
    await page.locator('[data-action="save-item"]').click();
    await closeRow.getByText("修改", { exact: true }).waitFor();
    await page.reload();
    await page.locator('#side-versions [data-name="qa-job-actions"]').click();
    assert.match(await closeRow.innerText(), /修改/);
    assert.match(await closeRow.innerText(), /待上线/);

    // 历史删除记录继续可编辑，不能因新选项而回退成新增。
    await page.locator('tr[data-select="deletedJob"]').click();
    await page.locator('#detail [data-action="edit-item"]').click();
    assert.equal(
      await page.locator('#item-form [name="action"]').inputValue(),
      "删除",
    );
    await page.locator('.modal-foot [data-action="close-modal"]').click();
    assert.deepEqual(errors, []);
    console.log(
      "UI PASS: 定时任务新增/修改/关闭展示、关闭新增和编辑、详情与复制值、刷新持久化、独立上线状态、历史删除兼容；未调用调度系统。",
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
