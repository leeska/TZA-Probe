// WebKit 布局/滚动回归检查 —— Chromium 看不出这类问题（内核不同，历史上因此漏掉过「整页高度每秒抖动」）。
//
//   npx playwright install --with-deps webkit     # 只需一次
//   node scripts/layout-webkit.mjs https://probe.example.com/
//
// 判据：静置足够长时间（跨过多个 10 秒周期）后
//   1) 文档高度不得有任何变化（一旦变化，用户视线里的内容就会整体位移 = 跳）；
//   2) 被盯住的卡片在视口里的位置不得变化；
//   3) 顶部磁贴高度不得变化（磁贴里的数值曾因折行把整页顶高 33px）。
const PLAYWRIGHT = process.env.PLAYWRIGHT_MODULE || "playwright";
const { webkit, devices } = await import(PLAYWRIGHT);

const url = process.argv[2] || "http://127.0.0.1:25874/";
const watchMs = Number(process.argv[3] || 26000);

const browser = await webkit.launch();
const ctx = await browser.newContext({ ...devices["iPhone 13"], locale: "zh-CN" });
const page = await ctx.newPage();
const failures = [];

await page.goto(url, { waitUntil: "load", timeout: 60000 });
await page.waitForTimeout(4500);

const env = await page.evaluate(() => ({
  engine: navigator.userAgent,
  supportsOverflowAnchor: CSS.supports("overflow-anchor", "none"),
  docHeight: document.documentElement.scrollHeight,
}));

await page.evaluate(() => window.scrollTo(0, Math.min(3400, document.documentElement.scrollHeight - window.innerHeight - 20)));
await page.waitForTimeout(1200);

const start = await page.evaluate(() => {
  const cards = [...document.querySelectorAll(".node-card")];
  if (!cards.length) return null;
  const k = cards.find((c) => c.getBoundingClientRect().top >= -4)?.dataset.node ?? cards[0].dataset.node;
  const tiles = [...document.querySelectorAll(".overview-tile")].map((t) => Math.round(t.getBoundingClientRect().height));
  window.__log = [];
  window.__i = setInterval(() => {
    const c = document.querySelector('.node-card[data-node="' + k + '"]');
    if (!c) return;
    window.__log.push({
      top: Math.round(c.getBoundingClientRect().top),
      docH: document.documentElement.scrollHeight,
      tiles: [...document.querySelectorAll(".overview-tile")].map((t) => Math.round(t.getBoundingClientRect().height)).join(","),
    });
  }, 200);
  return { card: k, cardTop: Math.round(document.querySelector('.node-card[data-node="' + k + '"]').getBoundingClientRect().top), tiles: tiles.join(",") };
});

if (!start) {
  console.log("FAIL: 页面上没有公开卡片，无法检查布局稳定性");
  await browser.close();
  process.exit(1);
}

await page.waitForTimeout(watchMs);
const log = await page.evaluate(() => {
  clearInterval(window.__i);
  return window.__log;
});
await browser.close();

const docMoves = new Set(log.map((x) => x.docH));
const tileMoves = new Set(log.map((x) => x.tiles));
const topMoves = new Set(log.map((x) => x.top));

if (docMoves.size > 1) failures.push(`文档高度在 ${watchMs / 1000} 秒里变了 ${docMoves.size} 种：${[...docMoves].join(" / ")}（内容会整体位移）`);
if (tileMoves.size > 1) failures.push(`顶部磁贴高度变了 ${tileMoves.size} 种：${[...tileMoves].join(" / ")}`);
if (topMoves.size > 1) failures.push(`被盯卡片屏幕位置变了 ${topMoves.size} 种：${[...topMoves].join(" / ")}`);

if (failures.length) {
  console.log("FAIL（WebKit）:", env.engine.slice(0, 60));
  for (const f of failures) console.log("  -", f);
  process.exit(1);
}
console.log(
  `PASS: WebKit 布局稳定 —— ${watchMs / 1000} 秒内文档高度 ${[...docMoves]}, 磁贴 ${start.tiles}, 盯住卡片屏幕位置 ${start.cardTop}→${[...topMoves]}, 采样 ${log.length} 次（overflow-anchor 支持：${env.supportsOverflowAnchor}）`,
);