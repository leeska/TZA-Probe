import assert from "node:assert/strict";
import { createRequire } from "node:module";
import path from "node:path";
import { fileURLToPath } from "node:url";

const require = createRequire(path.join(process.env.TZA_UI_DEPS, "package.json"));
const { JSDOM } = require("jsdom");
const { build } = require("esbuild");
const root = fileURLToPath(new URL("../", import.meta.url));
const bundle = await build({
  stdin: { contents: 'export { openDeclarativePlugin } from "./web/admin-declarative-plugin.js"; export { renderPlugins } from "./web/admin-plugins.js"; export { state } from "./web/admin-state.js"; export { pluginFields } from "./web/notification-fields.js";', resolveDir: root },
  bundle: true, format: "iife", globalName: "pluginUI", write: false,
});
const dom = new JSDOM('<main id="admin-content"></main><dialog id="dialog"></dialog><div id="toast"></div>', { url: "http://127.0.0.1/admin", runScripts: "outside-only" });
const { window } = dom;
window.HTMLDialogElement.prototype.showModal = function () { this.open = true; };
window.HTMLDialogElement.prototype.close = function () { this.open = false; this.dispatchEvent(new window.Event("close")); };
const notify = {
  id: "notify-example", kind: "notify", name: '<img src=x onerror="bad()">', hint: "本机通知", source: "local", version: "1.0.0",
  permissions: ["network.public_https"],
  settings: [{ key: "server", type: "url", label: "服务地址", required: true }, { key: "topic", type: "string", label: "主题", required: true }, { key: "token", type: "secret", label: "密钥", secret: true, required: true }],
};
const targetPlugin = { id: "dns", kind: "targets", name: "DNS 目标集", version: "1.0.0", source: "local", targets: 1, permissions: [] };
let plugins = [notify, targetPlugin];
let settings = { channel: "telegram", chat_id: "test", telegram_configured: true };
let failConfig = false;
let failRead = false;
const targets = [{ id: "plugin:dns:one", plugin: "dns", plugin_name: targetPlugin.name, host: "1.1.1.1", port: 53, family: "ipv4", region: "公开 DNS", carrier: "测试" }];
const catalog = { targets, plugins: [{ id: "dns", name: targetPlugin.name, count: 1 }] };
const reads = [];
const writes = [];
let tasks = [];
window.fetch = async (url, options = {}) => {
  if (options.method) {
    const body = options.body ? JSON.parse(options.body) : null;
    writes.push({ url, method: options.method, body });
    if (url === "/api/v1/admin/notification") {
      if (failConfig) return new Response(JSON.stringify({ error: "保存失败，请重试" }), { status: 503 });
      settings = { channel: "plugin", plugin: body.plugin, plugin_name: notify.name, plugin_configured: true, plugin_settings: { server: body.plugin_settings.server, topic: body.plugin_settings.topic }, plugin_secrets: { token: true } };
    } else if (url === "/api/v1/admin/notification/test") {
      assert.equal(settings.plugin, notify.id);
    } else if (url === "/api/v1/admin/plugins/notify-example") {
      if (settings.channel === "plugin" && settings.plugin === notify.id) return new Response(JSON.stringify({ error: "正在使用，请先切换渠道" }), { status: 409 });
      plugins = plugins.filter(plugin => plugin.id !== notify.id);
    } else if (url === "/api/v1/admin/plugins/dns") {
      plugins = plugins.filter(plugin => plugin.id !== targetPlugin.id);
    } else if (url === "/api/v1/admin/tasks") tasks.push({ ...body, id: "task-one" });
    else throw new Error("Unexpected write: " + url);
    return new Response(null, { status: 204 });
  }
  reads.push(url);
  if (url === "/api/v1/admin/plugins") return new Response(JSON.stringify(plugins));
  if (url === "/api/v1/admin/notification") return failRead ? new Response("{}", { status: 503 }) : new Response(JSON.stringify(settings));
  if (url === "/api/v1/admin/catalog") return new Response(JSON.stringify(catalog));
  if (url === "/api/v1/admin/nodes") return new Response(JSON.stringify([{ id: "node-one", name: "测试节点", group: "" }]));
  if (url.startsWith("/api/v1/admin/market/")) return new Response("{}", { status: 503 });
  throw new Error("Unexpected read: " + url);
};
window.eval(bundle.outputFiles[0].text);
const ui = window.pluginUI;
ui.state.container = window.document;
const content = window.document.querySelector("main");
let returned = 0;
const back = async () => { returned++; };
await ui.renderPlugins();
assert.equal(content.querySelectorAll("[data-plugin-manage]").length, 2, "local declarative plugins can be managed without the market");
await ui.openDeclarativePlugin(notify.id, { back });
assert.equal(writes.length, 0, "opening plugin settings does not switch channels or start anything");
assert(!content.querySelector("img"), "plugin-supplied content is escaped");
assert(content.textContent.includes("Telegram"));
assert(content.querySelector("[data-plugin-test]").disabled);
let form = content.querySelector("form");
form.elements.ps_server.value = "https://notification.example";
form.elements.ps_topic.value = "observations";
form.elements.ps_token.value = "test-only-secret";
failConfig = true;
await form.onsubmit({ preventDefault() {} });
assert.equal(settings.channel, "telegram", "failed save preserves the existing channel");
assert.equal(form.elements.ps_topic.value, "observations", "failed save preserves editable values");
assert(!form.querySelector('[type="submit"]').disabled);
failConfig = false;
await form.onsubmit({ preventDefault() {} });
assert.equal(settings.plugin, notify.id);
assert(content.textContent.includes("正在使用"));
form = content.querySelector("form");
assert.equal(form.elements.ps_token.value, "");
assert(form.elements.ps_token.placeholder.includes("留空沿用"));
await form.onsubmit({ preventDefault() {} });
assert.equal(writes.at(-1).body.plugin_settings.token, "", "existing secret is preserved by the server rather than echoed");
let testButton = content.querySelector("[data-plugin-test]");
await testButton.onclick({ currentTarget: testButton });
assert.equal(writes.at(-1).url, "/api/v1/admin/notification/test");
testButton = content.querySelector("[data-plugin-test]");
settings = { channel: "telegram", telegram_configured: true };
const beforeTest = writes.length;
await testButton.onclick({ currentTarget: testButton });
assert.equal(writes.length, beforeTest, "stale plugin UI cannot test a newly selected channel");
settings = { channel: "plugin", plugin: notify.id, plugin_configured: true };
content.querySelector("[data-plugin-remove]").onclick();
await window.document.querySelector("#confirm").onclick();
assert(plugins.some(plugin => plugin.id === notify.id), "active notification plugin remains installed");
assert.equal(returned, 0);
failRead = true;
await ui.openDeclarativePlugin(notify.id, { back });
assert(content.querySelector('[role="alert"]'));
assert(content.querySelector("[data-plugin-retry]"));
assert(!content.querySelector("form"), "unknown notification state is not shown as unconfigured");
failRead = false;
const pluginReads = reads.length;
const pluginWrites = writes.length;
await ui.openDeclarativePlugin(targetPlugin.id, { back });
assert(content.textContent.includes("1.1.1.1"));
assert(!reads.slice(pluginReads).some(url => /\/(runtime|executors|nodes)/.test(url)), "target plugin management has no Agent provisioning step");
const query = content.querySelector("[data-plugin-target-query]");
query.value = "not-found";
query.oninput({ target: query });
assert(content.textContent.includes("没有符合条件"));
query.value = "1.1.1.1";
query.oninput({ target: query });
const create = content.querySelector("[data-plugin-create-task]");
await create.onclick({ currentTarget: create });
assert.equal(writes.length, pluginWrites, "opening a task draft does not execute or install a component");
form = window.document.querySelector("#task-form");
assert(form);
assert.equal(form.elements.enabled.checked, false, "new task stays paused until the user chooses to enable it");
await form.onsubmit({ preventDefault() {} });
assert.equal(tasks.length, 1);
assert.equal(tasks[0].enabled, false);
assert.equal(tasks[0].probe.targets[0].host, "1.1.1.1");
content.querySelector("[data-plugin-remove]").onclick();
await window.document.querySelector("#confirm").onclick();
assert(!plugins.some(plugin => plugin.id === targetPlugin.id));
assert.equal(tasks.length, 1, "removal preserves existing task snapshots");
assert.equal(returned, 1);
assert(!writes.some(write => /\/(runtime|executors)|node-plans/.test(write.url)), "declarative workflows do not gain runtime operations");
const safeSelect = ui.pluginFields({ ...notify, settings: [{ key: "token", type: "select", secret: true, label: "令牌", options: [{ value: "sensitive-default", label: "bad" }] }] }, { plugin_settings: { token: "sensitive-current" }, plugin_secrets: { token: true } });
assert(!safeSelect.includes("sensitive-current") && !safeSelect.includes("sensitive-default"), "secret declarations cannot leak through select controls");
dom.window.close();
console.log("PASS: unified declarative plugin management, offline availability, secret preservation, explicit notification changes, target task drafts and removal without Agent provisioning.");
