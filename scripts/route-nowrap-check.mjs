// 回程格「居中且一行读完」的布局守卫：jsdom 不做布局，折行只有真浏览器看得见。
//
// 用户的硬要求：回程格居中，回程值必须完整显示、不折行（11px 的值 + 11px 的运营商标志 +
// 4px 间距，一格内容 93px，最长的「10099 → 9929」是 89.4px，余 3.6px —— 字号、内边距、
// 间距或标志尺寸动大一点就会折行）。这个脚本量真实页面：每个 .route-label 折了几行、
// 需要多少宽度、格子给了多少、是不是居中。任何一格折行或不是居中，退出码 1。
//
// 折行怎么数（踩过）：`.route-label` 是 flex-wrap，装不下时**整个值**（连同标志）落到第二行，
// 值自己仍然只有一行高——只量 strong 的高度永远得到「1 行」。要按格子的内容高度数：
// content = clientHeight - paddingTop - paddingBottom（clientHeight 不含边框），
// 再除以行高；同时用「标志顶边与值顶边之差」兜底（同一行里两者差约 3px，换行后差一整行）。
//
// 用法：
//   node scripts/route-nowrap-check.mjs                              # 默认量线上 probe.tza.cc
//   TZA_CHECK_URL=http://127.0.0.1:25911/ node scripts/route-nowrap-check.mjs
//   环境变量：WIDTH（默认 390，最窄的常用手机宽度）、PUPPETEER_CORE、
//            CHROME_PATH（默认 /usr/bin/chromium）
//   · 页面上没有回程格（例如回归用的合成库里那台机器没有探测任务）→ 打印 SKIP 并以 0 退出。
//   · 没有 puppeteer-core / chromium → 打印 SKIP 并以 0 退出。
//
// 为什么不做「把 styles.css 挂到空白页量」那种合成模式：同一段文字在那种环境里量出来比真实
// 页面宽约 11px（字体回退差别），会误报折行——只有量真实页面才可信。
//
// 线上实测（2026-10-01，390px、17 台 21 格，圆点时代）：折行 0，最紧一格「10099 → 9929」需要 85.4px、可用 89px。
import { existsSync } from "node:fs";

const CORE = process.env.PUPPETEER_CORE ||
  "/tmp/pptr/node_modules/puppeteer-core/lib/puppeteer/puppeteer-core.js";
const CHROME = process.env.CHROME_PATH || "/usr/bin/chromium";
const URL_ = process.env.TZA_CHECK_URL || "https://probe.tza.cc/";
const WIDTH = Number(process.env.WIDTH || 390);

if (!existsSync(CORE) || !existsSync(CHROME)) {
  console.log(`SKIP: 没有 puppeteer-core（${CORE}）或 chromium（${CHROME}）——布局守卫只在有真浏览器的机器上跑`);
  process.exit(0);
}

const puppeteer = (await import(CORE)).default;
const browser = await puppeteer.launch({
  executablePath: CHROME,
  headless: true,
  args: ["--no-sandbox", "--hide-scrollbars"],
  defaultViewport: { width: WIDTH, height: 1400, deviceScaleFactor: 2 },
});
const page = await browser.newPage();
await page.goto(URL_, { waitUntil: "networkidle2" });
await page.waitForSelector(".node-card", { timeout: 30000 });
await new Promise((r) => setTimeout(r, 2500));

const rows = await page.evaluate(() =>
  [...document.querySelectorAll(".node-card .route-label")].map((chip) => {
    const cs = getComputedStyle(chip);
    const strong = chip.querySelector("strong");
    const mark = chip.querySelector(".carrier-mark, .carrier-dot");
    const gap = parseFloat(cs.columnGap) || 0;
    // 克隆一份不换行的 strong 量自然宽度（-webkit-line-clamp 下 scrollWidth 会骗人）
    const clone = strong.cloneNode(true);
    clone.style.cssText = "white-space:nowrap;position:absolute;visibility:hidden;width:auto";
    chip.appendChild(clone);
    const valueWidth = clone.getBoundingClientRect().width;
    clone.remove();
    const markWidth = mark ? mark.getBoundingClientRect().width : 0;
    const avail = chip.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight);
    const lh = parseFloat(getComputedStyle(strong).lineHeight) || 15;
    // 折行按格子的内容高度数（flex-wrap 会把整个值挪到第二行，值自身仍是一行高），
    // 并用「标志与值是否同一行」兜底。
    const content = chip.clientHeight - parseFloat(cs.paddingTop) - parseFloat(cs.paddingBottom);
    const sameRow = mark ? Math.abs(strong.getBoundingClientRect().top - mark.getBoundingClientRect().top) <= 8 : true;
    return {
      carrier: chip.dataset.focusCarrier || "?",
      value: strong.textContent.trim(),
      avail: +avail.toFixed(1),
      need: +(valueWidth + markWidth + gap).toFixed(1),
      lines: Math.max(1, Math.round(content / lh)),
      sameRow,
      centered: cs.justifyContent === "center",
    };
  }),
);
await browser.close();

if (!rows.length) {
  console.log(`SKIP: ${URL_} 上没有回程格（这台机器的探测计划里没有回程任务）`);
  process.exit(0);
}

const worst = rows.slice().sort((a, b) => a.avail - a.need - (b.avail - b.need))[0];
const wrapped = rows.filter((r) => r.lines > 1 || !r.sameRow);
const offCenter = rows.filter((r) => !r.centered);
console.log(
  `回程格 ${rows.length} 个 @${WIDTH}px：折行 ${wrapped.length} 个、非居中 ${offCenter.length} 个；最紧的一格「${worst.value}」需要 ${worst.need}px、可用 ${worst.avail}px（余 ${(worst.avail - worst.need).toFixed(1)}px）`,
);
for (const r of wrapped) {
  console.log(`  ✗ ${r.carrier} 「${r.value}」折了 ${r.lines} 行：需要 ${r.need}px，可用 ${r.avail}px`);
}
for (const r of offCenter) console.log(`  ✗ ${r.carrier} 「${r.value}」不是居中`);

if (wrapped.length || offCenter.length) {
  console.error("FAIL: 回程格必须居中、且回程值完整显示不折行");
  process.exit(1);
}
console.log("PASS: 回程格居中、值一行读完");
