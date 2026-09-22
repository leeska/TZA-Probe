import assert from "node:assert/strict";
import { chart } from "../web/charts.js";
const samples = [0, 60, 3600, 3660].map((at) => ({
  at,
  metrics: { cpu: 25, memory_used: 50, memory_total: 100 },
}));
const html = chart(samples, "cpu", "CPU", "teal", 1);
assert.equal(
  (html.match(/<polyline /g) || []).length,
  2,
  "offline gap must not be bridged",
);
assert(
  html.includes("11.1,84.5"),
  "timestamps must determine horizontal spacing",
);
assert(html.includes("680.0,84.5"));
assert(
  !chart([], "cpu", "<script>").includes("<script>"),
  "labels must be escaped",
);
console.log(
  "PASS: time-proportional chart, offline gaps, empty history and escaped labels.",
);

const rates = [0,60,120].map((at,i)=>({at,metrics:{rate_in:[1000,null,2000][i]}}));
const rateHTML = chart(rates,"rate_in","下载");
assert(rateHTML.includes("/s"));
assert(!rateHTML.includes("NaN"));
assert.equal((rateHTML.match(/<polyline /g)||[]).length,2,"unknown samples must break lines");
const failures = [0,60].map(at=>({at,metrics:{latency:null}}));
assert(!chart(failures,"latency","延迟").includes("<polyline"),"failed connections must not become zero latency");
const {installCommand} = await import("../web/agent-setup.js");
const command = installCommand("https://probe.example.com", "a".repeat(64));
assert(command.startsWith("curl -fsSL https://probe.example.com/install.sh | sh -s -- "));
assert(command.includes("--endpoint https://probe.example.com") && command.includes("--token " + "a".repeat(64)));
assert(!command.includes("?token="), "the key must be a script argument, never part of a URL");
console.log("PASS: rate units, unknown latency gaps and one-line install command.");

// 横坐标细分：首尾之间取多个时刻，日期与时间分行；悬停数据挂在 figure 上。
const { axisLabels, colorClass, formatValue } = await import("../web/charts.js");
const ticks = axisLabels(0, 4 * 3600, 5);
assert.equal(ticks.length, 5, "the axis must subdivide into five ticks");
assert(ticks.every((t) => t.date && t.time), "every tick carries a date line and a time line");
assert.notEqual(ticks[0].time, ticks[4].time, "ticks must advance across the range");
assert.equal((rateHTML.match(/<b>\d+\/\d+<\/b><i>\d\d:\d\d<\/i>/g) || []).length, 5, "labels must split date and time onto two lines");
assert(rateHTML.includes("data-series=") && rateHTML.includes("data-range=") && rateHTML.includes("data-ceiling="), "charts must carry the hover payload");
assert(JSON.parse(rateHTML.match(/data-series="([^"]*)"/)[1].replaceAll("&quot;", '"'))[0].points.length === 3, "the payload keeps every sample, gaps included");
assert.equal(colorClass(0), "lc0");
assert.equal(colorClass(9), "lc1", "the palette wraps around");
assert.equal(formatValue(null, "ms"), "—", "a missing value must not print as a number");
assert.equal(formatValue(12.34, "ms"), "12.3 ms");
assert.equal(formatValue(42, "%"), "42%");

// 计费表单的 UTC 时刻：显示格式带秒，接口格式为 RFC3339，两者必须能往返。
const { stamp, stampISO } = await import("../web/format.js");
assert.equal(stamp("2026-09-01T12:30:45Z"), "2026-09-01 12:30:45", "显示格式精确到秒");
assert.equal(stamp(Math.floor(Date.parse("2026-09-01T12:30:45Z") / 1000)), "2026-09-01 12:30:45", "秒时间戳同样按 UTC 显示");
assert.equal(stamp(""), "");
assert.equal(stamp("不是时间"), "", "无法解析的值不能渲染成 NaN 字符串");
assert.equal(stampISO("2026-09-01 12:30:45"), "2026-09-01T12:30:45Z");
assert.equal(stampISO("2026-09-01T12:30:45Z"), "2026-09-01T12:30:45Z", "RFC3339 输入也能接受");
assert.equal(stampISO("2026-09-01 12:30"), "2026-09-01T12:30:00Z", "省略秒按 00 处理");
assert.throws(() => stampISO("2026-09-01"), /YYYY-MM-DD/, "缺时刻要报错");
assert.throws(() => stampISO("2026-02-31 00:00:00"), /不存在/, "2 月 31 日必须挡住，不能被 Date 顺延");
assert.throws(() => stampISO(""), /YYYY-MM-DD/);
console.log("PASS: subdivided two-line time axis, hover payload and palette classes.");
