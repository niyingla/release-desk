// 使用隔离 --data-dir 服务验证配置要求；不连接数据库、不启动 Codex。
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
  page.on("pageerror", (error) => errors.push(error.message));
  try {
    await page.goto(process.argv[2]);
    await page
      .getByRole("heading", { name: "版本总览", exact: true })
      .waitFor();
    // 从实际接口建立旧版事项，缺字段不应在界面被误判为可选。
    await page.evaluate(async () => {
      for (const name of [
        "qa-config-requirement",
        "qa-released-a",
        "qa-released-b",
      ]) {
        if (state.versions.some((version) => version.version.name === name))
          await api("version/delete", { name });
      }
      await api("version/save", {
        name: "qa-config-requirement",
        status: "计划中",
      });
      await api("item/save", {
        version: "qa-config-requirement",
        item: {
          id: "legacy-config",
          kind: "config",
          data: {
            filename: "legacy.yml",
            content: "feature:\n  enabled: true",
          },
        },
      });
    });
    await page.evaluate(() => refresh(true));
    await page
      .locator(
        '#side-versions [data-action="open-version"][data-name="qa-config-requirement"]',
      )
      .click();
    await page.locator('[data-action="tab"][data-kind="config"]').click();
    await page
      .getByRole("columnheader", { name: "是否必须配置", exact: true })
      .waitFor();
    const legacy = page.locator('tr[data-select="legacy-config"]');
    assert.match(await legacy.innerText(), /待确认/);

    // 通过真实表单修改为可选，并保留默认值说明和原线上状态。
    await legacy.click();
    await page.locator('#detail [data-action="edit-item"]').click();
    assert.equal(
      await page.locator('#item-form [name="required"]').inputValue(),
      "待确认",
    );
    await page.locator('#item-form [name="required"]').selectOption("可选配置");
    await page
      .locator('#item-form [name="description"]')
      .fill("代码默认 enabled=true，默认行为满足上线要求。");
    await page.locator('[data-action="save-item"]').click();
    await page.locator("#item-form").waitFor({ state: "detached" });
    await legacy.getByText("可选配置", { exact: true }).waitFor();
    assert.match(await legacy.innerText(), /可选配置/);
    assert.match(
      await page.locator(".detail-aside").innerText(),
      /是否必须配置[\s\S]*可选配置/,
    );

    // 新增必须配置项，确保新增和编辑共用相同有限选项并实际持久化。
    await page.locator('[data-action="new-item"]').click();
    await page.locator('#item-form [name="filename"]').fill("required.yml");
    await page
      .locator('#item-form [name="content"]')
      .fill("feature:\n  enabled: false");
    await page.locator('#item-form [name="required"]').selectOption("必须配置");
    await page.locator('[data-action="save-item"]').click();
    await page.locator("#item-form").waitFor({ state: "detached" });
    await page.reload();
    await page
      .locator(
        '#side-versions [data-action="open-version"][data-name="qa-config-requirement"]',
      )
      .click();
    await page.locator('[data-action="tab"][data-kind="config"]').click();
    const rows = page.locator("tbody tr");
    assert.match(
      await rows.filter({ hasText: "required.yml" }).innerText(),
      /必须配置/,
    );
    assert.match(
      await rows.filter({ hasText: "legacy.yml" }).innerText(),
      /可选配置/,
    );
    const items = await page.evaluate(() => versionData().items);
    assert.equal(
      items.find((item) => item.id === "legacy-config").online,
      "待上线",
    );
    assert.equal(
      items.find((item) => item.id === "legacy-config").data.description,
      "代码默认 enabled=true，默认行为满足上线要求。",
    );
    // 已发布快捷入口默认收起，用户展开后不被轮询刷新重新折叠。
    await page.evaluate(async () => {
      await api("version/save", { name: "qa-released-a", status: "已发布" });
      await api("version/save", { name: "qa-released-b", status: "已发布" });
      await refresh(true);
    });
    const group = page.locator("#published-versions");
    const publishedLink = group.locator('[data-name="qa-released-a"]');
    assert.equal(await group.evaluate((element) => element.open), false);
    assert.equal(await publishedLink.isVisible(), false);
    await group.locator("summary").click();
    await page.evaluate(() => refresh(true));
    assert.equal(await group.evaluate((element) => element.open), true);
    await publishedLink.click();
    assert.match(await page.locator("h1").innerText(), /qa-released-a/);
    // 改为未发布后应回到普通列表，剩余已发布项仍保持当前展开状态。
    await page.evaluate(async () => {
      await api("version/save", { name: "qa-released-a", status: "进行中" });
      await refresh(true);
    });
    assert.equal(await group.locator('[data-name="qa-released-a"]').count(), 0);
    assert.equal(
      await page
        .locator('#side-versions > [data-name="qa-released-a"]')
        .isVisible(),
      true,
    );
    await page.reload();
    await page
      .getByRole("heading", { name: "版本总览", exact: true })
      .waitFor();
    assert.equal(await group.evaluate((element) => element.open), false);
    await page
      .locator(
        '#side-versions [data-action="open-version"][data-name="qa-config-requirement"]',
      )
      .click();
    await page.locator('[data-action="tab"][data-kind="config"]').click();
    const output = path.resolve(__dirname, "../../.work/qa/artifacts");
    fs.mkdirSync(output, { recursive: true });
    await page.screenshot({
      path: path.join(output, "config-requirement.png"),
      fullPage: true,
    });
    assert.deepEqual(errors, []);
    console.log(
      "UI PASS: 配置要求表格/详情、新增与编辑、旧记录待确认、刷新持久化、原上线状态保留；已发布分组默认折叠、展开后刷新保留、改状态重新分组；浏览器错误 0。",
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
