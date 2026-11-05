// 只对隔离服务执行：配置、模型和仓库候选均使用明确的 QA 数据，不访问真实外部系统。
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
    saves = [],
    copies = [];
  let reads = 0;
  page.on("pageerror", (error) => errors.push(error.message));
  await page.route("**/api/git/versions", (route) =>
    route.fulfill({
      json: {
        data: {
          versions: [{ name: "99.0.0", folder: "SQL_ONLY", count: 1 }],
          branch: "sql-fixture",
        },
      },
    }),
  );
  await page.route("**/api/git/code-versions", (route) => {
    reads++;
    return route.fulfill({
      json: {
        data: {
          versions: ["5.8.0", "5.10.0", "5.8.1"].map((name) => ({
            name,
            folder: "release-" + name,
            count: 0,
          })),
          branch: "release-5.8.1",
          path: "QA_CODE_REPOSITORY",
        },
      },
    });
  });
  await page.route("**/api/settings/save", (route) => {
    saves.push(route.request().postDataJSON());
    return route.fulfill({ json: { data: null } });
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
    await page.locator('[data-action="new-version"]').first().click();
    await page.locator('[data-action="toggle-version-options"]').click();
    const options = page.locator('#version-options [role="option"]');
    assert.deepEqual(await options.allTextContents(), [
      "5.10.0",
      "5.8.1",
      "5.8.0",
    ]);
    assert.equal(reads, 1);
    await page.locator("#version-name").fill("5.8");
    assert.deepEqual(await options.allTextContents(), ["5.8.1", "5.8.0"]);
    await options.last().click();
    assert.equal(await page.locator("#version-name").inputValue(), "5.8.0");
    await page.locator("#version-name").fill("custom-version");
    assert.equal(await options.count(), 0);
    assert.match(
      await page.locator("#version-options").innerText(),
      /手动输入/,
    );
    await page.locator('.modal-foot [data-action="close-modal"]').click();
    await page.locator('[data-action="settings"]').click();
    assert.equal(
      await page.locator('.settings-tabs [aria-selected="true"]').innerText(),
      "数据库",
    );
    assert.equal(await page.locator("#settings-panel-code").isVisible(), false);
    await page.locator('[data-action="settings-tab"][data-tab="code"]').click();
    await page
      .locator('[name="code_repository"]')
      .fill("D:\\QA\\code-repository");
    await page.locator('[data-action="settings-tab"][data-tab="logs"]').click();
    await page.locator('[name="log_skill"]').fill("D:\\QA\\optional-skill.txt");
    await page
      .locator('[data-action="settings-tab"][data-tab="initialization"]')
      .click();
    assert.equal(
      await page.locator('[name="initialization_base_urls"]').isVisible(),
      true,
    );
    await page.locator('[data-action="settings-tab"][data-tab="code"]').click();
    assert.equal(
      await page.locator('[name="code_repository"]').inputValue(),
      "D:\\QA\\code-repository",
    );
    const output = path.resolve(".work/qa/settings-and-version-picker");
    fs.mkdirSync(output, { recursive: true });
    await page.screenshot({ path: path.join(output, "code-and-ai.png") });
    await page.locator('[data-action="save-settings"]').click();
    await page.locator("#settings-form").waitFor({ state: "detached" });
    assert.equal(saves.length, 1);
    assert.equal(saves[0].code_repository, "D:\\QA\\code-repository");
    assert.equal(saves[0].log_skill, "D:\\QA\\optional-skill.txt");
    assert.ok(saves[0].initialization_base_urls.test);
    await page.locator('[data-action="settings"]').click();
    await page.locator('[data-action="settings-tab"][data-tab="code"]').click();
    await page.locator('[data-action="ai-onboarding"]').click();
    await page
      .getByRole("heading", { name: "AI 一键接入", exact: true })
      .waitFor();
    assert.match(
      await page.locator("#modal-body").innerText(),
      /无需另发技能文件|技能.*位置/,
    );
    await page.locator('[data-action="copy-ai-onboarding"]').click();
    assert.equal(copies.length, 1);
    assert.match(copies[0].text, /AI接入指引\.txt/);
    assert.deepEqual(errors, []);
    console.log(
      "PASS：代码库版本倒序、搜索与手动输入；设置分区保留编辑并完整提交；内置技能接入指令可复制。",
    );
  } finally {
    await browser.close();
  }
})().catch((error) => {
  console.error(error.message);
  process.exitCode = 1;
});
