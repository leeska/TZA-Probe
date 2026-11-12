import assert from "node:assert/strict";
import { createRequire } from "node:module";
import path from "node:path";
import { fileURLToPath } from "node:url";

const require = createRequire(path.join(process.env.TZA_UI_DEPS || "/tmp/tza-ui-deps", "package.json"));
const { JSDOM } = require("jsdom");
const { build } = require("esbuild");
const root = fileURLToPath(new URL("../", import.meta.url));
const bundle = await build({ stdin: { contents: 'export * from "./web/admin-node-plugin-detail.js"; export { state } from "./web/admin-state.js";', resolveDir: root }, bundle: true, format: "iife", globalName: "pluginUI", write: false });
const dom = new JSDOM('<main id="admin-content"></main><dialog id="dialog"></dialog><div id="toast"></div>', { url: "http://localhost/admin", runScripts: "outside-only" });
const { window } = dom;
window.HTMLDialogElement.prototype.showModal = function () { this.open = true; };
window.HTMLDialogElement.prototype.close = function () { this.open = false; };
const writes = [], reads = [];
window.fetch = async (url, options = {}) => {
  if (options.method) {
    writes.push(url);
    return new Response("{}", { status: 500 });
  }
  reads.push(url);
  assert.equal(url, "/api/v1/admin/ipguard");
  return new Response(JSON.stringify({ nodes: [], jobs: [], services: [], worker_seen: 0 }));
};
window.eval(bundle.outputFiles[0].text);
const ui = window.pluginUI;
ui.state.container = window.document;
// 地区偏移检测是内置功能：打开即直接读取出口与任务，不查插件列表、不发任何安装请求。
await ui.renderRegionPage(Promise.resolve());
await new Promise((resolve) => setTimeout(resolve, 20));
assert.equal(ui.state.tab, "ipguard");
assert.deepEqual(writes, []);
assert.deepEqual(reads, ["/api/v1/admin/ipguard"]);
assert.equal(window.document.querySelector("[data-install-manager]"), null);
assert.equal(window.document.querySelector("[data-region-install]"), null);
assert.match(window.document.querySelector("main").textContent, /先添加节点/);
dom.window.close();
console.log("PASS: built-in region detection page, no installation step or requests.");
