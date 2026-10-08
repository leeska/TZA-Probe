// 卡片里的文字不许越过卡片右边框——jsdom 不做布局，这种事只有真浏览器量真实页面才看得见。
//
// 起因（2026-10-08 用户截图「边界溢出了」）：`.metrics-grid` 的列写的是 `1fr 1fr`，而 `1fr`
// 的最小值是内容的 min-content；「近 24 小时均值 … · 阈值 …」这行降速说明当时是 nowrap，
// 那一列于是永远不窄于整句话（实测 199px）。卡片内容一小于 199+79+18（约 296px，也就是
// 视口 ≤ 360px 的常见手机），整个格子被顶出卡片——卡片是 `overflow: hidden`，靠右的
// 「33.4%」「8.7 GB」就被边框切掉，看起来像数字缺了一半。修复：列改 `minmax(0, 1fr)`，
// 降速说明允许折行。这个脚本按几个真实手机宽度逐张卡片量，把越界的元素列出来。
//
// 用法：
//   node scripts/card-overflow-check.mjs                     # 默认量线上 probe.tza.cc
//   TZA_CHECK_URL=http://127.0.0.1:25911/ node scripts/card-overflow-check.mjs
//   环境变量：WIDTHS（逗号分隔，默认 320,344,360,375,390,414）、PUPPETEER_CORE、
//            CHROME_PATH（默认 /usr/bin/chromium）、TZA_SKIN、TZA_THEME
//   · 没有 puppeteer-core / chromium → 打印 SKIP 并以 0 退出。
import { existsSync } from "node:fs";

const CORE = process.env.PUPPETEER_CORE ||
  "/tmp/pptr/node_modules/puppeteer-core/lib/puppeteer/puppeteer-core.js";
const CHROME = process.env.CHROME_PATH || "/usr/bin/chromium";
const URL_ = process.env.TZA_CHECK_URL || "https://probe.tza.cc/";
const WIDTHS = (process.env.WIDTHS || "320,344,360,375,390,414").split(",").map((v) => Number(v.trim()));
const SKIN = process.env.TZA_SKIN || "glass";
const THEME = process.env.TZA_THEME || "light";

if (!existsSync(CORE) || !existsSync(CHROME)) {
  console.log(`SKIP: 没有 puppeteer-core（${CORE}）或 chromium（${CHROME}）——布局守卫只在有真浏览器的机器上跑`);
  process.exit(0);
}
const { default: puppeteer } = await import(CORE);
const browser = await puppeteer.launch({ executablePath: CHROME, args: ["--no-sandbox", "--disable-dev-shm-usage"], headless: "new" });
const page = await browser.newPage();
await page.setBypassCSP(true);

const problems = [];
const report = [];
for (const width of WIDTHS) {
  await page.setViewport({ width, height: 900, deviceScaleFactor: 2 });
  // 皮肤 / 明暗各套都量：三种皮肤的圆角与内边距不同，混着才拦得住某一套特有的事。
  await page.evaluateOnNewDocument((skin, theme) => {
    try {
      localStorage.setItem("tza-skin", skin);
      localStorage.setItem("tza-theme", theme);
    } catch {}
  }, SKIN, THEME);
  await page.goto(URL_, { waitUntil: "networkidle2", timeout: 90000 });
  await page.waitForFunction(() => document.querySelectorAll(".node-card").length >= 1, { timeout: 60000 });
  await new Promise((r) => setTimeout(r, 900));
  const found = await page.evaluate(() => {
    const bad = [];
    const cols = [];
    for (const c of document.querySelectorAll(".node-card")) {
      const cs = getComputedStyle(c);
      const cr = c.getBoundingClientRect();
      const inner = cr.right - parseFloat(cs.paddingRight) - parseFloat(cs.borderRightWidth) + 0.5;
      const name = c.querySelector(".node-name")?.textContent.trim() || "?";
      const grid = c.querySelector(".metrics-grid");
      if (grid) cols.push([...grid.children].map((cell) => Math.round(cell.getBoundingClientRect().width)).join("/"));
      for (const el of c.querySelectorAll("*")) {
        if (getComputedStyle(el).position === "fixed") continue;
        const r = el.getBoundingClientRect();
        if (!r.width && !r.height) continue;
        const over = r.right - inner;
        if (over > 0.5) {
          bad.push({
            name,
            cls: typeof el.className === "string" && el.className ? "." + el.className.split(/\s+/).join(".") : el.tagName.toLowerCase(),
            text: (el.textContent || "").trim().slice(0, 24),
            over: Math.round(over * 10) / 10,
            selfScroll: el.scrollWidth - el.clientWidth,
          });
        }
      }
    }
    return { bad: bad.slice(0, 20), total: bad.length, cols: [...new Set(cols)] };
  });
  report.push(`${width}px: 越界 ${found.total} 个${found.cols.length ? ` · 指标格宽 ${found.cols.join(" | ")}` : ""}`);
  for (const b of found.bad) problems.push(`${width}px ${b.name} ${b.cls} 超出边框 ${b.over}px（自身滚动 ${b.selfScroll}px）文本="${b.text}"`);
}
await browser.close();

console.log(report.join("\n"));
if (problems.length) {
  console.log(`\nFAIL: ${problems.length} 处文字越过卡片右边框（会被 overflow: hidden 切掉）`);
  for (const p of problems) console.log("  " + p);
  process.exit(1);
}
console.log("\nPASS: 各宽度下卡片里的文字都在边框内");
