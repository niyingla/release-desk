// 使用隔离 --data-dir，AI 执行接口全部模拟；不消耗模型额度或查询生产。
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
  const requests = {};
  let singleTask = null;
  let verifyQueue = null;
  const ids = [
    "target-only",
    "related-fix",
    "fix-3",
    "fix-4",
    "fix-5",
    "fix-6",
    "fix-7",
    "fix-8",
  ];
  // 只替换外部取证返回的状态，版本保存、选择和界面渲染仍使用真实应用。
  await page.route("**/api/state", async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    if (singleTask) body.data.single_verify = singleTask;
    if (verifyQueue) body.data.verify_queue = verifyQueue;
    await route.fulfill({ response, json: body });
  });
  page.on("pageerror", (error) => errors.push(error.message));
  // 模拟服务只记录实际 UI 请求；其余状态和版本保存使用真实本机后端。
  await page.route("**/api/ai/verify", (route) => {
    requests.single = route.request().postDataJSON();
    return route.fulfill({ json: { data: null } });
  });
  await page.route("**/api/ai/verify-batch", (route) => {
    requests.batch = route.request().postDataJSON();
    return route.fulfill({ json: { data: null } });
  });
  try {
    await page.goto(process.argv[2]);
    await page
      .getByRole("heading", { name: "版本总览", exact: true })
      .waitFor();
    await page.evaluate(async (ids) => {
      await api("version/save", {
        name: "qa-adaptive-verification",
        status: "计划中",
      });
      for (const id of ids) {
        await api("item/save", {
          version: "qa-adaptive-verification",
          item: {
            id,
            kind: "verify",
            action: "修改",
            service: "",
            data: {
              title: id,
              change: "按业务日期限制重复创建排班",
              source_evidence: "qa-sha TaskService#create",
              result: "旧验收结论，不应进入新提示词",
            },
          },
        });
      }
      await refresh(true);
    }, ids);
    await page
      .locator('#side-versions [data-name="qa-adaptive-verification"]')
      .click();
    await page.locator('[data-action="tab"][data-kind="verify"]').click();
    await page.locator('tr[data-select="target-only"]').click();
    // 复制提示词入口与软件内验证共用代码上下文，旧结论不能充当新证据。
    const prompt = await page.evaluate(() =>
      api("ai/prompt", {
        version: currentVersion,
        id: selectedID,
        start: "2026-10-16T10:00",
        end: "2026-10-16T11:00",
      }),
    );
    const data = JSON.parse(prompt.slice(prompt.lastIndexOf("\n{")));
    assert.equal(data.change.source_evidence, "qa-sha TaskService#create");
    assert.equal(data.change.result, undefined);
    assert.ok(data.code_repository && data.sql_repository);
    await page.locator('#detail [data-action="verify"]').click();
    await page
      .getByRole("heading", { name: "AI 智能验证", exact: true })
      .waitFor();
    assert.match(
      await page.locator("#modal-body").innerText(),
      /由 AI 自主选择/,
    );
    assert.match(
      await page.locator("#modal-body").innerText(),
      /由 AI 定位涉及服务/,
    );
    const output = path.resolve(__dirname, "../../.work/qa/artifacts");
    fs.mkdirSync(output, { recursive: true });
    await page.screenshot({
      path: path.join(output, "adaptive-verification.png"),
    });
    await page.locator('[data-action="start-verify"]').click();
    await page.waitForFunction(() =>
      document.querySelector('.modal-foot [data-action="tasks"]'),
    );
    assert.equal(requests.single.id, "target-only");
    const analysis = {
      goal: "重复请求只产生一个有效任务",
      code: "目标版本通过唯一键原子保护",
      logs: "预填关键词无命中；改查请求链发现重复请求已拦截 <script>window.qaInjected=true</script>",
      reasoning: "代码保护与生产链路相互印证，不依赖固定成功词",
    };
    singleTask = {
      id: "qa-single",
      version: "qa-adaptive-verification",
      item_id: "target-only",
      running: false,
      result: {
        status: "通过",
        summary: "修复目标已体现",
        environment: "生产",
        window: "QA 北京时间范围",
        evidence: ["模拟实际请求链"],
        next_steps: "",
        analysis,
      },
    };
    await page.evaluate(() => refresh(true));
    let resultText = await page.locator("#modal-body").innerText();
    for (const value of Object.values(analysis))
      assert.ok(resultText.includes(value));
    assert.equal(await page.evaluate(() => window.qaInjected), undefined);
    await page.screenshot({
      path: path.join(output, "joint-verification-result.png"),
    });
    // 升级前的历史没有 analysis，仍须正常显示原结论。
    singleTask.result = {
      status: "证据不足",
      summary: "旧版验证摘要",
      evidence: ["旧版证据"],
    };
    await page.evaluate(() => refresh(true));
    assert.match(await page.locator("#modal-body").innerText(), /旧版验证摘要/);
    await page.locator('.modal-foot [data-action="close-modal"]').click();
    await page.locator("[data-bulk-all]").check();
    await page.locator('[data-action="bulk-verify"]').click();
    await page
      .getByRole("heading", { name: "批量验证上线结果", exact: true })
      .waitFor();
    const text = await page.locator("#modal-body").innerText();
    assert.ok(!text.includes("将跳过"));
    assert.match(text, /由 AI 定位涉及服务/);
    assert.match(text, /最多同时验证 6 项/);
    const concurrency = page.locator('[name="bulk-verify-concurrency"]');
    assert.equal(await concurrency.inputValue(), "6");
    await concurrency.selectOption("3");
    await page.screenshot({
      path: path.join(output, "verification-concurrency-choice.png"),
    });
    await page.locator('[data-action="bulk-confirm-verify"]').click();
    await page
      .getByRole("heading", { name: "批量 AI 验证进度", exact: true })
      .waitFor();
    assert.deepEqual([...requests.batch.ids].sort(), [...ids].sort());
    assert.equal(requests.batch.concurrency, 3);
    verifyQueue = {
      id: "qa-queue",
      version: "qa-adaptive-verification",
      running: true,
      concurrency: 3,
      cancel_requested: false,
      rows: ids.map((id, i) => ({
        item_id: id,
        title: id,
        state: i < 3 ? "验证中" : "等待中",
        detail: "",
      })),
    };
    await page.evaluate(() => refresh(true));
    resultText = await page.locator("#modal-body").innerText();
    assert.match(resultText, /运行中 3 · 等待中 5 · 并发上限 3/);
    assert.match(resultText, /已处理 0 \/ 8 项/);
    verifyQueue.running = false;
    for (const row of verifyQueue.rows) {
      row.state = "通过";
      row.detail = "目标达成";
      row.result = {
        status: "通过",
        summary: "目标达成",
        evidence: ["模拟实际证据"],
        analysis,
      };
    }
    await page.evaluate(() => refresh(true));
    const detail = page.locator('[data-bulk-progress-id="target-only"]');
    await detail.locator("summary").click();
    assert.match(
      await detail.innerText(),
      /综合理由：代码保护与生产链路相互印证/,
    );
    assert.match(
      await detail.locator("..").getAttribute("data-copy"),
      /日志发现：预填关键词无命中/,
    );
    await page.evaluate(() => refresh(true));
    assert.equal(await detail.getAttribute("open"), "");
    await page.screenshot({
      path: path.join(output, "joint-verification-batch.png"),
    });
    assert.deepEqual(errors, []);
    console.log(
      "UI PASS: 单项/批量自主验证、并发数选择与进度、分项分析回看复制、旧历史兼容与日志转义；AI 调用模拟，浏览器错误 0。",
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
