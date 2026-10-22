const path = require("path");

/**
 * 验证逐行勾选、全选和两类批量操作；拦截所有外部执行，不访问真实 SQL 或 Codex。
 * @param {object} page 已创建测试版本和示例记录的 Playwright 页面。
 * @param {string} output 当前隔离 QA 截图目录。
 * @returns {Promise<void>} 行为不符合预期时抛错，成功后清理所有模拟路由。
 */
module.exports = async function checkBatchUI(page, output) {
  let sqlQueue = null;
  let verifyQueue = null;
  let currentBatch = null;
  const requests = {};
  await page.route("**/api/state", async (route) => {
    const response = await route.fetch();
    const result = await response.json();
    if (sqlQueue || verifyQueue) {
      result.data.sql_queue = sqlQueue;
      result.data.verify_queue = verifyQueue;
      result.data.batch = currentBatch;
      result.data.revision += 1000;
    }
    await route.fulfill({ response, json: result });
  });
  try {
    await page.locator('[data-action="tab"][data-kind="sql"]').click();
    if (!(await page.locator('[data-action="bulk-execute"]').isDisabled()))
      throw new Error("空选择仍可批量执行");
    const rowBefore = await page.evaluate(() => selectedID);
    await page.locator("[data-bulk-check]").first().check();
    if ((await page.evaluate(() => selectedID)) !== rowBefore)
      throw new Error("勾选改变了详情行");
    if ((await page.locator(".bulk-count").textContent()) !== "已选 1 项")
      throw new Error("逐项勾选计数错误");
    await page.locator('[data-action="bulk-select-all"]').click();
    if ((await page.locator("[data-bulk-check]:checked").count()) !== 3)
      throw new Error("SQL 全选未选中全部三项");
    await page.locator("[data-bulk-check]").first().uncheck();
    if (
      !(await page
        .locator("[data-bulk-all]")
        .evaluate((node) => node.indeterminate))
    )
      throw new Error("取消单项后全选未显示半选");
    await page.locator("[data-bulk-all]").check();
    const sqlItems = await page.evaluate(() =>
      versionData()
        .items.filter((item) => item.kind === "sql")
        .sort(
          (a, b) =>
            sqlPhaseOrder.indexOf(a.data.phase) -
            sqlPhaseOrder.indexOf(b.data.phase),
        ),
    );
    await page.route("**/api/db/batch/plan", (route) => {
      requests.sqlPlan = route.request().postDataJSON();
      return route.fulfill({
        json: {
          data: {
            token: "mock-bulk-plan",
            version: "5.7.3",
            items: sqlItems.map((item) => ({
              name: item.data.name,
              phase: item.data.phase,
              plan: {
                item_id: item.id,
                side: item.data.side,
                sql: item.data.sql,
                databases: ["newlink", "newlink1"],
                skipped: [],
              },
            })),
          },
        },
      });
    });
    await page.route("**/api/db/batch/execute", (route) => {
      requests.sqlExecute = route.request().postDataJSON();
      const executions = [
        { database: "newlink", state: "成功", detail: "模拟执行成功" },
        {
          database: "newlink1",
          state: "失败",
          detail: "模拟执行失败，继续其他库",
        },
      ];
      sqlQueue = {
        id: "mock-sql-queue",
        version: "5.7.3",
        running: true,
        rows: sqlItems.map((item, index) => ({
          item_id: item.id,
          name: item.data.name,
          phase: item.data.phase,
          side: item.data.side,
          state: index === 0 ? "执行中" : "等待中",
          detail: "",
          executions: index === 0 ? executions : [],
        })),
      };
      currentBatch = {
        version: "5.7.3",
        item_id: sqlItems[0].id,
        running: true,
        rows: executions,
      };
      return route.fulfill({ json: { data: null } });
    });
    await page.route("**/api/db/batch/cancel", (route) => {
      requests.sqlCancel = route.request().postDataJSON();
      sqlQueue.running = false;
      currentBatch.running = false;
      for (let index = 0; index < sqlQueue.rows.length; index++)
        sqlQueue.rows[index].state = index === 0 ? "有失败" : "未执行";
      return route.fulfill({ json: { data: null } });
    });
    await page.screenshot({
      path: path.join(output, "batch-sql-selection.png"),
    });
    await page.locator('[data-action="bulk-execute"]').click();
    await page
      .getByRole("heading", { name: "确认批量执行测试环境 SQL" })
      .waitFor();
    if (requests.sqlPlan.ids.length !== 3)
      throw new Error("SQL 预览没有提交完整勾选项");
    const preview = await page
      .locator(".bulk-preview-item summary")
      .allTextContents();
    if (
      !preview[0].includes("创建表") ||
      !preview[1].includes("修改表") ||
      !preview[2].includes("初始化数据")
    )
      throw new Error("批量预览没有展示正文执行顺序");
    if (!(await page.locator('[data-action="bulk-confirm-sql"]').isDisabled()))
      throw new Error("未勾选确认仍可启动 SQL");
    await page.screenshot({ path: path.join(output, "batch-sql-preview.png") });
    await page.locator("#bulk-sql-confirm").check();
    await page.locator('[data-action="bulk-confirm-sql"]').click();
    await page.getByRole("heading", { name: "批量 SQL 执行进度" }).waitFor();
    if (requests.sqlExecute.confirm !== true)
      throw new Error("批量执行没有传入勾选确认");
    await page
      .locator(".bulk-results td")
      .filter({ hasText: "newlink1" })
      .waitFor();
    await page.screenshot({
      path: path.join(output, "batch-sql-progress.png"),
    });
    await page.locator('[data-action="bulk-cancel"]').click();
    if (requests.sqlCancel.id !== "mock-sql-queue")
      throw new Error("停止没有绑定当前 SQL 批次");
    if (!(await page.locator('[data-action="bulk-cancel"]').isDisabled()))
      throw new Error("已结束批次仍显示可停止");
    await page
      .locator("[data-bulk-progress-id]")
      .first()
      .evaluate((node) => {
        node.open = false;
      });
    await page.locator("[data-bulk-progress-id] summary").first().click();
    await page.evaluate(() => refresh(true));
    if (
      !(await page
        .locator("[data-bulk-progress-id]")
        .first()
        .evaluate((node) => node.open))
    )
      throw new Error("轮询折叠了用户展开的逐库执行结果");
    await page.locator('[data-action="close-modal"]').first().click();

    // 切换表格后清空选择，不能把 SQL 选择带进日志验收。
    await page.locator('[data-action="tab"][data-kind="verify"]').click();
    if (await page.locator("[data-bulk-check]:checked").count())
      throw new Error("切换表格后仍保留旧 SQL 勾选");
    await page.evaluate(async () => {
      const sample = versionData().items.find((item) => item.kind === "verify");
      for (const suffix of ["second", "third"])
        await api("item/save", {
          version: currentVersion,
          item: {
            ...sample,
            id: "qa-bulk-" + suffix,
            data: { ...sample.data, title: "批量验收示例 " + suffix },
          },
        });
      await refresh(true);
    });
    await page.locator('[data-action="bulk-select-all"]').click();
    await page.locator("[data-bulk-check]").last().uncheck();
    await page.route("**/api/ai/verify-batch", (route) => {
      requests.verify = route.request().postDataJSON();
      verifyQueue = {
        id: "mock-verify-queue",
        version: "5.7.3",
        running: false,
        rows: requests.verify.ids.map((id, index) => ({
          item_id: id,
          title: "批量验收模拟 " + index,
          state: index === 0 ? "通过" : "证据不足",
          detail: "仅 UI 模拟结果，未查询生产日志",
        })),
      };
      return route.fulfill({ json: { data: null } });
    });
    await page.screenshot({
      path: path.join(output, "batch-verify-selection.png"),
    });
    await page.locator('[data-action="bulk-verify"]').click();
    await page.getByRole("heading", { name: "批量验证上线结果" }).waitFor();
    if ((await page.locator(".bulk-selection-list li").count()) !== 2)
      throw new Error("批量验收确认范围与勾选不一致");
    await page.locator('[data-action="bulk-confirm-verify"]').click();
    await page.getByRole("heading", { name: "批量 AI 验证进度" }).waitFor();
    if (
      requests.verify.ids.length !== 2 ||
      !requests.verify.start ||
      !requests.verify.end
    )
      throw new Error("批量验收缺少所选项或统一时间窗");
    if ((await page.locator(".bulk-results tbody tr").count()) !== 2)
      throw new Error("批量验收没有逐项展示结论");
    await page.screenshot({
      path: path.join(output, "batch-verify-results.png"),
    });
    await page.locator('[data-action="close-modal"]').first().click();
  } finally {
    for (const endpoint of [
      "state",
      "db/batch/plan",
      "db/batch/execute",
      "db/batch/cancel",
      "ai/verify-batch",
    ])
      await page.unroute("**/api/" + endpoint);
    await page.evaluate(() => refresh(true));
  }
};
