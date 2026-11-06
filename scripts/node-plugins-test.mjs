import assert from "node:assert/strict";
import { createRequire } from "node:module";
import path from "node:path";
import { fileURLToPath } from "node:url";

const require = createRequire(path.join(process.env.TZA_UI_DEPS, "package.json"));
const { JSDOM } = require("jsdom");
const { build } = require("esbuild");
const root = fileURLToPath(new URL("../", import.meta.url));
const bundle = await build({
  stdin: { contents: 'export * from "./web/admin-market.js"; export * from "./web/admin-plugin-agents.js"; export { renderPlugins } from "./web/admin-plugins.js"; export { state } from "./web/admin-state.js";', resolveDir: root },
  bundle: true, format: "iife", globalName: "pluginUI", write: false,
});
const dom = new JSDOM('<main id="admin-content"></main><dialog id="dialog"></dialog><div id="toast"></div>', { url: "http://127.0.0.1/admin", runScripts: "outside-only" });
const window = dom.window;
window.HTMLDialogElement.prototype.showModal = function () { this.open = true; };
window.HTMLDialogElement.prototype.close = function () { this.open = false; this.dispatchEvent(new window.Event("close")); };
const now = Math.floor(Date.now() / 1000);
const enrollment = { scope_sha256: "a".repeat(64), host: { platform: "linux-amd64", agent_version: "1.10.0" } };
const channels = [
  { node_id: "ready", enrollment, seen_at: now, requests: [] },
  { node_id: "stale", enrollment, seen_at: now - 60, requests: [] },
];
const nodes = ["ready", "stale", "unprovisioned"].map(id => ({ id, name: id === "ready" ? '<img src=x onerror="alert(1)">' : id }));
const pkg = { id: "ipsentinel-runtime", type: "executor", kind: "ipsentinel", name: "IPSentinel", price: 0, permissions: ["browser.execute"], versions: [{ version: "0.4.0", compatible: true }], latest: "0.4.0" };
let managers = [];
let offline = false;
let failInstall = false;
const writes = [];
const reads = [];
window.fetch = async (url, options = {}) => {
  if (options.method) {
    const body = options.body ? JSON.parse(options.body) : null;
    writes.push({ url, method: options.method, body });
    if (url === "/api/v1/admin/market/install") {
      if (failInstall) return new Response(JSON.stringify({ error: "验签失败" }), { status: 400 });
      managers = [{ id: pkg.id, name: "IP 地区防护", kind: "agent", version: "0.4.0", source: "market" }];
      return new Response(JSON.stringify(managers[0]));
    }
    if (url === "/api/v1/admin/market/node-plans") {
      assert.equal(body.scope_sha256, enrollment.scope_sha256);
      channels[0].requests = [{ id: "request", version: body.version, state: "requested", action: "stage", enrollment }];
      return new Response(JSON.stringify(channels[0].requests[0]), { status: 202 });
    }
    if (url === "/api/v1/admin/plugins/" + pkg.id) {
      managers = [];
      return new Response("{}");
    }
    if (url === "/api/v1/admin/nodes/batch") {
      for (const node of nodes.filter(item => body.ids.includes(item.id))) node.runtime_install_disabled = body.patch.runtime_install_disabled;
      return new Response(JSON.stringify({ updated: body.ids.length }));
    }
    if (url.startsWith("/api/v1/admin/nodes/")) {
      const node = nodes.find(item => url.endsWith(item.id));
      node.runtime_install_disabled = body.runtime_install_disabled;
      return new Response(JSON.stringify(node));
    }
    if (url.includes("/runtime/requests/request")) return new Response("{}");
    throw new Error("Unexpected write " + url);
  }
  reads.push(url);
  if (offline && url.startsWith("/api/v1/admin/market/")) return new Response("{}", { status: 503 });
  let body;
  if (url === "/api/v1/admin/nodes") body = nodes;
  else if (url === "/api/v1/admin/plugins") body = managers;
  else if (url === "/api/v1/admin/runtime") body = channels;
  else if (url === "/api/v1/admin/executors") body = [];
  else if (url === "/api/v1/admin/market/status") body = { configured: true, trusted_keys: 1 };
  else if (url === "/api/v1/admin/market/updates") body = { updates: [] };
  else if (url.startsWith("/api/v1/admin/market/catalog?")) body = { items: [pkg], page: 1, more: false };
  else if (url === "/api/v1/admin/market/packages/" + pkg.id) body = { ...pkg, installed: managers.length ? { version: "0.4.0", source: "market" } : undefined };
  else throw new Error("Unexpected request " + url);
  return new Response(JSON.stringify(body));
};
window.eval(bundle.outputFiles[0].text);
const ui = window.pluginUI;
ui.state.container = window.document;
const content = window.document.querySelector("main");
await ui.loadMarket(() => {}, [], "plugins");
content.innerHTML = ui.catalogPanel("plugins");
ui.wireMarket(content);
assert(content.querySelector('[data-market-kind=""]'));
assert(!content.textContent.includes("节点插件"));
assert(!content.querySelector('[data-market-kind="executor"]'));
await content.querySelector("[data-package]").onclick();
assert.equal(writes.length, 0, "browsing cannot install anything");
assert(content.querySelector("[data-install-manager]"));
assert(!content.querySelector("input, textarea"));
content.querySelector("[data-install-manager]").onclick();
assert(window.document.querySelector("#dialog").textContent.includes("不下载"));
assert.equal(writes.length, 0);
failInstall = true;
await window.document.querySelector("#confirm").onclick();
assert.equal(managers.length, 0);
assert(window.document.querySelector("#toast").textContent.includes("验签失败"));
failInstall = false;
content.querySelector("[data-install-manager]").onclick();
await window.document.querySelector("#confirm").onclick();
assert.equal(managers.length, 1);
assert(content.textContent.includes("Agent 组件"));
assert(!content.querySelector("input, textarea, [data-executor-settings]"));
assert(!content.querySelector("img"), "Agent names must be escaped");
assert.equal(content.querySelectorAll("[data-agent-action]").length, 1);
assert(content.querySelector("[data-agent-toggle]"), "controller execution opt-out is available");
assert(!writes.some(write => write.url.includes("node-plans")), "controller install must not provision an Agent");
await content.querySelector("[data-agent-action]").onclick();
assert.equal(writes.filter(write => write.url.endsWith("node-plans")).length, 1);
assert(content.textContent.includes("准备中"));
assert(!content.querySelector("[data-agent-action]"));
channels[0].requests[0] = { ...channels[0].requests[0], state: "awaiting_approval", updated_at: now, plan_sha256: "b".repeat(64), review: { plan: { version: "0.4.0", total_bytes: 1024, artifacts: [{}], permissions: ["browser.execute"] } } };
await content.querySelector("[data-agents-refresh]").onclick();
content.querySelector("[data-agent-review]").onclick();
const dialog = window.document.querySelector("#dialog");
assert(dialog.textContent.includes("下载量"));
assert(dialog.querySelector("details:not([open])"), "technical plan stays collapsed");
channels[0].requests[0].plan_sha256 = "c".repeat(64);
const beforeApproval = writes.length;
await dialog.querySelector("[data-agent-confirm]").onclick();
assert.equal(writes.length, beforeApproval, "changed plan cannot be approved");
dialog.close();
await content.querySelector("[data-agents-refresh]").onclick();
content.querySelector("[data-agent-review]").onclick();
await dialog.querySelector("[data-agent-confirm]").onclick();
assert.equal(writes.at(-1).method, "PUT");
assert.equal(writes.at(-1).body.plan_sha256, "c".repeat(64));
assert(!writes.some(write => ["select", "activate", "run.submit"].includes(write.body?.action)), "no implicit activation");
await content.querySelector("[data-agent-toggle]").onclick();
assert(writes.some(write => write.url.includes("/admin/nodes/ready") && write.body.runtime_install_disabled === true), "controller execution opt-out is available");
assert(content.textContent.includes("主控执行已关闭"));
assert(!content.querySelector("[data-agent-review]"), "disabled nodes cannot approve queued operations");
await content.querySelector("[data-agents-allow-all]").onclick();
assert(nodes.every(node => node.runtime_install_disabled === false), "one click enables all nodes");
assert.equal(writes.at(-1).url, "/api/v1/admin/nodes/batch");
assert(content.querySelector("[data-agent-review]"), "re-enabled pending operations remain reviewable");
await content.querySelector('[data-plugin-section="overview"]').onclick();
assert(content.textContent.includes("先添加一个防护节点"));
assert.equal(content.querySelector("textarea"), null);
offline = true;
await ui.renderPlugins();
assert(content.querySelector("[data-plugin-manage]"), "installed manager remains available offline");
assert(!content.textContent.includes("节点插件"));
await content.querySelector("[data-plugin-manage]").onclick();
await content.querySelector('[data-plugin-section="settings"]').onclick();
assert(content.textContent.includes("Agent 组件"));
assert(!content.querySelector("[data-install-manager]"), "offline mode does not offer an unverified version");
await ui.renderPlugins();
content.querySelector("[data-remove-plugin]").onclick();
assert(dialog.textContent.includes("不会卸载 Agent"));
await dialog.querySelector("#confirm").onclick();
assert.equal(managers.length, 0);
assert.equal(channels[0].requests.length, 1, "removal cannot delete Agent records");
assert(reads.some(url => url.includes("type=plugin")));
const ready = { ...channels[0], requests: [] };
assert.equal(ui.pluginAgentState(ready, null, "0.4.0").action, "stage");
assert.equal(ui.pluginAgentState({ ...ready, maintenance: true }, null, "0.4.0").action, undefined);
assert.equal(ui.pluginAgentState(channels[1], null, "0.4.0").action, undefined);
assert.equal(ui.pluginAgentState(null, null, "0.4.0").action, undefined);
const staged = { id: "stage", version: "0.4.0", state: "staged", enrollment };
assert.equal(ui.pluginAgentState({ ...ready, requests: [staged] }, null, "0.4.0").action, "install");
assert.equal(ui.pluginAgentState({ ...ready, requests: [{ ...staged, enrollment: { scope_sha256: "different" } }] }, null, "0.4.0").action, "stage");
assert.equal(ui.pluginAgentState({ ...ready, requests: [{ ...staged, state: "installed" }] }, null, "0.4.0").label, "已安装，待启用");
assert.equal(ui.pluginAgentState({ ...ready, requests: [{ ...staged, state: "unknown" }] }, null, "0.4.0").action, undefined);
assert.equal(ui.pluginAgentState({ ...ready, requests: [{ ...staged, state: "unknown", action: "install" }, { ...staged, state: "installed" }] }, null, "0.4.0").label, "正在核对");
assert.equal(ui.pluginAgentState({ ...ready, requests: [{ ...staged, state: "unknown", enrollment: { scope_sha256: "old" } }] }, null, "0.4.0").action, undefined);
assert.equal(ui.pluginAgentState(ready, { received_at: now }, "0.4.0").label, "已接入");
assert.equal(ui.pluginAgentState(ready, { received_at: now - 60 }, "0.4.0").label, "已接入，状态待刷新");
dom.window.close();
console.log("PASS: unified plugin catalog, Agent status, per-node opt-out and bulk enable, explicit approvals, changed-plan rejection, offline management and safe removal.");
