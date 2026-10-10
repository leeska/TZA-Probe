import assert from "node:assert/strict";
import { chart } from "../web/charts.js";
import { fold, searchKeys, searchMatch } from "../web/search.js";
import { osLabel, gib, bytes } from "../web/format.js";
// 系统标签的取数：有发行版就写发行版（Agent 读 /etc/os-release），老 Agent 只有 GOOS。
assert.equal(osLabel({ metrics: { distro: "Debian 13", os: "linux" } }), "Debian 13");
assert.equal(osLabel({ metrics: { distro: "Alpine 3.21", os: "linux" } }), "Alpine 3.21");
assert.equal(osLabel({ metrics: { os: "linux" } }), "linux", "老 Agent 退回 runtime.GOOS");
assert.equal(osLabel({ metrics: { distro: "Debian 13" } }, "—"), "Debian 13", "详情页的兜底不影响发行版");
assert.equal(osLabel({ metrics: { os: "linux" }, online: true }, "—"), "linux");
assert.equal(osLabel({ metrics: null, online: false }), "等待接入");
assert.equal(osLabel({ metrics: {}, online: true }), "在线");
assert.equal(osLabel({ metrics: {} }, "—"), "—", "详情页自己给兜底文案");
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
// 磁盘 / 内存按 1024 换算（gib），流量仍按 1000（bytes）——两套口径各管一摊，
// 混回一个函数就会出现「100 GiB 的盘写成 105 GB」这种与 df -h、商家规格对不上的数字。
assert.equal(gib(0), "0 B");
assert.equal(gib(1), "1 B");
assert.equal(gib(1024), "1.0 KiB");
assert.equal(gib(1048576), "1.0 MiB");
assert.equal(gib(8333946880), "7.8 GiB", "8 GB 的内存要写成 7.8 GiB（free -h 同值）");
assert.equal(gib(105467994112), "98.2 GiB", "100 GiB 的盘不许写成 105 GB");
assert.equal(gib(1099511627776), "1.0 TiB");
assert.equal(bytes(105467994112), "105 GB", "流量那套仍是十进制");
assert.equal(bytes(1099511627776), "1.1 TB");
assert.equal(gib(NaN), "—");
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
assert.equal((rateHTML.match(/<span><b>[^<]*<\/b><i>[^<]*<\/i><\/span>/g) || []).length, 5, "labels must split date and time onto two lines");
// 同一天只在第一格写日期；几分钟的范围里落在同一分钟的相邻刻度不重复写时间。
const axisTimes = [...rateHTML.matchAll(/<span><b>([^<]*)<\/b><i>([^<]*)<\/i><\/span>/g)].map((m) => [m[1], m[2]]);
assert(/^\d+\/\d+$/.test(axisTimes[0][0]), "the first tick carries the date");
assert(axisTimes.slice(1).every(([d]) => d === "&nbsp;"), "same-day ticks must not repeat the date");
const shownTimes = axisTimes.map(([, t]) => t).filter((t) => t !== "&nbsp;");
assert(shownTimes.every((t, i) => i === 0 || t !== shownTimes[i - 1]), "adjacent ticks must not repeat the same minute");
assert(rateHTML.includes("data-series=") && rateHTML.includes("data-range=") && rateHTML.includes("data-ceiling="), "charts must carry the hover payload");
// 窄屏下盒子宽度远小于 viewBox 的 680：默认的 preserveAspectRatio="meet" 会把绘图区
// 等比缩小后垂直居中，折线和网格线就落不到 8/59/110 三条刻度线上。
assert(/preserveAspectRatio="none"/.test(html), "charts must stretch the plot to their box, not letterbox it");
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

// 筛选：中文、全拼、拼音首字母、英文与代号都要命中（web/search.js）。
for (const [text, query] of [
  ["香港", "香港"], ["香港", "xianggang"], ["香港", "XG"], ["香港", "hk"], ["香港", "hongkong"], ["香港", "Hong Kong"],
  ["河北", "河北"], ["河北", "hebei"], ["河北", "hb"], ["湖北", "hb"],
  ["四川", "sc"], ["四川", "sichuan"], ["重庆", "cq"], ["黑龙江", "hlj"], ["内蒙古", "nmg"],
  ["上海", "上海"], ["上海", "shanghai"], ["深圳", "shenzhen"],
  ["电信", "电信"], ["电信", "dianxin"], ["电信", "dx"], ["电信", "ct"], ["电信", "telecom"],
  ["联通", "cu"], ["联通", "liantong"], ["联通", "unicom"], ["移动", "cmcc"], ["移动", "yd"],
  ["美国", "us"], ["美国", "usa"], ["美国", "meiguo"], ["美国", "united states"], ["美国", "los angeles"],
  ["新加坡", "sg"], ["日本", "jp"], ["德国", "de"], ["英国", "uk"], ["加拿大", "ca"], ["澳大利亚", "au"],
])
  assert(searchMatch(query, text), `「${query}」应命中「${text}」`);
for (const [text, query] of [
  ["香港", "beijing"], ["香港", "us"], ["电信", "cu"], ["美国", "jp"], ["河北", "xianggang"], ["河北", "nmg"],
])
  assert(!searchMatch(query, text), `「${query}」不该命中「${text}」`);
// 目录里的「地区 / 运营商」和地址串在一起时，三者任一处命中都算命中。
assert(searchMatch("hb ct", "河北 / 电信", "219.148.62.1"));
assert(searchMatch("219.148", "河北 / 电信", "219.148.62.1"));
assert(!searchMatch("hb cu", "河北 / 电信", "219.148.62.1"), "空格分隔的关键词是 AND");
assert(searchMatch("", "河北 / 电信"), "空关键词保留全部");
assert(searchMatch("  ", "河北 / 电信"));
assert(searchMatch("hb/ct", "河北 / 电信"), "斜杠也算词间分隔");
assert(!searchMatch("河北", "香港", "1.1.1.1"));
// 拼音表外的汉字退回纯文本匹配，不会因为查不到拼音而误命中。
assert(searchMatch("鲸落机房", "鲸落机房 香港"));
assert(!searchMatch("jingluojifang", "鲸落机房 香港"));
assert.equal(fold("Hong Kong / 香港"), "hongkong香港");
assert(searchKeys("河北 / 电信").includes("hebeidianxin"), "全拼");
assert(searchKeys("河北 / 电信").includes("hbdx"), "首字母");
assert(searchKeys("河北 / 电信").includes("ct"), "代号");
console.log("PASS: catalog search matches Chinese, pinyin, initials, English and carrier codes.");

// 解锁检测面板（web/unlock.js）：分数、分类统计、状态词表与 region_only 的处理都是纯输出，
// 这里直接断言 HTML 字符串；真实数据那一层在 ui-smoke 里跑。
const { unlockPanel, unlockBadge, unlockCategoryStats, unlockStateCell, unlockStateName, unlockCounts } =
  await import("../web/unlock.js");
const unlockCatalog = {
  categories: ["流媒体", "AI", "交易所", "其他"],
  services: [
    { id: "netflix", name: "Netflix", category: "流媒体" },
    { id: "disneyplus", name: "Disney+", category: "流媒体" },
    { id: "openai", name: "ChatGPT", category: "AI" },
    { id: "bybit", name: "Bybit", category: "交易所" },
    { id: "bing", name: "Bing Region", category: "其他", region_only: true },
    { id: "reddit", name: "Reddit", category: "其他" },
  ],
};
const unlockSummary = {
  services: [
    { service: "netflix", status: "originals_only", region: "JP" },
    { service: "disneyplus", status: "no" },
    { service: "openai", status: "yes" },
    { service: "bybit", status: "banned" },
    { service: "bing", status: "yes", region: "Japan" },
    { service: "reddit", status: "failed" },
  ],
  score: 3,
  total: 6,
  tested_at: 1759400000,
};
// 分类统计的分母固定取目录项数，分子是已解锁（yes + originals_only）。
assert.deepEqual(unlockCategoryStats(unlockSummary, unlockCatalog), [
  { category: "流媒体", unlocked: 1, total: 2 },
  { category: "AI", unlocked: 1, total: 1 },
  { category: "交易所", unlocked: 0, total: 1 },
  { category: "其他", unlocked: 1, total: 2 },
]);
assert(unlockCounts("yes") && unlockCounts("originals_only"), "已解锁与仅自制剧都计分");
assert(!unlockCounts("no") && !unlockCounts("banned") && !unlockCounts("failed"), "其余状态不计分");
assert.equal(unlockStateName.banned, "IP 被封禁");
assert.equal(unlockStateName.failed, "检测失败");
const unlockHTML = unlockPanel(unlockSummary, unlockCatalog, { tested: true });
assert(/unlock-title">解锁检测</.test(unlockHTML) && /unlock-score">3\/6</.test(unlockHTML), "标题行给「解锁检测」+ 分数");
assert(unlockHTML.includes("流媒体 1/2 · AI 1/1 · 交易所 0/1 · 其他 1/2"), "分类统计按目录分母: " + unlockHTML);
assert(unlockHTML.includes("Netflix") && unlockHTML.includes("仅自制剧"), "originals_only 写「仅自制剧」");
assert(unlockHTML.includes("IP 被封禁") && unlockHTML.includes("检测失败") && unlockHTML.includes("未解锁"), "状态词表齐全");
assert(unlockHTML.includes("检测于 "), "tested 为真时给检测时间");
assert(!unlockPanel(unlockSummary, unlockCatalog).includes("检测于"), "卡片浮层不带检测时间");
// region_only 只给地区 / 币种文字：不写「未解锁」，也不画锁。
const bing = unlockStateCell({ service: "bing", status: "yes", region: "Japan" }, unlockCatalog.services.find((s) => s.id === "bing"));
assert(bing.includes("Japan") && !bing.includes("已解锁") && !bing.includes("unlock-lock"), "region_only 只给地区文字、不画锁: " + bing);
// 已解锁画绿锁（图标走 class，颜色由 styles.css 决定）。
const openai = unlockStateCell({ service: "openai", status: "yes" }, unlockCatalog.services.find((s) => s.id === "openai"));
assert(openai.includes("unlock-lock") && openai.includes("已解锁"), "已解锁要带锁图标");
assert(unlockBadge({ id: "n1" }) === "", "没有解锁数据的节点不渲染徽标（不留占位）");
const unlockBadgeHTML = unlockBadge({ id: "n1", name: "jp-x", unlock: { score: 19, total: 24 } });
assert(unlockBadgeHTML.includes('data-unlock="n1"') && unlockBadgeHTML.includes("19/24"), "有数据才给徽标与分数");
console.log("PASS: unlock panel score, catalogue-based category stats, status words and region_only text.");
