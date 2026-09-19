// 真实浏览器视觉验收：对公开页、详情弹窗和后台截图，桌面与手机各一份。
// 仅针对独立 loopback 数据目录运行，不创建或修改任何数据。
//
//   TZA_UI_DEPS=/tmp/tza-ui-deps TZA_TEST_URL=http://127.0.0.1:25874 \
//   TZA_TEST_ADMIN_FILE=/tmp/tza-ui-data/admin-token TZA_SHOT_DIR=/tmp/tza-shots \
//   node scripts/visual-check.mjs
//
// 依赖 playwright（npm install --prefix $TZA_UI_DEPS playwright）。若不想让 Playwright
// 下载浏览器，可用 TZA_CHROME 指向已有的 Chromium / headless shell 可执行文件。
import { createRequire } from "node:module";
import { readFile, mkdir } from "node:fs/promises";

const deps = process.env.TZA_UI_DEPS;
if (!deps) throw new Error("TZA_UI_DEPS 未设置");
const { chromium } = createRequire(deps + "/package.json")("playwright");

const base = process.env.TZA_TEST_URL || "http://127.0.0.1:25874";
const token = (await readFile(process.env.TZA_TEST_ADMIN_FILE, "utf8")).trim();
const out = process.env.TZA_SHOT_DIR || "/tmp/tza-shots";
await mkdir(out, { recursive: true });

const browser = await chromium.launch(
  process.env.TZA_CHROME ? { executablePath: process.env.TZA_CHROME } : {},
);
const errors = [];

async function open(viewport, theme) {
  const ctx = await browser.newContext({ viewport, deviceScaleFactor: 1 });
  // 通过与 theme.js 相同的 localStorage 键预设主题，等价于用户手动切换后的刷新。
  // 初始化脚本每次导航都会执行，只在尚未设置时写入，以便验证切换后的保持。
  if (theme) await ctx.addInitScript((t) => localStorage.getItem("tza-theme") || localStorage.setItem("tza-theme", t), theme);
  const page = await ctx.newPage();
  page.on("pageerror", (e) => errors.push(`[pageerror] ${e.message}`));
  page.on("console", (m) => {
    // 后台未登录时的 /session 探测返回 401，属预期。
    if (m.type() === "error" && !/401/.test(m.text())) errors.push(`[console] ${m.text()}`);
  });
  return { ctx, page };
}

async function shot(page, name, fullPage = true) {
  await page.waitForTimeout(800);
  await page.screenshot({ path: `${out}/${name}.png`, fullPage });
}

{
  const { ctx, page } = await open({ width: 1440, height: 1000 });
  await page.goto(base + "/");
  await page.waitForSelector(".node-card");
  await shot(page, "public-desktop");
  const online = await page.$(".node-card.is-online .node-name");
  await (online || (await page.$(".node-card .node-name"))).click();
  await page.waitForTimeout(1000);
  await shot(page, "public-detail", false);
  await ctx.close();
}
{
  const { ctx, page } = await open({ width: 390, height: 844 });
  await page.goto(base + "/");
  await page.waitForSelector(".node-card");
  await shot(page, "public-mobile");
  await ctx.close();
}
{
  const { ctx, page } = await open({ width: 1440, height: 1000 });
  await page.goto(base + "/admin");
  await page.waitForSelector("#login");
  await shot(page, "admin-login", false);
  await page.fill("#login [name=token]", token);
  await page.click("#login button");
  await page.waitForSelector(".admin-layout");
  await shot(page, "admin-nodes");
  // 弹窗：只打开并截图，随后按 Esc 关闭，不提交任何表单。
  const dialog = async (opener, name) => {
    await page.click(opener);
    await page.waitForSelector("#dialog[open]");
    await page.waitForTimeout(600);
    await shot(page, name, false);
    await page.keyboard.press("Escape");
    await page.waitForFunction(() => !document.querySelector("#dialog").open);
  };
  await dialog("[data-edit]", "modal-node-edit");
  await dialog("[data-token]", "modal-token-confirm");
  await page.click("[data-tab=tasks]");
  await shot(page, "admin-tasks");
  await dialog("#add-task", "modal-task-new");
  if (await page.$("[data-edit-task]")) {
    await dialog("[data-edit-task]", "modal-task-edit");
    await dialog("[data-runs-task]", "modal-task-runs");
    await dialog("[data-bind-task]", "modal-task-bind");
  }
  await page.click("[data-tab=alerts]");
  await shot(page, "admin-alerts");
  await dialog("#configure-webhook", "modal-webhook");
  await page.setViewportSize({ width: 390, height: 844 });
  await shot(page, "admin-mobile-alerts");
  await page.click("[data-tab=tasks]");
  await dialog("#add-task", "modal-task-new-mobile");
  await page.click("[data-tab=nodes]");
  await shot(page, "admin-mobile");
  await page.click("[data-tab=tasks]");
  await shot(page, "admin-mobile-tasks");
  // 窄屏表格应堆叠而非横向滚动
  const overflow = await page.evaluate(() => [...document.querySelectorAll(".table-scroll")].some((el) => el.scrollWidth > el.clientWidth + 1));
  if (overflow) throw new Error("手机宽度下后台表格仍需横向滚动");
  await ctx.close();
}
// 深色主题：公开页与后台各一张，并验证切换按钮循环与刷新后保持。
{
  const { ctx, page } = await open({ width: 1440, height: 1000 }, "dark");
  await page.goto(base + "/");
  await page.waitForSelector(".node-card");
  const theme = () => page.evaluate(() => document.documentElement.dataset.theme);
  if ((await theme()) !== "dark") throw new Error("预设深色主题未生效");
  await shot(page, "public-desktop-dark");
  await page.click(".public-header [data-theme-toggle]"); // dark -> auto
  await page.click(".public-header [data-theme-toggle]"); // auto -> light
  if ((await theme()) !== "light") throw new Error("主题切换按钮未切到浅色");
  await page.reload();
  await page.waitForSelector(".node-card");
  if ((await theme()) !== "light") throw new Error("主题偏好刷新后未保持");
  await ctx.close();
}
{
  const { ctx, page } = await open({ width: 1440, height: 1000 }, "dark");
  await page.goto(base + "/admin");
  await page.waitForSelector("#login");
  await shot(page, "admin-login-dark", false);
  await page.fill("#login [name=token]", token);
  await page.click("#login button");
  await page.waitForSelector(".admin-layout");
  await shot(page, "admin-nodes-dark");
  await page.click(".node-card, [data-inspect]");
  await page.waitForTimeout(1000);
  await shot(page, "admin-detail-dark", false);
  await ctx.close();
}
// 减少透明度偏好：玻璃层应退化为不透明面板，文字对比度不依赖背景模糊。
for (const theme of ["light", "dark"]) {
  const { ctx, page } = await open({ width: 1440, height: 1000 }, theme);
  const cdp = await ctx.newCDPSession(page);
  await cdp.send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-reduced-transparency", value: "reduce" }] });
  await page.goto(base + "/");
  await page.waitForSelector(".node-card");
  const matched = await page.evaluate(() => matchMedia("(prefers-reduced-transparency: reduce)").matches);
  if (!matched) throw new Error("浏览器未应用 prefers-reduced-transparency 模拟");
  const blurred = await page.evaluate(() => getComputedStyle(document.querySelector(".node-card")).backdropFilter);
  if (blurred && blurred !== "none") throw new Error("减少透明度下节点卡片仍有 backdrop-filter: " + blurred);
  await shot(page, `public-reduced-${theme}`);
  await ctx.close();
}
await browser.close();

if (errors.length) {
  console.error("浏览器错误：\n" + errors.join("\n"));
  process.exit(1);
}
console.log(`PASS: 24 张截图写入 ${out}，主题切换与保持正常，无页面脚本错误。请人工查看排版。`);
