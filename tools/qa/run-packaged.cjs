// 使用真实 EXE 的隔离数据目录验证界面，云端连接和密钥在回归脚本中模拟。
const fs = require("node:fs");
const path = require("node:path");
const cp = require("node:child_process");
const assert = require("node:assert/strict");
const { setTimeout: delay } = require("node:timers/promises");
const executable = path.resolve(process.argv[2]);
fs.mkdirSync(path.resolve(".work/qa"), { recursive: true });
const directory = fs.mkdtempSync(path.resolve(".work/qa/packaged-"));
const child = cp.spawn(executable, ["--serve", "--data-dir", directory], {
  windowsHide: true,
  stdio: ["ignore", "pipe", "pipe"],
});
let output = "";
const addressPattern = new RegExp("http://127[.]0[.]0[.]1:[0-9]+");
let launchError;
child.stdout.on("data", (data) => {
  output += data;
});
child.on("error", (error) => {
  launchError = error;
});
(async () => {
  try {
    // 只等待当前隔离进程，不发现或结束用户的正常桌面程序。
    for (
      let attempt = 0;
      attempt < 600 && !addressPattern.test(output);
      attempt++
    ) {
      if (launchError) throw launchError;
      if (child.exitCode !== null) throw new Error("隔离程序提前退出");
      await delay(100);
    }
    const url = output.match(addressPattern)?.[0];
    assert.ok(url, "隔离程序未在限定时间内启动");
    const scripts = process.argv.slice(3);
    // 仅运行明确列出的界面场景，不调用真实 AI、数据库或初始化接口。
    for (const script of scripts.length
      ? scripts
      : ["settings-and-version-picker.cjs"]) {
      assert.match(script, /^[a-z0-9-]+\.cjs$/);
      const result = cp.spawnSync(
        process.execPath,
        [path.join(__dirname, script), url],
        {
          windowsHide: true,
          encoding: "utf8",
          timeout: 90000,
        },
      );
      process.stdout.write(result.stdout || "");
      process.stderr.write(result.stderr || "");
      if (result.error) throw result.error;
      assert.equal(result.status, 0, script);
    }
  } finally {
    // 无论测试成功或失败，都只释放本工具创建的子进程。
    child.kill();
  }
})().catch((error) => {
  console.error(error.message);
  process.exitCode = 1;
});
