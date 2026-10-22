// 验证实际随 EXE 分发的资源，避免只测试 node_modules 而漏掉离线打包问题。
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const context = vm.createContext({ console });
for (const file of [
  "vendor/cronstrue/cronstrue.min.js",
  "vendor/cronstrue/zh_CN.min.js",
  "cron.js",
]) {
  vm.runInContext(
    fs.readFileSync(path.join(__dirname, "../../web/scripts", file), "utf8"),
    context,
  );
}

const cron = context.ReleaseCron;
for (const [expression, expected] of [
  ["0 0 10 * * ?", "每天 10:00"],
  ["0 0/5 * * * ?", "每隔 5 分钟"],
  ["0 0 3 ? * 2", "星期一"],
  ["0 3 * * 1", "星期一"],
  ["0 0 3 ? * MON", "星期一"],
  ["0 0 0 L * ?", "最后一天"],
  ["0 0 0 15W * ?", "工作日"],
  ["0 0 3 ? * MON#2", "第二个 星期一"],
  ["0 0 10 * * ? 2027", "2027"],
  ["  0  0  10 * * ?  ", "每天 10:00"],
]) {
  const result = cron.describe(expression);
  assert.equal(result.valid, true, expression);
  assert.ok(result.text.includes(expected), `${expression}: ${result.text}`);
}
for (const expression of [
  "0 99 10 * * ?",
  "0 0 24 * * ?",
  "0 0/0 * * * ?",
  "0 0 10 * * ?（每天10点）",
  "0 0 10 * * ? 2027 2028",
  "每天10点",
  "0 0",
  "<img src=x onerror=alert(1)>",
]) {
  assert.equal(cron.describe(expression).valid, false, expression);
}
assert.equal(cron.describe("").empty, true);
assert.equal(cron.display("0 0 10 * * ?"), "0 0 10 * * ?（每天 10:00）");
assert.equal(cron.display(""), "—");
console.log(
  "PASS: 离线 Cron 解释，5/6/7 段、星期编号、L/W/#、非法输入和表格括号文案。",
);
