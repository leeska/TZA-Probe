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

async function open(viewport) {
  const ctx = await browser.newContext({ viewport, deviceScaleFactor: 1 });
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
  await page.click("[data-tab=tasks]");
  await shot(page, "admin-tasks");
  await page.click("[data-tab=alerts]");
  await shot(page, "admin-alerts");
  await page.setViewportSize({ width: 390, height: 844 });
  await page.click("[data-tab=nodes]");
  await shot(page, "admin-mobile");
  await ctx.close();
}
await browser.close();

if (errors.length) {
  console.error("浏览器错误：\n" + errors.join("\n"));
  process.exit(1);
}
console.log(`PASS: 8 张截图写入 ${out}，无页面脚本错误。请人工查看排版。`);
