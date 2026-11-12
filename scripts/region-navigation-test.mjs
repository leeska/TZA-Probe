import assert from "node:assert/strict";
import { createRequire } from "node:module";
import path from "node:path";
import { fileURLToPath } from "node:url";

const require = createRequire(path.join(process.env.TZA_UI_DEPS || "/tmp/tza-ui-deps", "package.json"));
const { JSDOM } = require("jsdom");
const { build } = require("esbuild");
const root = fileURLToPath(new URL("../", import.meta.url));
const bundle = await build({ stdin: { contents: 'export { mountAdmin } from "./web/admin.js"; export { state, go } from "./web/admin-state.js";', resolveDir: root }, bundle: true, format: "iife", globalName: "adminUI", write: false });
for (const installed of [true, false]) {
  const dom = new JSDOM('<div id="app"></div><dialog id="dialog"></dialog><div id="toast"></div>', { url: "http://localhost/admin", runScripts: "outside-only" });
  const { window } = dom;
  window.AbortController = AbortController;
  window.matchMedia = () => ({ matches: false, addEventListener() {} });
  window.HTMLDialogElement.prototype.close = function () { this.open = false; };
  const reads = [];
  window.fetch = async (url, options = {}) => {
    assert.ok(!options.method || options.method === "GET", "navigation must not write settings");
    reads.push(url);
    const data = {
      "/api/v1/session": { version: "1.18.0" },
      "/api/v1/admin/nodes": [],
      "/api/v1/admin/plugins": installed ? [{ id: "ipsentinel-runtime", kind: "agent", version: "0.8.2" }] : [],
      "/api/v1/admin/ipguard": { nodes: [], jobs: [], services: [], worker_seen: 0 },
    };
    assert.ok(Object.hasOwn(data, url), url);
    return new Response(JSON.stringify(data[url]));
  };
  window.eval(bundle.outputFiles[0].text);
  const ui = window.adminUI;
  ui.state.tab = "ipguard";
  await ui.mountAdmin(window.document.querySelector("#app"));
  const link = window.document.querySelector('[data-tab="ipguard"]');
  assert.equal(link.previousElementSibling.dataset.tab, "tasks");
  assert.equal(link.textContent, "地区偏移检测");
  assert.equal(link.getAttribute("aria-current"), "page");
  assert.equal(window.document.querySelector("#breadcrumb").textContent, "地区偏移检测");
  assert.equal(ui.state.tab, "ipguard");
  assert.equal(window.document.querySelector("[data-plugin-back]"), null);
  assert.equal(reads.includes("/api/v1/admin/ipguard"), installed);
  if (installed) {
    assert.match(window.document.querySelector("#admin-content").textContent, /先添加节点/);
    await ui.go("ipguard");
    assert.equal(window.document.querySelectorAll("[data-node-plugin-detail]").length, 1);
  } else {
    assert.match(window.document.querySelector("#admin-content").textContent, /尚未安装 IPSentinel/);
    assert.ok(window.document.querySelector("[data-region-install]"));
  }
  dom.window.close();
}
console.log("PASS: sidebar direct entry, aligned active state and breadcrumb, installed/uninstalled handling, read-only navigation.");
