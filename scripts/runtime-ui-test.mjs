import assert from "node:assert/strict";
import { createRequire } from "node:module";
import path from "node:path";
import { fileURLToPath } from "node:url";

const require = createRequire(path.join(process.env.TZA_UI_DEPS, "package.json"));
const { JSDOM } = require("jsdom");
const { build } = require("esbuild");
const root = fileURLToPath(new URL("../", import.meta.url));
const bundle = await build({
  stdin: {
    contents: 'export { renderRuntime, refreshRuntimeValidity } from "./web/admin-runtime.js"; export { state } from "./web/admin-state.js";',
    resolveDir: root,
  },
  bundle: true, format: "iife", globalName: "runtimeUI", write: false,
});
const dom = new JSDOM('<section data-runtime-channels></section><dialog id="dialog"></dialog><div id="toast"></div>', {
  url: "http://127.0.0.1", runScripts: "outside-only",
});
const window = dom.window;
window.HTMLDialogElement.prototype.showModal = function () { this.open = true; };
window.HTMLDialogElement.prototype.close = function () { this.open = false; };
const now = Math.floor(Date.now() / 1000);
const enrollment = {scope_sha256: "a".repeat(64), target: {controller_id: "controller", node_id: "node"}, host: {platform: "linux-amd64", probe_version: "1.9.2", agent_version: "1.9.2"}};
const request = (id, state, action = "stage") => ({
  id, state, action, version: "0.4.0", enrollment, updated_at: now, plan_sha256: "b".repeat(64),
});
let views = [{
  node_id: "qa-node", enrollment, seen_at: now,
  requests: [
    {...request("latest-unknown", "unknown", "select"), error_code: "operation_interrupted"},
    request("actual-active", "awaiting_approval"),
    request("staged-source", "staged"),
  ],
}];
views[0].requests[1].review = {plan: {
  version: "0.4.0", target: enrollment.target, archive_sha256: "c".repeat(64),
  host: enrollment.host, permissions: ["browser.execute"], artifacts: [{id: "chromium", sha256: "d".repeat(64)}], total_bytes: 1024,
}};
const writes = [];
window.fetch = async (url, options) => {
  if (options.method) {
    writes.push({url, method: options.method, body: options.body ? JSON.parse(options.body) : null});
    return {ok: true, status: 204};
  }
  assert.equal(url, "/api/v1/admin/runtime");
  return {ok: true, status: 200, json: async () => structuredClone(views)};
};
window.eval(bundle.outputFiles[0].text);
window.runtimeUI.state.nodes = [{id: "qa-node", name: '<img src=x onerror="alert(1)">'}];
await window.runtimeUI.renderRuntime();
assert.equal(window.document.querySelector("h3").textContent, '<img src=x onerror="alert(1)">');
assert.equal(window.document.querySelector("img"), null);
assert.match(window.document.body.textContent, /结果未知，需本机核对/);
assert.match(window.document.body.textContent, /完整计划：权限、兼容性、制品与本机路径/);
const approve = window.document.querySelector("[data-runtime-approve]");
assert.equal(approve.dataset.request, "1", "active request must use its true index even when unknown history is newer");
approve.click();
await window.document.querySelector("[data-confirm-runtime]").onclick();
assert.equal(writes[0].url, "/api/v1/admin/nodes/qa-node/runtime/requests/actual-active");
assert.deepEqual(writes[0].body, {approved: true, plan_sha256: "b".repeat(64)});
assert(!window.document.querySelector("#dialog").open);
views[0].seen_at = now - 60;
await window.runtimeUI.renderRuntime();
assert(window.document.querySelector("[data-runtime-approve]").disabled);
views[0].seen_at = now;
views[0].requests[1].updated_at = now - 901;
await window.runtimeUI.renderRuntime();
assert(window.document.querySelector("[data-runtime-approve]").disabled);
views[0].requests = [request("staged-source", "staged"), {...request("unknown-history", "unknown"), error_code: "operation_interrupted"}];
await window.runtimeUI.renderRuntime();
const form = window.document.querySelector("[data-runtime-create]");
assert(form, "unknown history must not permanently hide the next request form");
form.elements.version.value = "0.5.0";
await form.onsubmit({preventDefault() {}});
assert.deepEqual(writes.at(-1).body, {action: "stage", version: "0.5.0", stage_request_id: ""});
const installForm = window.document.querySelector("[data-runtime-create]");
installForm.elements.action.value = "install";
installForm.elements.action.onchange();
assert(installForm.elements.version.disabled);
assert(!installForm.elements.stage_request_id.disabled);
installForm.elements.stage_request_id.value = "staged-source";
await installForm.onsubmit({preventDefault() {}});
assert.deepEqual(writes.at(-1).body, {action: "install", version: "0.4.0", stage_request_id: "staged-source"});
views[0].maintenance = true;
views[0].requests = [{
  ...request("startup", "started_paused", "activate"),
  lifecycle_receipt: {hello: {descriptor_sha256: "e".repeat(64), status: {executor_instance_id: "new-instance", control: {epoch: 8}}}},
}, request("staged-source", "staged")];
let authorized;
await window.runtimeUI.renderRuntime(view => { authorized = view; });
assert.match(window.document.body.textContent, /升级维护暂停中/);
assert.match(window.document.body.textContent, /等待重新授权与恢复/);
assert.equal(window.document.querySelector('option[value="activate"]').disabled, false);
const authorize = [...window.document.querySelectorAll("button")].find(button => button.textContent === "按启动回执核对重新授权");
assert(authorize);
authorize.click();
assert.equal(authorized.binding.epoch, 8);
assert.equal(authorized.binding.descriptor_sha256, "e".repeat(64));
assert.equal(authorized.binding.mode, "managed");
const lifecycleForm = window.document.querySelector("[data-runtime-create]");
lifecycleForm.elements.action.value = "activate";
lifecycleForm.elements.action.onchange();
lifecycleForm.elements.stage_request_id.value = "staged-source";
await lifecycleForm.onsubmit({preventDefault() {}});
assert.deepEqual(writes.at(-1).body, {action: "activate", version: "0.4.0", stage_request_id: "staged-source"});
views[0].requests = [{
  ...request("drain-plan", "awaiting_approval", "drain"),
  lifecycle: {plan: {
    operation: {version: "0.4.0", archive_sha256: "b".repeat(64), target: enrollment.target, options: {install_root: "/opt/ipsentinel-runtime", unit: "ipsentinel-runtime.service"}},
    before: {instance_id: "old-instance", epoch: 7},
    peer: {uid: 1001, gid: 1001, socket_path: "/run/ipsentinel/control.sock"},
  }},
}];
await window.runtimeUI.renderRuntime();
assert.match(window.document.body.textContent, /UID 1001/);
assert([...window.document.querySelectorAll(".runtime-json")].some(element => element.textContent.includes('"peer"')));
assert.match(window.document.body.textContent, /撤销或失败仍保留维护暂停/);
window.close();
console.log("runtime UI: approval identity, full review, expiry, maintenance, lifecycle plans and explicit epoch reauthorization passed");
