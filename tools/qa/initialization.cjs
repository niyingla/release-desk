// 只使用隔离软件数据及模拟调用响应，不请求测试或线上业务域名。
const { chromium } = require("playwright");
const assert = require("node:assert/strict");

(async () => {
  const browser = await chromium.launch({ channel: "msedge", headless: true });
  const page = await browser.newPage({
    viewport: { width: 1440, height: 1000 },
  });
  const calls = [],
    copied = [],
    errors = [];
  const prerequisiteText =
    "先发布服务并完成字段变更，确认初始化范围和已有配置的保留要求。".repeat(8) +
    "\n配置来源和执行说明保留完整，不能因折叠而截断。";
  page.on("pageerror", (error) => errors.push(error.message));
  await page.route("**/api/clipboard/copy", async (route) => {
    copied.push(route.request().postDataJSON().text);
    await route.fulfill({ json: { data: null } });
  });
  await page.route("**/api/initialization/send", async (route) => {
    calls.push(route.request().postDataJSON());
    await route.fulfill({
      json: {
        data: {
          status: 200,
          body: '{"success":false,"message":"QA response"}',
          truncated: false,
          elapsed_ms: 1,
        },
      },
    });
  });
  try {
    await page.goto(process.argv[2]);
    await page.waitForFunction(() => state);
    await page.evaluate(async () => {
      await api("version/save", {
        name: "qa-initialization",
        status: "计划中",
      });
      await refresh(true);
    });
    await page
      .locator(
        '#side-versions [data-action="open-version"][data-name="qa-initialization"]',
      )
      .click();
    await page.locator('[data-action="tab"][data-kind="init_api"]').click();
    await page.locator('[data-action="new-item"]').click();
    const form = page.locator("#item-form");
    await form.locator('[name="name"]').fill("QA 服务商初始化");
    await form.locator('[name="side"]').selectOption("服务商端");
    await form.locator('[name="method"]').fill("POST");
    await form.locator('[name="url"]').fill("/maintenance/rebuild");
    assert.equal(await form.locator('[name="body"]').isVisible(), false);
    await form.locator('[name="parameter_mode"]').selectOption("必填参数");
    await form.locator('[name="params"]').fill('{"tenantId":""}');
    await form.locator('[name="body"]').fill('{"scope":""}');
    await form.locator('[name="prerequisites"]').fill(prerequisiteText);
    await form.locator('[name="expected"]').fill("核对实际业务结果");
    await page.locator('[data-action="save-item"]').click();
    await page.waitForFunction(() => modalType === "");
    assert.equal(calls.length, 0, "保存记录触发了业务调用");
    await page.locator('[data-action="copy-detail"]').click();
    assert.match(copied[0], /tenantId/);
    assert.match(copied[0], /先发布服务/);
    await page.locator('[data-action="call-initialization"]').click();
    const request = page.locator("#initialization-form");
    await request.waitFor();
    const notes = request.locator("details");
    assert.equal(await notes.count(), 2);
    assert.equal(await notes.first().evaluate((e) => e.open), false);
    await notes.first().locator("summary").click();
    assert.equal(
      await notes.first().locator("p").innerText(),
      prerequisiteText,
    );
    await notes.first().locator("summary").click();
    // 使用真实内嵌资源核对各色板和窄窗口，长地址与展开说明不能挤出操作区。
    for (const theme of ["night", "graphite", "purple", "forest", "light"]) {
      await page.evaluate((value) => {
        document.documentElement.dataset.theme = value;
      }, theme);
      for (const viewport of [
        { width: 1360, height: 880 },
        { width: 760, height: 760 },
        { width: 430, height: 650 },
      ]) {
        await page.setViewportSize(viewport);
        const layout = await page
          .locator(".initialization-modal")
          .evaluate((e) => {
            const body = e.querySelector(".modal-body");
            const footer = e
              .querySelector(".modal-foot")
              .getBoundingClientRect();
            const send = e
              .querySelector('[data-action="send-initialization"]')
              .getBoundingClientRect();
            const modal = e.getBoundingClientRect();
            return {
              widthFits:
                e.scrollWidth <= e.clientWidth &&
                body.scrollWidth <= body.clientWidth,
              footerFits:
                send.top >= footer.top &&
                send.bottom <= footer.bottom &&
                send.bottom <= innerHeight,
              modalFits:
                modal.top >= 0 &&
                modal.bottom <= innerHeight &&
                modal.left >= 0 &&
                modal.right <= innerWidth,
            };
          });
        assert.ok(
          Object.values(layout).every(Boolean),
          `${theme}/${viewport.width}: ${JSON.stringify(layout)}`,
        );
      }
    }
    await page.setViewportSize({ width: 1360, height: 880 });
    await page.evaluate(() => {
      document.documentElement.dataset.theme = "night";
    });
    await page.screenshot({
      path: ".work/qa/initialization-parameters.png",
      fullPage: true,
    });
    assert.equal(
      await request.locator('[name="environment"]').inputValue(),
      "test",
    );
    assert.match(
      await page.locator("#initialization-url").innerText(),
      /^https:\/\/qyapitest\.qiyucloud\.com\.cn\/spapi\/maintenance\/rebuild$/,
    );
    await request.locator('[name="token"]').fill("fake-old-test-token");
    await request.locator('[name="environment"]').selectOption("production");
    assert.equal(await request.locator('[name="token"]').inputValue(), "");
    assert.match(
      await page.locator("#initialization-url").innerText(),
      /^https:\/\/qyapi\.qiyucloud\.com\.cn\/spapi\/maintenance\/rebuild$/,
    );
    await request.locator('[name="params"]').fill('{"tenantId":"1"}');
    await request.locator('[name="body"]').fill('{"scope":"demo"}');
    await request.locator('[name="token"]').fill("fake-per-call-token");
    await page.locator('[data-action="send-initialization"]').click();
    await page
      .locator("#initialization-result")
      .getByText(/HTTP 200/)
      .waitFor();
    assert.equal(calls.length, 1);
    assert.equal(calls[0].environment, "production");
    assert.equal(calls[0].body, '{"scope":"demo"}');
    assert.equal(await request.locator('[name="token"]').inputValue(), "");
    const saved = await page.evaluate(
      () =>
        state.versions.find((v) => v.version.name === "qa-initialization")
          .items[0],
    );
    assert.equal(calls[0].version, "qa-initialization");
    assert.equal(calls[0].id, saved.id);
    assert.equal(saved.status, "待处理");
    assert.ok(!JSON.stringify(saved).includes("fake-per-call-token"));
    await page.locator('[data-action="send-initialization"]').click();
    assert.equal(calls.length, 1, "空 Token 仍然发送请求");
    await page.locator('[data-action="close-modal"]').first().click();
    await page.locator('#detail [data-action="edit-item"]').click();
    await page.locator('#item-form [name="status"]').selectOption("已完成");
    await page.locator('[data-action="save-item"]').click();
    await page.waitForFunction(() => modalType === "");
    await page.waitForFunction(
      () =>
        state.versions.find((v) => v.version.name === "qa-initialization")
          .items[0].status === "已完成",
    );
    assert.match(await page.locator("tr[data-select]").innerText(), /已完成/);
    // 无参调用隐藏参数输入，并要求重新填写本次 Token。
    await page.locator('#detail [data-action="edit-item"]').click();
    await page
      .locator('#item-form [name="parameter_mode"]')
      .selectOption("无参");
    await page.locator('[data-action="save-item"]').click();
    await page.waitForFunction(
      () =>
        modalType === "" &&
        state.versions.find((v) => v.version.name === "qa-initialization")
          .items[0].data.parameter_mode === "无参",
    );
    await page.locator('[data-action="call-initialization"]').click();
    assert.equal(
      await request.locator('[name="params"], [name="body"]').count(),
      0,
    );
    assert.equal(await request.locator('[name="token"]').inputValue(), "");
    await page.screenshot({
      path: ".work/qa/initialization-call.png",
      fullPage: true,
    });
    await request.locator('[name="token"]').fill("fake-new-token");
    await page.locator('[data-action="send-initialization"]').click();
    await page
      .locator("#initialization-result")
      .getByText(/HTTP 200/)
      .waitFor();
    assert.equal(calls.length, 2);
    assert.equal(calls[1].token, "fake-new-token");
    assert.equal(calls[1].params, undefined);
    assert.equal(calls[1].body, undefined);
    await page.locator('[data-action="close-modal"]').first().click();
    // 已识别服务内路径、尚未确认网关映射时，明确展示待确认并引导编辑，不拼接域名根地址。
    await page.evaluate(async () => {
      await api("item/save", {
        version: "qa-initialization",
        item: {
          id: "qa-route-pending",
          kind: "init_api",
          action: "新增",
          service: "qa-maintenance",
          data: {
            name: "QA 待确认路由",
            side: "租户端",
            method: "POST",
            service_path: "/init/records",
            parameter_mode: "无参",
          },
        },
      });
      await refresh(true);
      selectedID = "qa-route-pending";
      renderVersion();
    });
    assert.match(
      await page.locator('tr[data-select="qa-route-pending"]').innerText(),
      /\/init\/records.*调用路径待确认/,
    );
    assert.match(
      await page.locator('[data-action="call-initialization"]').innerText(),
      /补全调用路径/,
    );
    await page.locator('[data-action="call-initialization"]').click();
    await page.locator('#item-form [name="service_path"]').waitFor();
    assert.equal(
      await page.locator('#item-form [name="service_path"]').inputValue(),
      "/init/records",
    );
    assert.equal(await page.locator("#initialization-form").count(), 0);
    assert.equal(calls.length, 2, "待确认路由仍发起了请求");
    await page.locator('[data-action="close-modal"]').first().click();
    assert.deepEqual(errors, []);
    console.log(
      "PASS: 初始化新布局、五套主题/三种窗口、折叠说明全文、参数分类、测试默认/线上切换、spapi 路由、逐次 Token 与人工完成状态。",
    );
  } finally {
    await browser.close();
  }
})().catch((error) => {
  console.error(error.stack);
  process.exitCode = 1;
});
