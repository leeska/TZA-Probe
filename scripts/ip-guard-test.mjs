import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { readFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const require = createRequire(path.join(process.env.TZA_UI_DEPS, "package.json"));
const { JSDOM } = require("jsdom");
const { build } = require("esbuild");
const root = fileURLToPath(new URL("../", import.meta.url));
const bundle = await build({ stdin: { contents: 'export * from "./web/admin-ip-guard.js";', resolveDir: root }, bundle: true, format: "iife", globalName: "guardUI", write: false });
const dom = new JSDOM('<main></main><dialog id="dialog"></dialog><div id="toast"></div>', { url: "http://127.0.0.1", runScripts: "outside-only" });
const window = dom.window;
window.HTMLDialogElement.prototype.showModal = function () { this.open = true; };
window.HTMLDialogElement.prototype.close = function () { this.open = false; };
const config = JSON.parse(await readFile(new URL("../internal/ipsentinel/testdata/contracts/ipsentinel-executor-v1/examples/config.json", import.meta.url)));
const hello = JSON.parse(await readFile(new URL("../internal/ipsentinel/testdata/contracts/ipsentinel-executor-v1/examples/hello.response.json", import.meta.url))).result;
const now = Date.now();
hello.status.budget.day_utc = new Date(now).toISOString().slice(0, 10);
hello.status.assessments[0].valid_until = new Date(now + 86400000).toISOString();
const view = { node_id: "ready", binding: { mode: "managed", epoch: 1 }, received_at: Math.floor(now / 1000), config, hello };
let views = [view];
const writes = [];
window.fetch = async (url, options = {}) => {
  if (options.method) {
    assert.equal(url, "/api/v1/admin/nodes/ready/executors/ipsentinel/actions");
    writes.push(JSON.parse(options.body));
    return new Response(JSON.stringify({ id: "accepted" }));
  }
  if (url === "/api/v1/admin/nodes") return new Response(JSON.stringify([{ id: "ready", name: '<img src=x onerror="alert(1)">' }]));
  if (url === "/api/v1/admin/executors") return new Response(JSON.stringify(views));
  throw new Error("Unexpected request: " + url);
};
window.eval(bundle.outputFiles[0].text);
const ui = window.guardUI;
const service = config.services[0];
assert.equal(ui.guardRunReason(view, service, "baseline", now), "");
assert.equal(ui.guardRunReason(view, service, "experiment", now), "");
for (const mutate of [
  changed => { changed.received_at -= 60; },
  changed => { changed.binding.mode = "managed_readonly"; },
  changed => { changed.hello.status.control.paused = true; },
  changed => { changed.hello.status.control.draining = true; },
  changed => { changed.hello.status.control.active_run_id = "running"; },
  changed => { changed.hello.status.health = "unavailable"; },
  changed => { changed.hello.status.budget = null; },
  changed => { changed.hello.status.budget.day_utc = "2000-01-01"; },
  changed => { changed.hello.status.budget.used_runs = 8; },
  changed => { changed.hello.status.budget.limits.network_bytes = 1; },
]) {
  const changed = structuredClone(view);
  mutate(changed);
  assert(ui.guardRunReason(changed, service, "baseline", now));
}
const noExperiment = structuredClone(service);
delete noExperiment.experiment;
assert(ui.guardRunReason(view, noExperiment, "experiment", now));
const assessment = hello.status.assessments[0];
assert.equal(ui.guardEvidence(assessment, view, now), "检测到送中跳转");
assert.equal(ui.guardEvidence({ ...assessment, state: "change_confirmed" }, view, now), "送中跳转消失，连续复测通过");
assert.equal(ui.guardEvidence({ ...assessment, valid_until: "not-a-date" }, view, now), "暂无有效证据");
assert.equal(ui.guardEvidence(assessment, { ...view, received_at: 1 }, now), "状态已过期，请刷新");
const content = window.document.querySelector("main");
let setup = 0;
await ui.renderIPGuard(content, () => { setup++; });
assert.equal(writes.length, 0);
assert.equal(content.querySelector("img"), null);
assert.equal(content.querySelector("textarea"), null);
assert.equal(content.querySelectorAll("[data-guard-run]").length, 3);
for (const phase of ["baseline", "experiment", "followup"]) {
  content.querySelector(`[data-guard-run="${phase}"]`).onclick();
  assert.equal(writes.length, ["baseline", "experiment", "followup"].indexOf(phase));
  if (phase === "experiment") assert(window.document.querySelector("#dialog").textContent.includes("不能保证"));
  await window.document.querySelector("#confirm").onclick();
  const request = writes.at(-1);
  assert.equal(request.kind, "run.submit");
  assert.equal(request.phase, phase);
  assert.equal(request.purpose, phase === "experiment" ? "experiment" : "observe");
  assert.equal(request.service_id, "google");
  assert.equal(request.config_revision, hello.status.config.revision);
}
assert.equal(new Set(writes.map(item => item.job_id)).size, 3);
content.querySelector('[data-guard-run="experiment"]').onclick();
views = [{ ...view, config: { ...config, daily_budget: { ...config.daily_budget, runs: 7 } } }];
await window.document.querySelector("#confirm").onclick();
assert.equal(writes.length, 3, "configuration changed during confirmation must never submit");
assert(window.document.querySelector("#toast").textContent.includes("配置已变化"));
views = [{ ...view, hello: { ...hello, status: { ...hello.status, control: { ...hello.status.control, paused: true } } } }];
await ui.renderIPGuard(content, () => { setup++; });
assert([...content.querySelectorAll("[data-guard-run]")].every(button => button.disabled));
views = [];
await ui.renderIPGuard(content, () => { setup++; });
assert(content.textContent.includes("先添加一个防护节点"));
await content.querySelector("[data-guard-setup]").onclick();
assert.equal(setup, 1);
dom.window.close();
console.log("PASS: IP guard detection, recovery attempts, clean followups, evidence expiry, confirmation fences, budgets and empty states.");
