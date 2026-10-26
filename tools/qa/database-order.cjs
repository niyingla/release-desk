// 验证数据库进度和历史仅重排展示副本，不执行 SQL 或修改实际状态。
const { chromium } = require("playwright");
const assert = require("node:assert/strict");

(async () => {
  const browser = await chromium.launch({ channel: "msedge", headless: true });
  const page = await browser.newPage();
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  try {
    await page.goto(process.argv[2]);
    await page.waitForFunction(
      () => state && typeof orderedDatabaseRows === "function",
    );
    const result = await page.evaluate(() => {
      const fixtures = [
        ["sp10001", "sp10", "sp2", "sp1", "sp"],
        [
          "newlink10",
          "newlink2",
          "newlink",
          "newlink_share10",
          "newlink_share2",
          "newlink_share",
        ],
      ];
      return fixtures.map((names) => {
        const rows = names.map((database) => ({
          database,
          state: "成功",
          detail: "QA",
        }));
        state.versions = [
          {
            version: { name: "qa-sort", status: "计划中" },
            items: [
              {
                id: "sort-sql",
                kind: "sql",
                data: { name: "QA SQL", sql: "SELECT 1;" },
              },
            ],
          },
        ];
        currentVersion = "qa-sort";
        currentKind = "sql";
        selectedID = "sort-sql";
        state.batch = {
          version: currentVersion,
          item_id: selectedID,
          environment: "test",
          rows,
          running: false,
        };
        selectedSQLEnvironment = "test";
        // 真实进度渲染使用后台批次，不能为了排序污染后台行号和后续进度更新。
        showProgress(false);
        const progress = [
          ...document.querySelectorAll("#modal-body tbody tr td:first-child"),
        ].map((n) => n.textContent);
        // 批量脚本结果复用相同排序，不因所属端或单条执行入口不同而改变。
        state.sql_queue = {
          id: "qa-order-queue",
          version: currentVersion,
          environment: "test",
          running: false,
          rows: [
            {
              item_id: selectedID,
              name: "QA SQL",
              phase: "初始化数据",
              side: "测试端",
              state: "成功",
              executions: rows,
            },
          ],
        };
        showBulkProgress("sql");
        const bulk = [
          ...document.querySelectorAll(
            "#modal-body .bulk-results tbody tr td:first-child",
          ),
        ].map((n) => n.textContent);
        state.history = rows.map((row) => ({
          ...row,
          version: currentVersion,
          item_id: selectedID,
          environment: "test",
        }));
        showProgress(true);
        const history = [
          ...document.querySelectorAll("#modal-body tbody tr td:first-child"),
        ].map((n) => n.textContent);
        return {
          original: rows.map((row) => row.database),
          progress,
          history,
          bulk,
        };
      });
    });
    const expected = [
      ["sp", "sp1", "sp2", "sp10", "sp10001"],
      [
        "newlink",
        "newlink2",
        "newlink10",
        "newlink_share",
        "newlink_share2",
        "newlink_share10",
      ],
    ];
    for (let index = 0; index < result.length; index++) {
      assert.deepEqual(result[index].progress, expected[index]);
      assert.deepEqual(result[index].history, expected[index]);
      assert.deepEqual(result[index].bulk, expected[index]);
      assert.notDeepEqual(result[index].original, expected[index]);
    }
    assert.deepEqual(errors, []);
    console.log(
      "PASS: provider/tenant/shared databases use numeric suffix order in progress and history; input unchanged.",
    );
  } finally {
    await browser.close();
  }
})().catch((error) => {
  console.error(error.message);
  process.exitCode = 1;
});
