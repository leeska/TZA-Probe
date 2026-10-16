// Real-browser contract and isolation checks against a disposable local Server.
// TZA_BROWSER_DEPS=<package root containing playwright> TZA_TEST_URL=http://127.0.0.1:...
// TZA_TEST_ADMIN_FILE=... TZA_TEST_CREDENTIALS_FILE=... bun scripts/frontend-browser-test.mjs
import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { readFile, mkdtemp, rm } from "node:fs/promises";
import { execFileSync } from "node:child_process";
import path from "node:path";
import os from "node:os";
const base = process.env.TZA_TEST_URL;
assert(base && ["127.0.0.1", "localhost"].includes(new URL(base).hostname), "disposable loopback Server required");
const { chromium } = createRequire(path.join(process.env.TZA_BROWSER_DEPS || "/tmp/tza-browser-deps", "package.json"))("playwright");
const token = (await readFile(process.env.TZA_TEST_ADMIN_FILE, "utf8")).trim();
const credentials = await readFile(process.env.TZA_TEST_CREDENTIALS_FILE, "utf8");
const username = credentials.match(/^后台用户名：(.+)$/m)?.[1], password = credentials.match(/^后台密码：(.+)$/m)?.[1];
assert(username && password);
const tmp = await mkdtemp(path.join(os.tmpdir(), "tza-frontend-"));
execFileSync("go", ["run", "./tools/themepack", "-out", tmp, "examples/themes/observatory"]);
const browser = await chromium.launch();
const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
const request = async (method, url, data, type = "application/json") => {
 const response = await fetch(base + "/api/v1" + url, { method, body: data === undefined ? undefined : Buffer.isBuffer(data) ? data : JSON.stringify(data), headers: { Authorization: "Bearer " + token, "Content-Type": type, Origin: base } });
 assert(response.ok, `${method} ${url}: ${response.status} ${response.ok ? "" : await response.text()}`);
 return response.status === 204 ? null : response.json();
};
const ids = [];
const publicName = `Public fixture ${Date.now()}`, privateName = `PRIVATE fixture ${Date.now()}`;
try {
 await request("POST", "/admin/themes", await readFile(path.join(tmp, "observatory.zip")), "application/zip");
 await request("PUT", "/admin/panel", { default_skin: "observatory" });
 await request("PUT", "/admin/themes/observatory/settings", { headline: "network" });
 for (const visible of [true, false]) {
  const node = await request("POST", "/admin/nodes", { name: visible ? publicName : privateName, public: visible, region: "Singapore", billing: { reset_at: new Date().toISOString(), traffic_mode: "sum" } });
  ids.push(node.id);
 }
 const login = await fetch(base + "/api/v1/login", { method: "POST", body: JSON.stringify({ username, password }), headers: { Origin: base, "Content-Type": "application/json" } });
 assert.equal(login.status, 204);
 const session = login.headers.get("set-cookie")?.match(/tza_session=([^;]+)/)?.[1];
 assert(session, "login did not create a session");
 await context.addCookies([{ name: "tza_session", value: session, url: base, httpOnly: true, sameSite: "Strict" }]);
 const page = await context.newPage();
 const scripts = [];
 page.on("request", (r) => { if (r.resourceType() === "script") scripts.push(new URL(r.url()).pathname); });
 const errors = [];
 page.on("pageerror", (e) => errors.push(e.message));
 await page.goto(base + "/");
 const frame = page.frameLocator("#theme-frontend");
 await frame.getByRole("cell", { name: publicName, exact: false }).waitFor();
 await frame.getByRole("heading", { name: "网络观测", exact: true }).waitFor();
 assert.equal(await frame.locator("aside p").textContent(), "PUBLIC OBSERVATORY / SDK v1", "package-local JSON fetch");
 assert(await frame.locator("aside img").evaluate((img) => img.complete && img.naturalWidth > 0), "package SVG asset");
 assert.equal(await page.locator(".public-header, .node-card").count(), 0, "core frontend must not render underneath");
 assert(!scripts.includes("/app.js"), "core frontend renderer must not execute");
 const isolated = page.frames().find((f) => f !== page.mainFrame());
 assert(await page.evaluate(async (id) => (await fetch("/api/v1/admin/nodes").then((r) => r.json())).some((n) => n.id === id), ids[1]), "parent browser must really be signed in as admin");
 const publicNodes = await isolated.evaluate(() => window.TZA.api("/nodes"));
 assert(publicNodes.some((n) => n.id === ids[0]));
 assert(!publicNodes.some((n) => n.id === ids[1]), "parent's admin cookie must not expose private nodes");
 const privacy = await isolated.evaluate(async () => {
  const outcome = {};
  try { parent.document.querySelector("#app"); outcome.parent = true; } catch { outcome.parent = false; }
  try { localStorage.getItem("tza_session"); outcome.storage = true; } catch { outcome.storage = false; }
  for (const path of ["/admin/nodes", "//example.com/nodes", "/nodes/../../admin/nodes", "/nodes/" + "x".repeat(65) + "/history"]) {
   try { await TZA.api(path); outcome[path] = true; } catch { outcome[path] = false; }
  }
  try { await fetch("/api/v1/admin/nodes", { credentials: "include" }); outcome.directFetch = true; } catch { outcome.directFetch = false; }
  return outcome;
 });
 assert(Object.values(privacy).every((v) => v === false), JSON.stringify(privacy));
 // Real private-history ID, with an authenticated browser, still has no authority.
 assert(await isolated.evaluate(async (id) => { try { await TZA.api(`/nodes/${id}/history`); return false; } catch { return true; } }, ids[1]));
 await isolated.evaluate(() => TZA.storage.set("example", { sort: "cpu" }));
 assert.deepEqual(await isolated.evaluate(() => TZA.storage.get("example")), { sort: "cpu" });
 await isolated.evaluate(() => TZA.setMode("dark"));
 await frame.locator("html[data-theme=dark]").waitFor();
 await frame.getByRole("button", { name: "查看 →" }).first().click();
 await frame.getByRole("heading", { name: "CPU / 最近 24 小时" }).waitFor();
 assert(scripts.some((x) => x.endsWith("/chart.mjs")), "split module chunk must load");
 assert(new URL(page.url()).pathname.startsWith("/node/"));
 await page.reload();
 await frame.getByRole("heading", { name: "CPU / 最近 24 小时" }).waitFor();
 await frame.getByRole("button", { name: "← 返回总览" }).click();
 await frame.getByRole("cell", { name: publicName, exact: false }).waitFor();
 await page.goBack();
 await frame.getByRole("heading", { name: "CPU / 最近 24 小时" }).waitFor();
 await page.frames().find((f) => f !== page.mainFrame()).evaluate(() => TZA.navigate("/administration.v1#overview"));
 await page.reload();
 await frame.getByRole("cell", { name: publicName, exact: false }).waitFor();
 assert.equal(new URL(page.url()).pathname, "/administration.v1", "custom dotted route must survive reload");
 await page.goto(base + "/admin");
 await page.locator(".admin-layout").waitFor();
 assert.equal(await page.locator("#theme-frontend").count(), 0, "admin must never mount an executable theme");
 await page.goto(base + "/?tza-theme=observatory");
 await frame.getByRole("cell", { name: publicName, exact: false }).waitFor();
 if (process.env.TZA_TEST_ARTIFACTS) await page.screenshot({ path: path.join(process.env.TZA_TEST_ARTIFACTS, "frontend-desktop.png"), fullPage: true });
 await page.setViewportSize({ width: 390, height: 844 });
 assert(await page.frames().find((f) => f !== page.mainFrame()).evaluate(() => document.documentElement.scrollWidth <= innerWidth), "mobile document must fit viewport");
 if (process.env.TZA_TEST_ARTIFACTS) await page.screenshot({ path: path.join(process.env.TZA_TEST_ARTIFACTS, "frontend-mobile.png"), fullPage: true });
 await page.setViewportSize({ width: 1440, height: 1000 });
 await frame.getByRole("combobox", { name: "外观" }).selectOption("shadcn");
 await page.locator(".public-header").waitFor();
 assert.equal(await page.locator("#theme-frontend").count(), 0);
 await page.goto(base + "/?tza-theme=observatory");
 await frame.getByRole("cell", { name: publicName, exact: false }).waitFor();
 const broken = page.frames().find((f) => f !== page.mainFrame());
 await broken.evaluate(() => TZA.reportError(new Error("test failure recovery")));
 await page.getByRole("link", { name: "使用默认前台" }).waitFor();
 await page.getByRole("link", { name: "使用默认前台" }).click();
 await page.locator(".public-header").waitFor();
 assert.deepEqual(errors, [], "unexpected frontend script errors");
 // A real module resource failure must trigger recovery without theme cooperation.
 await page.route("**/themes/observatory/main.js*", (route) => route.abort());
 await page.goto(base + "/?tza-theme=observatory");
 await page.getByRole("link", { name: "使用默认前台" }).waitFor();
 await page.unroute("**/themes/observatory/main.js*");
 const direct = await context.newPage();
 await direct.goto(base + "/themes/observatory/index.html");
 assert(await direct.evaluate(() => { try { localStorage.getItem("test"); return false; } catch { return true; } }), "direct HTML must also be sandboxed");
 await direct.close();
 console.log("PASS: independent HTML/modules/chunks, public-only data with admin signed in, DOM/storage/API isolation, theme storage/mode/switch, deep-link reload/back, admin separation, mobile layout, direct-document sandbox and script-failure recovery.");
} finally {
 for (const id of ids) await request("DELETE", "/admin/nodes/" + id).catch(() => {});
 await request("DELETE", "/admin/themes/observatory").catch(() => {});
 await browser.close();
 await rm(tmp, { recursive: true, force: true });
}
