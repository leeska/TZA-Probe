// Integration checks against a disposable local Server; never target production.
// Install jsdom 26 and esbuild 0.25 into a temporary directory, then set
// TZA_UI_DEPS, TZA_TEST_URL and TZA_TEST_ADMIN_FILE as shown in docs/VERIFICATION.md.
import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";
const base = process.env.TZA_TEST_URL;
if (!base || !["127.0.0.1", "localhost"].includes(new URL(base).hostname))
  throw new Error("Disposable loopback test server required");
const require = createRequire(
  path.join(process.env.TZA_UI_DEPS, "package.json"),
);
const { JSDOM, VirtualConsole } = require("jsdom");
const { build } = require("esbuild");
const root = fileURLToPath(new URL("../", import.meta.url));
const token = (await readFile(process.env.TZA_TEST_ADMIN_FILE, "utf8")).trim();
const bundle = await build({
  entryPoints: [path.join(root, "web/app.js")],
  bundle: true,
  format: "iife",
  write: false,
});
const errors = [];
const consoleAdapter = new VirtualConsole();
consoleAdapter.on("jsdomError", (e) => errors.push(e.message));
const dom = new JSDOM(
  '<div id="app"></div><div id="toast"></div><dialog id="dialog"></dialog>',
  {
    url: base + "/admin",
    runScripts: "outside-only",
    virtualConsole: consoleAdapter,
    pretendToBeVisual: true,
  },
);
const w = dom.window;
w.structuredClone = structuredClone;
let cookie = "";
w.HTMLDialogElement.prototype.showModal = function () {
  this.open = true;
};
w.HTMLDialogElement.prototype.close = function () {
  this.open = false;
};
w.fetch = async (input, opts = {}) => {
  const response = await fetch(new URL(input, base), {
    ...opts,
    headers: { ...opts.headers, ...(cookie ? { Cookie: cookie } : {}) },
  });
  const set = response.headers.get("set-cookie");
  if (set) cookie = set.split(";")[0];
  return response;
};
w.eval(bundle.outputFiles[0].text);
async function until(predicate, label) {
  const deadline = Date.now() + 8000;
  while (Date.now() < deadline) {
    if (predicate()) return;
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  throw new Error(
    "Timed out: " +
      label +
      "; toast=" +
      w.document.querySelector("#toast").textContent,
  );
}
const $ = (s) => w.document.querySelector(s);
const set = (form, key, value) => {
  form.elements.namedItem(key).value = String(value);
};
const submit = (form) =>
  form.dispatchEvent(
    new w.Event("submit", { bubbles: true, cancelable: true }),
  );
await until(() => $("#login"), "login");
set($("#login"), "token", token);
submit($("#login"));
await until(() => $("#add-node"), "admin home");
$("#add-node").click();
let form = $("#node-form");
assert(form);
const name = "UI 验收 " + Date.now();
set(form, "name", name);
set(form, "representative", "上海");
set(form, "quota", 2000);
set(form, "price", 12.8);
set(form, "expires", "2027-09-18");
form.elements.offline.checked = true;
form.elements.renewal.checked = true;
form.elements.traffic.checked = true;
form.elements.load.checked = true;
submit(form);
await until(() => $("#agent-config"), "one-time agent configuration");
const nodeToken = JSON.parse($("#agent-config").textContent).token;
assert(nodeToken.length === 64);
assert($("#install-command").textContent === `curl -fsSL ${base}/install.sh | sh -s -- --endpoint ${base} --token ${nodeToken}`, "one-line install command must carry endpoint and key");
$(".close").click();
const adminHeaders = { Authorization: "Bearer " + token };
let list = await (
  await fetch(base + "/api/v1/admin/nodes", { headers: adminHeaders })
).json();
const created = list.find((n) => n.name === name);
assert(created?.alerts.load);
assert.equal(created.traffic_limit, 2e12);
$(`[data-inspect="${created.id}"]`).click();
await until(() => $("#history-range"), "node detail and history controls");
$("[data-detail-family=ipv6]").click();
assert($("[data-detail-family=ipv6]").classList.contains("active"));
$(".close").click();
$(`[data-edit="${created.id}"]`).click();
form = $("#node-form");
assert.equal(form.elements.name.value, name);
assert.equal(form.elements.hold_seconds.value, "300");
// 未启用的规则禁用其阈值：服务端允许这些字段为 0，输入框的 min 不应把这样的节点锁死。
form.elements.load.checked = false;
form.elements.load.dispatchEvent(new w.Event("change"));
assert(form.elements.cpu_percent.disabled && form.elements.hold_seconds.disabled, "disabled rule must not validate its thresholds");
assert(!form.elements.offline_seconds.disabled === form.elements.offline.checked);
form.elements.load.checked = true;
form.elements.load.dispatchEvent(new w.Event("change"));
assert(!form.elements.cpu_percent.disabled, "enabling a rule must re-enable its thresholds");
$(".close").click();
// 通过 API 造一个阈值全 0 的节点（旧数据或脚本创建），编辑表单必须回填可保存的值
const zeroAlerts = {
  name: "UI 零阈值 " + Date.now(), group: "", region: "", order: 0, public: false,
  representative: "", traffic_limit: 0, traffic_mode: "sum", billing_day: 1, price: 0,
  currency: "CNY", price_period: "月", expires_at: "",
  alerts: { offline: false, offline_seconds: 0, repeat_seconds: 0, renewal: false, renewal_days: 0, traffic: false, traffic_percent: 0, load: false, cpu_percent: 0, memory_percent: 0, hold_seconds: 0 },
};
const zeroResponse = await fetch(base + "/api/v1/admin/nodes", { method: "POST", headers: { ...adminHeaders, "Content-Type": "application/json" }, body: JSON.stringify(zeroAlerts) });
assert.equal(zeroResponse.status, 200, "server accepts zero thresholds while the rules are off");
list = await (await fetch(base + "/api/v1/admin/nodes", { headers: adminHeaders })).json();
const zeroNode = list.find((n) => n.name === zeroAlerts.name);
$("#refresh-nodes").click();
await until(() => $(`[data-edit="${zeroNode.id}"]`), "node list including the zero-threshold node");
$(`[data-edit="${zeroNode.id}"]`).click();
form = $("#node-form");
// 禁用元素的 checkValidity() 恒为真，所以直接比对值与 min。
for (const field of ["traffic_percent", "cpu_percent", "memory_percent", "hold_seconds"]) {
  const el = form.elements[field];
  assert(Number(el.value) >= Number(el.min), `${field}=${el.value} is below min=${el.min}: the node could never be saved`);
}
assert.equal(form.elements.traffic_percent.value, "80");
$(".close").click();
await fetch(base + "/api/v1/admin/nodes/" + zeroNode.id, { method: "DELETE", headers: adminHeaders });
$('[data-tab="tasks"]').click();
await until(() => $("#add-task"), "task list");
$("#add-task").click();
await until(() => $("#catalog-search"), "task editor");
const picker = $("#catalog-search");
picker.value = "上海";
picker.dispatchEvent(new w.Event("input", { bubbles: true }));
assert.equal(w.document.querySelectorAll("[data-target]").length, 3);
$("#select-visible").click();
assert.equal(w.document.querySelectorAll("[data-remove]").length, 3);
picker.value = "不存在地区";
picker.dispatchEvent(new w.Event("input", { bubbles: true }));
assert.equal(w.document.querySelectorAll("[data-target]").length, 0);
assert.equal(w.document.querySelectorAll("[data-remove]").length, 3);
form = $("#dialog form");
assert(form.elements.tcp.checked && form.elements.route.checked);
assert(form.elements.ipv4.checked && form.elements.ipv6.checked);
form.elements.namedItem("node:" + created.id).checked = true;
set(form, "name", "UI 网络验收");
submit(form);
await until(() => !$("#dialog").open, "task save");
const tasks = await (
  await fetch(base + "/api/v1/admin/tasks", { headers: adminHeaders })
).json();
const createdTask = tasks.find(
  (t) => t.name === "UI 网络验收" && t.node_ids.includes(created.id),
);
assert(createdTask);
assert.deepEqual(createdTask.checks, ["tcp", "route"]);
assert.equal(createdTask.targets.length, 6);
assert.equal(createdTask.route_interval, 3600);
assert.equal(createdTask.interval, 60);
$('[data-edit-task="' + createdTask.id + '"]').click();
await until(() => $("#task-form"), "edit combined task");
assert.equal(w.document.querySelectorAll("[data-remove]").length, 3);
assert($("#task-form").elements.ipv6.checked);
form = $("#task-form");
form.elements.namedItem("custom-region").value = "新加坡";
form.elements.namedItem("custom-ipv4").value = "example.com";
form.elements.namedItem("custom-ipv6").value = "example.com";
assert.equal(form.elements.namedItem("custom-carrier").value, "其他");
$("#add-custom").click();
assert.equal(w.document.querySelectorAll("[data-remove]").length, 4);
submit(form);
await until(() => !$("#dialog").open, "custom target save");
const editedTask = (
  await (
    await fetch(base + "/api/v1/admin/tasks", { headers: adminHeaders })
  ).json()
).find((t) => t.id === createdTask.id);
assert.equal(editedTask.targets.length, 8);
assert.equal(editedTask.targets.filter((x) => x.carrier === "其他").length, 2);
assert.equal(editedTask.node_ids.length, 1);
$('[data-tab="alerts"]').click();
await until(() => $("#configure-webhook"), "alert page");
$("#configure-webhook").click();
assert($('#dialog input[name="webhook"]'));
$(".close").click();
$("#logout").click();
await until(() => $("#login"), "logout");
assert.equal(
  (await fetch(base + "/api/v1/session", { headers: { Cookie: cookie } }))
    .status,
  401,
);
// Read-only public UI runs in a separate DOM with no admin cookie.
const publicDOM = new JSDOM(
  '<div id="app"></div><div id="toast"></div><dialog id="dialog"></dialog>',
  {
    url: base + "/",
    runScripts: "outside-only",
    virtualConsole: consoleAdapter,
  },
);
publicDOM.window.fetch = (input, opts) => fetch(new URL(input, base), opts);
publicDOM.window.HTMLDialogElement.prototype.showModal = function () {
  this.open = true;
};
publicDOM.window.HTMLDialogElement.prototype.close = function () {
  this.open = false;
};
publicDOM.window.eval(bundle.outputFiles[0].text);
await until(
  () => publicDOM.window.document.querySelector(".node-card"),
  "public cards",
);
assert(publicDOM.window.document.body.textContent.includes(name));
const publicSearch = publicDOM.window.document.querySelector("#search");
publicSearch.value = "no-such-node-fixture";
publicSearch.dispatchEvent(new publicDOM.window.Event("input"));
assert(publicDOM.window.document.querySelector(".empty"));
publicSearch.value = "";
publicSearch.dispatchEvent(new publicDOM.window.Event("input"));
publicDOM.window.document.querySelector('[data-family="ipv6"]').click();
assert(
  publicDOM.window.document
    .querySelector('[data-family="ipv6"]')
    .classList.contains("active"),
);
assert.deepEqual(errors, []);
await fetch(base + "/api/v1/admin/tasks/" + createdTask.id, {
  method: "DELETE",
  headers: adminHeaders,
});
await fetch(base + "/api/v1/admin/nodes/" + created.id, {
  method: "DELETE",
  headers: adminHeaders,
});
dom.window.close();
publicDOM.window.close();
console.log(
  "PASS: login, node creation/edit, billing and alerts, search/filter/batch catalog, combined checks and dual stack, overseas custom targets, edit round-trip and explicit binding, notifications page, logout, public cards/search/IPv6. DOM simulation; visual layout not covered.",
);
