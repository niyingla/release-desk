const path = require("path");

/**
 * 检查统一任务列表、逐库结果、筛选和精确停止；所有任务接口均为模拟。
 * @param {object} page 已打开隔离工作台的 Playwright 页面。
 * @param {string} output 本轮截图目录。
 * @returns {Promise<void>} 失败抛错；完成后恢复实际测试状态。
 */
module.exports = async function checkTaskCenter(page, output) {
  const tasks = [
    {
      id: "qa-running-scan",
      kind: "scan",
      version: "5.7.3",
      title: "版本扫描 · huohuo",
      status: "运行中",
      running: true,
      progress: "读取 Git 差异",
      started: new Date().toISOString(),
      rows: [
        {
          id: "scan",
          title: "扫描阶段",
          state: "运行中",
          detail: "读取 Git 差异",
        },
      ],
    },
    {
      id: "qa-sql-result",
      kind: "sql",
      version: "5.7.3",
      title: "批量 SQL 执行",
      status: "有失败",
      running: false,
      progress: "已处理 1 / 1 项",
      started: new Date(Date.now() - 30000).toISOString(),
      finished: new Date().toISOString(),
      rows: [
        {
          id: "script",
          title: "增加字段并核验",
          state: "有失败",
          children: [
            {
              id: "db1",
              title: "newlink1",
              state: "成功",
              detail: "实际结构核验通过",
            },
            {
              id: "db2",
              title: "newlink2",
              state: "结构不一致",
              detail: "name：类型不一致",
            },
          ],
        },
      ],
    },
    {
      id: "qa-verification-result",
      kind: "verify",
      version: "5.7.3",
      title: "日志验证",
      status: "证据不足",
      running: false,
      progress: "已处理 1 / 1 项",
      started: new Date(Date.now() - 60000).toISOString(),
      finished: new Date().toISOString(),
      rows: [
        {
          id: "change",
          title: "变更项",
          state: "证据不足",
          condition: "模拟条件",
          detail: "没有匹配生产证据",
          evidence: ["仅模拟，未查询生产日志"],
        },
      ],
    },
  ];
  let cancelled = "";
  await page.route("**/api/state", async (route) => {
    const response = await route.fetch();
    const result = await response.json();
    result.data.tasks = tasks.map(({ rows, ...summary }) => summary);
    return route.fulfill({ json: result });
  });
  await page.route("**/api/task/detail", (route) =>
    route.fulfill({
      json: {
        data: tasks.find(
          (item) => item.id === route.request().postDataJSON().id,
        ),
      },
    }),
  );
  await page.route("**/api/task/cancel", (route) => {
    cancelled = route.request().postDataJSON().id;
    const item = tasks.find((task) => task.id === cancelled);
    item.running = false;
    item.status = "已停止";
    item.progress = "已保留已有结果";
    item.finished = new Date().toISOString();
    item.rows[0].state = "已停止";
    return route.fulfill({ json: { data: null } });
  });
  try {
    await page.evaluate(() => refresh(true));
    await page.locator('[data-action="tasks"]').click();
    await page
      .getByRole("heading", { name: "任务中心", exact: true })
      .waitFor();
    if ((await page.locator(".task-list tbody tr").count()) !== 3)
      throw new Error("统一目录没有覆盖三类任务");
    await page
      .locator('[data-action="task-detail"][data-id="qa-sql-result"]')
      .click();
    await page
      .locator("#modal-body")
      .getByText("newlink2", { exact: true })
      .waitFor();
    if (!(await page.locator("#modal-body").innerText()).includes("结构不一致"))
      throw new Error("逐库核验状态丢失");
    await page.locator("[data-task-row] summary").first().click();
    await page.evaluate(() => refresh(true));
    if (
      await page
        .locator("[data-task-row]")
        .first()
        .evaluate((node) => node.open)
    )
      throw new Error("任务轮询覆盖了用户折叠状态");
    await page.locator("[data-task-row] summary").first().click();
    await page.screenshot({ path: path.join(output, "task-sql-results.png") });
    await page.locator('[data-action="close-modal"]').first().click();
    await page
      .locator('[data-action="task-filter"][data-kind="verify"]')
      .click();
    if ((await page.locator(".task-list tbody tr").count()) !== 1)
      throw new Error("任务类型筛选错误");
    await page
      .locator('[data-action="task-detail"][data-id="qa-verification-result"]')
      .click();
    await page
      .locator("#modal-body")
      .getByText("仅模拟，未查询生产日志", { exact: true })
      .waitFor();
    if (
      !(await page.locator("#modal-body").innerText()).includes(
        "仅模拟，未查询生产日志",
      )
    )
      throw new Error("历史证据缺失");
    await page.locator('[data-action="close-modal"]').first().click();
    await page.locator('[data-action="task-filter"][data-kind=""]').click();
    await page
      .locator('[data-action="task-stop"][data-id="qa-running-scan"]')
      .click();
    await page
      .locator(".task-list tr")
      .filter({ hasText: "版本扫描 · huohuo" })
      .getByText("已停止", { exact: true })
      .waitFor();
    if (
      cancelled !== "qa-running-scan" ||
      (await page
        .locator('[data-action="task-stop"][data-id="qa-running-scan"]')
        .count())
    )
      throw new Error("停止未绑定精确任务或终态仍可停止");
    await page.screenshot({ path: path.join(output, "task-center.png") });
  } finally {
    if (await page.locator('[data-action="close-modal"]').count())
      await page.locator('[data-action="close-modal"]').first().click();
    for (const endpoint of ["state", "task/detail", "task/cancel"])
      await page.unroute("**/api/" + endpoint);
    await page.locator('[data-action="overview"]').click();
    await page.evaluate(() => refresh(true));
  }
};
