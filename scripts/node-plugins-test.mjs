import assert from "node:assert/strict";
import { createRequire } from "node:module";
import path from "node:path";
import { fileURLToPath } from "node:url";

const require = createRequire(path.join(process.env.TZA_UI_DEPS || "/tmp/tza-ui-deps", "package.json"));
const { JSDOM } = require("jsdom");
const { build } = require("esbuild");
const root = fileURLToPath(new URL("../", import.meta.url));
const bundle = await build({ stdin: { contents: 'export * from "./web/admin-node-plugin-detail.js"; export * from "./web/region-tasks.js"; export { state } from "./web/admin-state.js";', resolveDir: root }, bundle: true, format: "iife", globalName: "pluginUI", write: false });
const dom = new JSDOM('<main id="admin-content"></main><dialog id="dialog"></dialog><div id="toast"></div>', { url: "http://localhost/admin", runScripts: "outside-only" });
const { window } = dom;
window.HTMLDialogElement.prototype.showModal = function () { this.open = true; };
window.HTMLDialogElement.prototype.close = function () { this.open = false; };
const writes = [], reads = [];
let ipguardData = { nodes: [], jobs: [], services: [], worker_seen: 0 };
window.fetch = async (url, options = {}) => {
  if (options.method) {
    writes.push({ url, method: options.method, body: JSON.parse(options.body || "null") });
    return new Response("{}", { status: 201 });
  }
  reads.push(url);
  assert.equal(url, "/api/v1/admin/ipguard");
  return new Response(JSON.stringify(ipguardData));
};
window.eval(bundle.outputFiles[0].text);
const ui = window.pluginUI;
ui.state.container = window.document;
// 节点的地区偏移设置在节点管理里以弹窗打开：直接读取出口与设置，不查插件列表、不发任何安装请求；
// 固定那一台，不能切换节点；关掉弹窗时回调一次。
let closed = 0;
await ui.openRegionDialog(() => closed++, { node: "x", name: "X" });
await new Promise((resolve) => setTimeout(resolve, 20));
assert.ok(window.document.querySelector("#dialog").open, "弹窗要打开");
assert.equal(window.document.querySelector("#dialog-title").textContent, "地区偏移设置 · X");
assert.deepEqual(writes, []);
assert.deepEqual(reads, ["/api/v1/admin/ipguard"]);
reads.length = 0;
assert.equal(window.document.querySelector("[data-install-manager]"), null);
assert.equal(window.document.querySelector("[data-region-install]"), null);
assert.match(window.document.querySelector("#dialog").textContent, /节点不存在/);
window.document.querySelector("#dialog").close();
window.document.querySelector("#dialog").dispatchEvent(new window.Event("close"));
assert.equal(closed, 1, "关掉弹窗回调一次");
assert.ok(!window.document.querySelector("#dialog").classList.contains("region-dialog"), "关掉后去掉弹窗样式");
{
  const policy = { revision: 1, enabled: true, auto_recover: false, expected_ip: "203.0.113.5", country: "US", services: ["google"] };
  ipguardData = { nodes: [{ id: "a", name: "甲", policy, egress: {} }, { id: "b", name: "乙", egress: {} }, { id: "c", name: "丙", egress: {} }], jobs: [], services: [], worker_seen: 0, worker_configured: true, environments: { US: { locale: "en-US", timezone: "America/New_York" } }, tasks: [{ id: "t1", name: "已有任务", enabled: true, auto_recover: false, services: ["google"], node_ids: ["c"] }], node_settings: { a: { family: "ipv4", country: "US" } }, pending: {} };
  await ui.openRegionDialog(() => {}, { node: "a", name: "甲" });
  await new Promise((resolve) => setTimeout(resolve, 20));
  assert.equal(window.document.querySelector("#dialog [data-region-node]"), null, "节点设置固定那一台");
  assert.equal(window.document.querySelector('#dialog [name="country"]').value, "US", "回填节点设置");
  assert.equal(window.document.querySelector('#dialog [name="enabled"]'), null, "启用与服务属于任务，不在节点设置里");
  assert.match(window.document.querySelector("#dialog [data-region-notice]").textContent, /还没有加入地区偏移检测任务/);
  window.document.querySelector("#dialog").close();
  window.document.querySelector("#dialog").dispatchEvent(new window.Event("close"));
  assert.deepEqual(writes, [], "打开弹窗不写任何东西");

  // 地区偏移检测任务：和解锁检测一样一次选多台节点；已在别的任务里的节点不能选，没设置的节点标出来。
  let saved = 0;
  const nodes = ipguardData.nodes.map((n) => ({ ...n, online: true }));
  ui.regionTaskEditor(null, ipguardData, nodes, () => saved++);
  const form = window.document.querySelector("#region-task-form");
  const box = (id) => form.querySelector(`[name="node"][value="${id}"]`);
  assert.ok(box("c").disabled, "已在别的任务里的节点不能选");
  assert.match(box("c").parentElement.textContent, /已在「已有任务」/);
  assert.match(box("b").parentElement.textContent, /未设置目标地区/);
  assert.match(box("a").parentElement.textContent, /目标 .* IPv4/);
  box("a").checked = true;
  box("b").checked = true;
  form.querySelector('[name="service"][value="meta"]').checked = false;
  form.elements.name.value = "  两台  ";
  form.dispatchEvent(new window.Event("submit", { bubbles: true, cancelable: true }));
  await new Promise((resolve) => setTimeout(resolve, 20));
  assert.deepEqual(writes.at(-1), { url: "/api/v1/admin/ipguard/tasks", method: "POST", body: { name: "两台", enabled: true, auto_recover: false, services: ["google"], node_ids: ["a", "b"] } });
  assert.equal(saved, 1);
  const { worst, waiting } = ui.regionTaskReading({ services: ["google"], node_ids: ["a", "b"] }, { ...ipguardData, pending: { b: "未设置目标地区：在节点管理里设置" } });
  assert.equal(waiting, 1);
  assert.equal(worst.node, "甲");
}
dom.window.close();
console.log("PASS: region drift node settings dialog (fixed node, no task switches, no requests), multi-node region task editor (taken nodes disabled, unset nodes marked, POST body), task reading.");
