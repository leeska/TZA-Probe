import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { readFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { remainingValueLabel } from "../web/format.js";
const require = createRequire(path.join(process.env.TZA_UI_DEPS || "/tmp/tza-ui-deps", "package.json"));
const { JSDOM } = require("jsdom"), { build } = require("esbuild");
const root = fileURLToPath(new URL("../", import.meta.url));
const source = await readFile(path.join(root,"web/app.js"),"utf8");
const contents = source.slice(0,source.lastIndexOf("\nstart().catch(")) + `
window.valueView = {gridInner,listInner,liveTick,setNodes(value){nodes=value;}};`;
const bundle = await build({stdin:{contents,resolveDir:path.join(root,"web"),sourcefile:"app.js"},bundle:true,format:"iife",write:false});
const dom = new JSDOM('<div id="app"><div id="nodes"></div></div>',{url:"http://127.0.0.1/",runScripts:"outside-only"});
const w = dom.window;w.eval(bundle.outputFiles[0].text);
const node = {id:"value",name:"Value",public:true,online:false,billing:{price:30,currency:"USD",price_period:"月"},line:{},display:{},probe_targets:[],traffic_used:0,remaining_value:15};
const helpers = w.valueView, host = w.document.querySelector("#nodes");
for (const render of [helpers.gridInner,helpers.listInner]) {
 host.innerHTML = `<article class="node-card" data-node="value">${render(node)}</article>`;
 helpers.setNodes([structuredClone(node)]);
 assert.equal(host.querySelector(".remaining-value").textContent,"剩余价值 USD 15.00");
 w.fetch = async () => new Response(JSON.stringify([{id:"value",remaining_value:14.5}]),{headers:{"Content-Type":"application/json"}});
 await helpers.liveTick();
 assert.equal(host.querySelector(".remaining-value").textContent,"剩余价值 USD 14.50","live tick must update card and row estimates");
 host.innerHTML=render({...node,remaining_value:null});
 assert.equal(host.querySelector(".remaining-value").textContent,"剩余价值 未配置");
 host.innerHTML=render({...node,billing:{currency:'<img src=x onerror=bad()>'}});
 assert(!host.querySelector(".remaining-value img"),"currency must be escaped");
}
assert.equal(remainingValueLabel({...node,remaining_value:0}),"USD 0.00");
assert.equal(remainingValueLabel({...node,remaining_value:null}),"未配置");
assert.equal(remainingValueLabel({...node,remaining_value:NaN}),"未配置");
assert.equal(remainingValueLabel({...node,billing:{}}),"15.00（币种未设）");
dom.window.close();
console.log("PASS: remaining value, unknown/zero distinction, currency escaping and live card/list updates.");
