import assert from "node:assert/strict";
import { createRequire } from "node:module";
import path from "node:path";
import { fileURLToPath } from "node:url";

const require = createRequire(path.join(process.env.TZA_UI_DEPS, "package.json"));
const { JSDOM } = require("jsdom");
const { build } = require("esbuild");
const root = fileURLToPath(new URL("../", import.meta.url));
const bundle = await build({
  stdin: { contents: 'export * from "./web/admin-market.js"; export * from "./web/admin-node-plugins.js"; export { state } from "./web/admin-state.js";', resolveDir: root },
  bundle: true, format: "iife", globalName: "nodeUI", write: false,
});
const dom = new JSDOM('<main id="admin-content"></main><dialog id="dialog"></dialog><div id="toast"></div>', { url: "http://127.0.0.1/admin", runScripts: "outside-only" });
const window = dom.window;
window.HTMLDialogElement.prototype.showModal = function () { this.open = true; };
window.HTMLDialogElement.prototype.close = function () { this.open = false; this.dispatchEvent(new window.Event("close")); };
const now = Math.floor(Date.now() / 1000);
const enrollment = { scope_sha256: "a".repeat(64), host: { platform: "linux-amd64", agent_version: "1.10.0" } };
const channels = [
  { node_id: "ready", enrollment, seen_at: now, requests: [] },
  { node_id: "retry", enrollment, seen_at: now, requests: [] },
  { node_id: "stale", enrollment, seen_at: now - 60, requests: [] },
  { node_id: "busy", enrollment, seen_at: now, requests: [{ state: "running" }] },
];
const nodes = ["ready", "retry", "stale", "busy", "unprovisioned"].map(id => ({ id, name: id === "ready" ? '<img src=x onerror="alert(1)">' : id }));
const pkg = { id: "ipsentinel-runtime", type: "executor", kind: "ipsentinel", name: "IPSentinel", price: 0, permissions: ["browser.execute"], versions: [{ version: "0.4.0", compatible: true }], latest: "0.4.0" };
const reads = [];
const writes = [];
let failSecond = true;
window.fetch = async (url, options = {}) => {
  if (options.method) {
    assert.equal(url, "/api/v1/admin/market/node-plans");
    const body = JSON.parse(options.body);
    writes.push(body);
    if (body.node_id === "retry" && failSecond) return new Response(JSON.stringify({ error: "节点安装配置已变化" }), { status: 409 });
    return new Response(JSON.stringify({ id: "request-" + body.node_id }), { status: 202 });
  }
  reads.push(url);
  let body;
  if (url === "/api/v1/admin/nodes") body = nodes;
  else if (url === "/api/v1/admin/runtime") body = channels;
  else if (url === "/api/v1/admin/executors") body = [];
  else if (url === "/api/v1/admin/market/status") body = { configured: true, trusted_keys: 1 };
  else if (url.startsWith("/api/v1/admin/market/catalog?")) body = { items: url.includes("type=executor") ? [pkg] : [], page: 1, more: false };
  else if (url === "/api/v1/admin/market/packages/ipsentinel-runtime") body = pkg;
  else throw new Error("Unexpected request: " + url);
  return new Response(JSON.stringify(body));
};
window.eval(bundle.outputFiles[0].text);
const ui = window.nodeUI;
ui.state.container = window.document;
assert(ui.nodePluginEligibility());
assert(ui.nodePluginEligibility({ ...channels[0], seen_at: now + 60 }));
assert(ui.nodePluginEligibility({ ...channels[0], seen_at: undefined }));
assert.equal(ui.nodePluginEligibility(channels[0]), "");
await ui.loadMarket(() => {}, [], "plugins");
const content = window.document.querySelector("main");
content.innerHTML = ui.catalogPanel("plugins");
ui.wireMarket(content);
await content.querySelector('[data-market-kind="executor"]').onclick();
assert(reads.some(url => url.includes("type=executor") && !url.includes("kind=")));
assert(content.textContent.includes("安装到所选节点"));
await content.querySelector("[data-package]").onclick();
assert.equal(writes.length, 0, "browsing must never install or download dependencies");
await window.document.querySelector("[data-node-plugin-version]").onclick();
const form = window.document.querySelector("[data-node-plugin-form]");
assert.equal(form.querySelector("img"), null);
const choices = [...form.querySelectorAll('input[name="node"]')];
assert.deepEqual(choices.map(choice => choice.disabled), [false, false, true, true, true]);
assert(form.querySelector("[type=submit]").disabled);
choices[0].checked = choices[1].checked = true;
form.onchange();
await form.onsubmit({ preventDefault() {} });
assert.equal(writes.length, 2);
assert.deepEqual(writes[0], { id: pkg.id, version: "0.4.0", node_id: "ready", scope_sha256: enrollment.scope_sha256 });
assert(choices[0].disabled && !choices[0].checked);
assert(!choices[1].checked, "failed selection must not be retried automatically");
assert(form.textContent.includes("本次 1 个未确认成功"));
assert(form.textContent.includes("尚未安装或启用"));
failSecond = false;
choices[1].checked = true;
form.onchange();
await form.onsubmit({ preventDefault() {} });
assert.equal(writes.length, 3);
assert.equal(writes.filter(write => write.node_id === "ready").length, 1);
assert(form.textContent.includes("已申请 2 个节点计划"));
await window.document.querySelector("[data-node-plugin-manage]").onclick();
assert(window.document.querySelector("[data-executor-settings]"));
assert(content.textContent.includes("节点安装与更新"));
assert(content.textContent.includes("节点设置与运行状态"));
assert(!content.textContent.includes("节点执行器"));
assert.equal(writes.length, 3, "opening settings must not enable a node");
await content.querySelector('[data-plugin-section="overview"]').onclick();
assert(!window.document.querySelector("[data-executor-settings]"));
assert(content.querySelector("[data-node-plugin-version]"));
window.document.querySelector("#dialog").close();
await ui.choosePluginNodes(pkg, "0.4.0");
window.Date.now = () => (now + 60) * 1000;
const staleForm = window.document.querySelector("[data-node-plugin-form]");
staleForm.querySelector('input[value="0"]').checked = true;
await staleForm.onsubmit({ preventDefault() {} });
assert.equal(writes.length, 3, "submit must recheck freshness after the dialog opens");
dom.window.close();
console.log("PASS: node catalog, scoped plans, escaped names, ineligible nodes, approval boundary and partial-failure retry.");
