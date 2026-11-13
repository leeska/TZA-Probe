import assert from "node:assert/strict";
import { createRequire } from "node:module";
import path from "node:path";
import { fileURLToPath } from "node:url";

const require = createRequire(path.join(process.env.TZA_UI_DEPS || "/tmp/tza-ui-deps", "package.json"));
const { JSDOM } = require("jsdom");
const { build } = require("esbuild");
const root = fileURLToPath(new URL("../", import.meta.url));
const bundle = await build({ stdin: { contents: 'export * from "./web/admin-ip-guard.js";', resolveDir: root }, bundle: true, format: "iife", globalName: "guardUI", write: false });
const dom = new JSDOM('<main></main>', { url: "http://127.0.0.1", runScripts: "outside-only" });
const { window } = dom;
window.eval(bundle.outputFiles[0].text);
const ui = window.guardUI;
const now = Math.floor(Date.now() / 1000);
const policy = { revision: 1, enabled: true, auto_recover: false, expected_ip: "8.8.8.8", country: "US", locale: "en-US", timezone: "UTC", services: ["google", "meta"] };
const node = { id: "node", name: "node", policy, egress: { enabled: true, received_at: now, allowed_ips: ["8.8.8.8"] } };
// 结果判读：只认这台节点当前目标下完成的检测；验证页面、访问受限、过期、校正待复测都不算正常。
const job = { node_id: "node", service: "google", phase: "baseline", state: "completed", finished_at: now, policy: node.policy, result: { assessment: "target_observed", page: { signal: "google_page_country", value: node.policy.country } } };
assert.equal(ui.serviceReading(node, "google", [job]).label, "正常");
const challengedWithTerms = { ...job, state: "failed", result: { reason: "challenge_detected", assessment: "unknown", page: { outcome: "unknown" }, google_terms: { assessment: "target_observed", page: { signal: "google_terms_country", value: node.policy.country } } } };
assert.equal(ui.serviceReading(node, "google", [challengedWithTerms]).label, "未知");
assert.equal(ui.serviceReading(node, "google_terms", [challengedWithTerms]).label, "正常");
assert.equal(ui.serviceReading(node, "google_terms", [job]).label, "待检测");
assert.equal(ui.serviceReading(node, "google", [{ ...job, state: "failed", result: { assessment: "unknown", reason: "http_denied" } }, job]).label, "访问受限");
assert.equal(ui.serviceReading(node, "meta", [{ ...job, service: "meta", state: "failed", result: { assessment: "unknown", reason: "http_forbidden" } }]).label, "访问受限");
assert.equal(ui.serviceReading(node, "google", [{ ...job, result: { assessment: "other_region", page: { signal: "google_page_country", value: "JP" } } }]).label, "地区偏移");
assert.match(ui.serviceReading(node, "google", [job]).note, /实际：.*US.*目标：.*US/);
assert.equal(ui.serviceReading(node, "google", [{ ...job, result: { assessment: "target_observed", page: { signal: "homepage_hkredirect", value: "NO_HKREDIRECT" } } }]).label, "未知", "old redirect checks cannot confirm selected target");
for (const target of ["CN", "JP", "US", "DE"]) {
  const selected = { ...node, policy: { ...node.policy, country: target } };
  for (const actual of ["CN", "JP", "US", "DE"]) {
    const observation = { ...job, policy: selected.policy, result: { assessment: actual === target ? "target_observed" : "other_region", page: { signal: "google_page_country", value: actual } } };
    assert.equal(ui.serviceReading(selected, "google", [observation]).label, actual === target ? "正常" : "地区偏移");
  }
}
assert.equal(ui.serviceReading(node, "google", [{ ...job, policy: { ...node.policy, country: "JP" } }]).label, "待检测");
assert.equal(ui.serviceReading(node, "google", [{ ...job, finished_at: now - 90000 }]).label, "已过期");
assert.equal(ui.serviceReading(node, "google", [{ ...job, phase: "experiment" }]).label, "待复测");
assert.match(ui.nodeEnable(), /--ipguard$/);
assert.match(ui.serverInstall, /--with-browser$/);
dom.window.close();
console.log("PASS: region drift readings (target match, challenge with terms region, access denied, drift, stale, pending correction) and setup commands.");
