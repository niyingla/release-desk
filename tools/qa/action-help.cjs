// 仅对隔离 --data-dir 服务执行；扫描接口强制模拟，避免测试调用真实 Codex。
const { chromium } = require("playwright");
const assert = require("node:assert/strict");
const path = require("node:path");
const fs = require("node:fs");

(async () => {
  const browser = await chromium.launch({ channel: "msedge", headless: true });
  const page = await browser.newPage({
    viewport: { width: 1440, height: 960 },
  });
  const errors = [];
  const scans = [];
  const archives = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.route("**/api/ai/scan", (route) => {
    scans.push(route.request().postDataJSON().name);
    return route.fulfill({ json: { data: null } });
  });
  await page.route("**/api/version/archive", (route) => {
    archives.push(route.request().postDataJSON());
    return route.continue();
  });
  try {
    await page.goto(process.argv[2]);
    await page
      .getByRole("heading", { name: "版本总览", exact: true })
      .waitFor();
    await page.evaluate(async () => {
      await api("version/save", { name: "qa-action-help", status: "计划中" });
      await refresh(true);
    });
    await page.locator('#side-versions [data-name="qa-action-help"]').click();
    // 控件标题必须解释用途，而非重复按钮名称；图标按钮同样可直接悬停阅读。
    for (const [selector, meaning] of [
      ['.page-head [data-action="scan-version"]', /Codex/],
      ['.page-head [data-action="archive-version"]', /恢复/],
      ['.page-head [data-action="edit-version"]', /状态/],
      ['.page-head [data-action="export"]', /HTML/],
      ['header [data-action="backup"]', /JSON/],
      ['header [data-action="restore"]', /备份/],
      ["#theme-select", /保存/],
    ]) {
      await page.locator(selector).hover();
      assert.match(
        (await page.locator(selector).getAttribute("title")) || "",
        meaning,
      );
    }
    // 扫描必须先说明目标与影响；取消及关闭均不能调用扫描接口。
    await page.locator('.page-head [data-action="scan-version"]').click();
    await page
      .getByRole("heading", { name: "确认 AI 扫描", exact: true })
      .waitFor();
    assert.match(
      await page.locator("#modal-body").innerText(),
      /qa-action-help/,
    );
    assert.match(await page.locator("#modal-body").innerText(), /额度/);
    assert.equal(scans.length, 0);
    await page.locator('.modal-foot [data-action="close-modal"]').click();
    assert.equal(scans.length, 0);
    await page.locator('.page-head [data-action="scan-version"]').click();
    await page.locator('[data-action="confirm-scan"]').click();
    await page
      .getByRole("heading", { name: "AI 扫描版本", exact: true })
      .waitFor();
    assert.deepEqual(scans, ["qa-action-help"]);
    await page.locator('.modal-foot [data-action="close-modal"]').click();

    // 归档取消不能改记录；确认后调用真实的隔离数据接口并验证入口隐藏。
    await page.locator('.page-head [data-action="archive-version"]').click();
    await page
      .getByRole("heading", { name: "确认归档版本", exact: true })
      .waitFor();
    assert.match(await page.locator("#modal-body").innerText(), /恢复/);
    assert.equal(archives.length, 0);
    const output = path.resolve(__dirname, "../../.work/qa/artifacts");
    fs.mkdirSync(output, { recursive: true });
    await page.screenshot({
      path: path.join(output, "archive-confirmation.png"),
    });
    await page.locator('.modal-foot [data-action="close-modal"]').click();
    assert.equal(archives.length, 0);
    await page.locator('.page-head [data-action="archive-version"]').click();
    await page.locator('[data-action="confirm-archive-version"]').click();
    await page
      .locator('#side-versions [data-name="qa-action-help"]')
      .waitFor({ state: "detached" });
    assert.deepEqual(archives, [{ name: "qa-action-help", archived: true }]);
    await page.locator('[data-action="archived-versions"]').click();
    await page
      .locator('[data-action="archive-version"][data-name="qa-action-help"]')
      .click();
    await page
      .getByRole("heading", { name: "确认恢复版本", exact: true })
      .waitFor();
    await page.locator('[data-action="confirm-archive-version"]').click();
    await page.locator('#side-versions [data-name="qa-action-help"]').waitFor();
    assert.deepEqual(archives[1], { name: "qa-action-help", archived: false });
    // 新建版本勾选扫描也必须先确认；取消只取消扫描，已保存版本仍保留。
    await page.route("**/api/git/versions", (route) =>
      route.fulfill({
        json: { data: { branch: "qa", path: "QA fixture", versions: [] } },
      }),
    );
    await page.locator('[data-action="new-version"]').first().click();
    await page
      .locator('#version-form [name="name"]')
      .fill("qa-scan-after-save");
    await page.locator('[name="ai_scan"]').check();
    await page.locator('[data-action="save-version"]').click();
    await page
      .getByRole("heading", { name: "确认 AI 扫描", exact: true })
      .waitFor();
    assert.match(
      await page.locator("#modal-body").innerText(),
      /qa-scan-after-save/,
    );
    assert.equal(scans.length, 1);
    await page.locator('.modal-foot [data-action="close-modal"]').click();
    assert.equal(scans.length, 1);
    assert.equal(
      await page
        .locator('#side-versions [data-name="qa-scan-after-save"]')
        .count(),
      1,
    );
    assert.deepEqual(errors, []);
    console.log(
      "UI PASS: 操作用途提示、扫描取消不启动/确认绑定版本、新建版本勾选扫描先确认、归档取消不写入/确认隐藏/恢复；Codex 模拟，浏览器错误 0。",
    );
  } finally {
    await page.unrouteAll({ behavior: "ignoreErrors" });
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
