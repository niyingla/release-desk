// 隔离本机服务，所有公网更新请求均模拟；不会下载程序或读取用户业务记录。
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
    opens = [];
  let checks = 0;
  let update = {
    current: "1.9.0",
    checking: false,
    available: false,
    latest: "",
    note: "",
    checked_at: "",
    release_url: "",
    download_url: "",
    error: "",
  };
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/api/state", async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    body.data.software_update = update;
    await route.fulfill({ response, json: body });
  });
  await page.route("**/api/update/check", (route) => {
    checks++;
    update.checking = true;
    return route.fulfill({ json: { data: update } });
  });
  await page.route("**/api/update/open", (route) => {
    opens.push(route.request().postDataJSON());
    return route.fulfill({ json: { data: null } });
  });
  try {
    await page.goto(process.argv[2]);
    await page
      .getByRole("heading", { name: "版本总览", exact: true })
      .waitFor();
    await page.locator('[data-action="software-update"]').click();
    await page
      .getByRole("heading", { name: "软件更新", exact: true })
      .waitFor();
    await page.waitForFunction(
      () =>
        document.querySelector('[data-action="check-software-update"]')
          ?.disabled,
    );
    assert.equal(checks, 1);
    update = {
      ...update,
      checking: false,
      available: true,
      latest: "2.0.0",
      checked_at: "2026-10-22T14:00:00Z",
      release_url:
        "https://github.com/niyingla/release-desk/releases/tag/v2.0.0",
      download_url:
        "https://github.com/niyingla/release-desk/releases/download/v2.0.0/ReleaseDesk-2.0.0.exe",
      note: "新版说明 <script>window.injectedUpdate=true</script>",
    };
    await page.evaluate(() => refresh(true));
    assert.match(
      await page.locator("#update-button").innerText(),
      /发现新版 2.0.0/,
    );
    assert.match(
      await page.locator("#modal-body").innerText(),
      /新版说明 <script>/,
    );
    assert.equal(await page.evaluate(() => window.injectedUpdate), undefined);
    await page.locator('[data-action="download-software-update"]').click();
    await page.locator('[data-action="open-software-release"]').click();
    assert.deepEqual(opens, [{ kind: "download" }, { kind: "release" }]);
    const output = path.resolve(__dirname, "../../.work/qa/artifacts");
    fs.mkdirSync(output, { recursive: true });
    await page.screenshot({ path: path.join(output, "software-updates.png") });
    update = {
      ...update,
      available: false,
      latest: "1.9.0",
      note: "当前正式版",
      download_url: "",
    };
    await page.evaluate(() => refresh(true));
    assert.equal(
      await page.locator('[data-action="download-software-update"]').count(),
      0,
    );
    update.error = "暂时无法检查更新，请稍后重试";
    await page.evaluate(() => refresh(true));
    assert.match(
      await page.locator("#modal-body").innerText(),
      /暂时无法检查更新/,
    );
    await page.locator('.modal-foot [data-action="close-modal"]').click();
    assert.equal(await page.locator("#modal-root").innerText(), "");
    assert.deepEqual(errors, []);
    console.log(
      "UI PASS: 后台检查、版本提示、下载/说明入口、同版不更新、断网可关闭和远端正文转义；浏览器错误 0。",
    );
  } finally {
    await page.unrouteAll({ behavior: "ignoreErrors" });
    await browser.close();
  }
})().catch((error) => {
  console.error(String(error.message));
  process.exitCode = 1;
});
