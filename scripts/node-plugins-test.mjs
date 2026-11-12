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
    assert.equal(url,"/api/v1/admin/market/install");
    return new Response(JSON.stringify({ version:"0.5.0",source:"market" }));
  }
  reads.push(url);
  assert.equal(url,"/api/v1/admin/ipguard");
  return new Response(JSON.stringify({nodes:[],jobs:[],services:[],worker_seen:0}));
};
window.eval(bundle.outputFiles[0].text);
const ui = window.pluginUI;
ui.state.container = window.document;
const pkg = {id:"ipsentinel-runtime",type:"executor",versions:[{version:"0.5.0",compatible:true}]};
await ui.openNodePluginDetail(pkg);
assert.equal(writes.length,0);
assert.match(window.document.body.textContent,/不再安装 Chromium/);
window.document.querySelector("[data-install-manager]").click();
assert.equal(writes.length,0);
window.document.querySelector("#confirm").click();
await new Promise(resolve => setTimeout(resolve,20));
assert.deepEqual(writes,["/api/v1/admin/market/install"]);
assert.deepEqual(reads,["/api/v1/admin/ipguard"]);
assert.equal(window.document.querySelector("[data-plugin-section]"),null);
assert.match(window.document.querySelector("main").textContent,/先添加节点/);
dom.window.close();
console.log("PASS: single central plugin entry, explicit signed-manager installation, no legacy installation tabs or requests.");
