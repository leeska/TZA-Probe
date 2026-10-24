// Exercise the homepage's actual grouping and both renderers with known probe history.
import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";
const require = createRequire(path.join(process.env.TZA_UI_DEPS || "/tmp/tza-ui-deps", "package.json"));
const { JSDOM } = require("jsdom");
const { build } = require("esbuild");
const root = fileURLToPath(new URL("../", import.meta.url));
const source = await readFile(path.join(root, "web/app.js"), "utf8");
// Skip startup/polling, expose the real data path without changing production exports.
const contents = source.slice(0, source.lastIndexOf("\nstart().catch(")) + `
window.homeNetwork = { nodeFacts, latencyBands, latencyLines,
  set(samples, latest = []) { latencySamples = samples; observations.set("test", latest); },
  family(value) { cardFamilies.set("test", value); },
  scope(value) { latencyScope = value; }
};`;
const bundle = await build({ stdin: { contents, resolveDir: path.join(root, "web"), sourcefile: "app.js" }, bundle: true, format: "iife", write: false });
const dom = new JSDOM('<div id="app"></div>', { url: "http://127.0.0.1/", runScripts: "outside-only" });
const w = dom.window;
w.eval(bundle.outputFiles[0].text);
const api = w.homeNetwork;
const node = { id: "test", name: "Test", online: true, ip_families: ["ipv4", "ipv6"], display: { latency_region: "上海" }, billing: {}, probe_targets: [
  { kind: "tcp", family: "ipv4", carrier: "电信", region: "上海" },
  { kind: "tcp", family: "ipv6", carrier: "电信", region: "上海" },
  { kind: "tcp", family: "ipv4", carrier: "其他", region: "Google" },
] };
const sample = (at, latency, extra = {}) => ({ at, latency, sent: 3, received: 3, node_id: node.id, family: "ipv4", carrier: "电信", region: "上海", ...extra });
const history = [sample(1, 1000), sample(2, 1000), ...Array.from({ length: 9 }, (_, i) => sample(i + 3, (i + 1) * 10)), sample(12, null, { sent: 6, received: 0 })];
const latest = [{ at: 12, stale: false, task: { kind: "tcp", family: "ipv4", carrier: "电信", region: "上海" }, result: { status: "ok", latency: 999, sent: 3, received: 3 } }];
api.set([...history, sample(13, 800, { node_id: "other" }), sample(14, 600, { region: "北京" }), sample(15, 75, { family: "ipv6" }), sample(16, 25, { carrier: "其他", region: "Google" })], latest);
let facts = api.nodeFacts(node);
let cell = facts.latencies[0];
assert.equal(cell.value, 50, "only the nine successful samples visible in the ten bars contribute latency");
assert.equal(cell.lossText, "18.2%", "loss uses total packets including the failed probe with a different packet count");
assert.equal(cell.history.length, 10);
assert.equal(cell.history[0].at, 3, "older hidden bars must not enter the mean");
assert.match(cell.title, /最近 10 次平均/);
assert.equal(facts.latencies[1].value, 25, "overseas target stays separate from the configured carrier region");
for (const render of [api.latencyBands, api.latencyLines]) {
  const host = w.document.createElement("div");
  host.innerHTML = render(node, facts);
  const first = host.firstElementChild;
  assert.match(first.textContent, /50 ms/);
  assert.match(first.textContent, /18\.2%/);
  assert.equal(first.querySelector(".quality-bars").children.length, 10);
  assert.equal(first.querySelector(".quality-bars .fail").getAttribute("title").includes("失败"), true);
}
assert.equal(facts.scope, "domestic", "default scope is domestic");
assert.equal(facts.hasBothScopes, true, "this node has both domestic and overseas entries");
// 延迟分国内 / 国际两栏：两边都有格子时有效分栏跟访客的选择走，渲染函数只画当前栏的格子；
// latencies 本身始终是全部格子（两个分栏共用同一份取数，切换不改任何数字）。
api.scope("intl");
facts = api.nodeFacts(node);
assert.equal(facts.scope, "intl", "the visitor's choice wins when both scopes have entries");
for (const render of [api.latencyBands, api.latencyLines]) {
  const host = w.document.createElement("div");
  host.innerHTML = render(node, facts);
  assert.equal(host.children.length, 1, "国际栏只画海外目标那一格");
  assert.match(host.firstElementChild.textContent, /25 ms/);
}
api.scope("domestic");
facts = api.nodeFacts(node);
{
  const host = w.document.createElement("div");
  host.innerHTML = api.latencyBands(node, facts);
  assert.equal(host.children.length, 1, "国内栏只画三网那一格");
  assert.match(host.textContent, /50 ms/);
}
// 只挂了海外目标的机器（国际线路按设计不挂三网任务）：没有可点的 tab，有效分栏固定是国际。
const intlOnly = { ...node, probe_targets: [node.probe_targets[2]] };
assert.equal(api.nodeFacts(intlOnly).hasBothScopes, false, "a single-scope node offers no tab");
assert.equal(api.nodeFacts(intlOnly).scope, "intl", "single overseas scope falls back to intl regardless of the stored choice");
api.family("ipv6");
assert.equal(api.nodeFacts(node).latencies[0].value, 75, "IPv6 has its own window");
api.family("ipv4");
assert.equal(api.nodeFacts({ ...node, online: false }).latencies[0].value, 50, "historic averages stay aligned with historic bars when offline");
api.set([sample(20, null, { received: 0 }), sample(21, null, { received: 0 })], latest);
cell = api.nodeFacts(node).latencies[0];
assert.equal(cell.value, null);
assert.equal(cell.missText, "超时");
assert.equal(cell.lossText, "100.0%");
api.set([sample(20, 0, { sent: 0, received: 0 })]);
cell = api.nodeFacts(node).latencies[0];
assert.equal(cell.value, 0, "a valid zero latency is included");
assert.equal(cell.lossText, "—", "missing packet counts must not invent zero loss");
api.set([], latest);
cell = api.nodeFacts(node).latencies[0];
assert.equal(cell.value, null, "an empty current window must not fall back to latest observations");
assert.equal(cell.missText, "未检测");
assert.equal(cell.lossText, "—");
dom.window.close();
console.log("PASS: homepage window means, weighted loss, failures, empty history, grouping and card/list consistency.");
