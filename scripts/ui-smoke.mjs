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
// 数字框的步进箭头靠样式表关掉。伪元素在 jsdom 里算不出来，所以箭头样式按内容断言，
// 「已生效」则单独起一个小文档确认计算值。
const styles = await readFile(path.join(root, "web/styles.css"), "utf8");
assert.match(styles, /::-webkit-inner-spin-button/, "WebKit 的步进箭头同样要关掉");
const cssProbe = new JSDOM(`<style>${styles}</style><input type="number" id="probe">`);
assert.equal(
  cssProbe.window.getComputedStyle(cssProbe.window.document.getElementById("probe")).appearance,
  "textfield",
  "数字框不应再画步进箭头",
);
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
    if (await predicate()) return;
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
set(form, "region", "香港");
set(form, "quota", 2000);
set(form, "price", 12.8);
set(form, "expires_at", "2027-09-18 03:04:05");
// 重置时间与到期时间都精确到秒：重置时间默认落在当月 1 日 00:00:00（UTC），
// 旧的「每月重置日」数字框与步进箭头整体取消。
assert.equal(form.elements.billing_day, undefined, "旧的每月重置日数字框应已删除");
assert.equal(form.elements.billing_at.type, "text");
assert.match(form.elements.billing_at.value, /^\d{4}-\d{2}-01 00:00:00$/, "重置时间默认值: " + form.elements.billing_at.value);
assert.match(form.elements.billing_at.placeholder, /\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}/, "输入框要给出带秒的格式示例");
assert.match(form.elements.expires_at.placeholder, /\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}/);
// 滚动不再误改数值：滚到数字框上时先失焦（jsdom 自身不会按滚轮改值，只验证这个约定）。
const quota = form.elements.quota;
quota.focus();
assert.equal(w.document.activeElement, quota);
quota.dispatchEvent(new w.Event("wheel", { bubbles: true }));
assert.notEqual(w.document.activeElement, quota, "滚轮经过数字框时必须失焦，避免滑动页面时误改数值");
// 每个提醒阈值都要在界面上写明默认值（标签里写明，或提示行里列出）。
const rules = $("#node-form").textContent;
for (const d of ["（默认 60）", "（默认 7）", "（默认 80）", "（默认 90）", "（默认 300）"])
  assert(rules.includes(d), "提醒规则缺少默认值提示: " + d);
assert(rules.includes("新建节点的默认值：离线等待 60 秒"), "提示行要列出各项默认值");
assert.equal(form.elements.repeat_minutes.placeholder, "默认 0（只提醒一次）");
// 内置目录只有国内三网，而节点多半在海外：建议列表必须补上常用海外地区，且仍可自由填写。
const nodeRegionOptions = [...w.document.querySelectorAll("#region-options option")].map((o) => o.value);
for (const region of ["香港", "美国", "日本", "新加坡"])
  assert(nodeRegionOptions.includes(region), `节点地区建议列表缺少 ${region}（共 ${nodeRegionOptions.length} 项）`);
assert(form.elements.region.getAttribute("list") === "region-options", "节点地区仍要允许输入目录外的值");
assert(nodeRegionOptions.length > 30, "建议列表不能只剩目录里的省份");
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
assert.equal(created.expires_at, "2027-09-18T03:04:05Z", "到期时间要带上输入框里的秒");
assert.match(created.billing_at, /^\d{4}-\d{2}-01T00:00:00Z$/, "重置时间 = " + created.billing_at);
// 还没上报地址族的节点：不给出无从探测的 IPv4/IPv6 切换，网络区块整体隐藏。
$(`[data-inspect="${created.id}"]`).click();
await until(() => $("#history-range"), "node detail and history controls");
assert.equal(w.document.querySelector("[data-detail-family]"), null, "a host with no reported family must not offer an address-family switch");
assert(w.document.querySelector(".family-tag"), "a host with no reported family still shows a fixed tag");
assert.equal(w.document.querySelector("#network-overview"), null, "network blocks stay hidden without a usable address family");
$(".close").click();
// 详情页任一请求失败时，弹窗必须说明原因并给出重试，而不是停在“读取节点详情…”。
const realFetch = w.fetch;
w.fetch = async (input, opts = {}) => {
  if (String(input).includes("/observations"))
    return new Response(JSON.stringify({ error: "详情接口故障" }), {
      status: 500,
      headers: { "Content-Type": "application/json" },
    });
  return realFetch(input, opts);
};
$(`[data-inspect="${created.id}"]`).click();
await until(() => $(".detail-error"), "detail failure notice");
assert(
  $(".detail-error").textContent.includes("详情接口故障"),
  "the dialog must show why the detail request failed",
);
assert.equal($(".loading"), null, "a failed request must not leave the loading state behind");
w.fetch = realFetch;
// 重试时节点已从列表消失（被删除或转为非公开）：必须给出说明，而不是留下空弹窗。
w.fetch = async (input, opts = {}) => {
  const response = await realFetch(input, opts);
  if (!String(input).endsWith("/nodes")) return response;
  const list = await response.json();
  return new Response(JSON.stringify(list.filter((x) => x.id !== created.id)), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
};
$(".detail-error button").click();
await until(
  () => $(".detail-error")?.textContent.includes("已不在列表里"),
  "notice when the node disappeared from the list",
);
assert.equal($(".loading"), null, "a vanished node must not leave an empty dialog");
w.fetch = realFetch;
$(".detail-error button").click();
await until(() => $(".family-tag"), "detail after retry");
assert.equal($(".detail-error"), null, "a successful retry must clear the failure notice");
$(".close").click();
// Agent 上报双栈后，卡片与详情页都要能按地址族切换。
const report = await fetch(base + "/api/v1/agent/report", {
  method: "POST",
  headers: { Authorization: "Bearer " + nodeToken, "Content-Type": "application/json" },
  body: JSON.stringify({
    protocol: 1,
    metrics: { counter_id: "ui-smoke", cpu: 1, memory_used: 1, memory_total: 2, disk_used: 1, disk_total: 2, net_in: 0, net_out: 0, rate_in: 0, rate_out: 0, uptime: 60, os: "smoke", arch: "amd64", version: "smoke", ip_families: ["ipv4", "ipv6"] },
  }),
});
assert.equal(report.status, 204, "agent report carrying both address families must be accepted");
$("#refresh-nodes").click();
await until(() => $(`[data-inspect="${created.id}"]`), "node list after the agent report");
$(`[data-inspect="${created.id}"]`).click();
// 关闭后的对话框只是 open=false，旧标记仍在文档里，所以要等新内容真的渲染出来。
await until(() => $("[data-detail-family=ipv6]"), "address-family switch after the agent report");
assert($("#network-overview"), "a dual-stack host must show the network blocks");
$("[data-detail-family=ipv6]").click();
assert($("[data-detail-family=ipv6]").classList.contains("active"));
$(".close").click();
$(`[data-edit="${created.id}"]`).click();
form = $("#node-form");
assert.equal(form.elements.name.value, name);
assert.equal(form.elements.hold_seconds.value, "300");
assert.match(form.elements.billing_at.value, /^\d{4}-\d{2}-01 00:00:00$/, "重置时间要能原样回填");
assert.equal(form.elements.expires_at.value, "2027-09-18 03:04:05", "到期时间回填必须保留秒");
assert.equal(form.elements.latency_region.value, "", "latency region defaults to empty (falls back to the representative region)");
set(form, "latency_region", "上海");
submit(form);
await until(async () => {
  list = await (await fetch(base + "/api/v1/admin/nodes", { headers: adminHeaders })).json();
  return list.find((n) => n.id === created.id)?.latency_region === "上海";
}, "latency region saved and read back");
$(`[data-edit="${created.id}"]`).click();
form = $("#node-form");
assert.equal(form.elements.latency_region.value, "上海", "saved latency region must reappear in the form");
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
  representative: "", traffic_limit: 0, traffic_mode: "sum", billing_at: "2026-09-01T00:00:00Z", price: 0,
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
// 搜索同时支持中文、全拼、拼音首字母和代号（web/search.js）：
// 上海 = 上海 / shanghai / sh…，电信 = 电信 / dianxin / dx / ct / telecom。
const searchCatalog = (text) => {
  picker.value = text;
  picker.dispatchEvent(new w.Event("input", { bubbles: true }));
  return [...w.document.querySelectorAll("[data-target]")].map((i) => i.dataset.target);
};
const regionOf = (target) => target.split(" / ")[0];
assert.deepEqual(searchCatalog("hebei"), searchCatalog("河北"), "全拼 hebei 必须与中文同结果");
assert.equal(searchCatalog("hebei").length, 3);
assert(searchCatalog("hebei").every((t) => regionOf(t) === "河北"));
// hb 是河北与湖北共同的首字母，两个都应命中：不能漏，也不能只挑一个。
const initialHit = searchCatalog("hb").map(regionOf);
assert(initialHit.length === 6 && new Set(initialHit).size === 2 && ["河北", "湖北"].every((r) => initialHit.includes(r)), `hb 应命中河北与湖北，实际 ${JSON.stringify(initialHit)}`);
assert.equal(searchCatalog("ct").length, 31, "代号 ct 必须命中全部电信目标");
assert(searchCatalog("ct").every((t) => t.endsWith("电信")));
assert.equal(searchCatalog("telecom").length, 31, "英文 telecom 与代号 ct 同结果");
// hb 同时是河北与湖北的首字母，AND 之后两个省各剩电信一组。
assert.equal(searchCatalog("hb ct").length, 2, "空格分隔的关键词是 AND：hb + ct");
assert.deepEqual(searchCatalog("hb ct").map(regionOf).sort(), ["河北", "湖北"]);
assert.equal(searchCatalog("hebei ct").length, 1, "全拼 hebei 只命中河北");
assert.equal(searchCatalog("hb dx").length, 2, "混用首字母与拼音缩写也要能命中");
const catalogHosts = (await (await fetch(base + "/api/v1/admin/catalog", { headers: adminHeaders })).json()).targets.map((x) => x.host);
assert(searchCatalog(catalogHosts[0]).length >= 1, "目录地址本身也要能搜到");
picker.value = "";
picker.dispatchEvent(new w.Event("input", { bubbles: true }));
form = $("#dialog form");
assert(form.elements.tcp.checked && form.elements.route.checked);
assert(form.elements.ipv4.checked && form.elements.ipv6.checked);
// 每组目标有自己的 IP 协议：上方勾选只是新加入时的默认值。
const chipFamilies = (label) =>
  [...w.document.querySelectorAll(".target-chip")]
    .find((c) => c.querySelector("strong").textContent.includes(label))
    ?.querySelectorAll(".chip-family.on");
assert.equal(chipFamilies("电信").length, 2, "a freshly selected group follows the global default");
[...w.document.querySelectorAll(".target-chip")]
  .find((c) => c.querySelector("strong").textContent.includes("电信"))
  .querySelector('.chip-family[data-family="ipv6"]')
  .click();
assert.equal(chipFamilies("电信").length, 1, "per-group toggle must turn one family off");
assert.equal(chipFamilies("联通").length, 2, "other groups keep their own families");
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
// 用真实租约灌一条延迟结果和一条回程结果：列表必须把两种数据分开并标注类型。
const lease = await (
  await fetch(base + "/api/v1/agent/jobs", { headers: { Authorization: "Bearer " + nodeToken } })
).json();
assert(lease.length >= 2, "a bound task must hand out jobs to its agent");
for (const job of lease) {
  const result = job.task.kind === "route"
    ? { job_id: job.id, status: "ok", sent: 1, received: 1, latency: 152.4, route: "CN2GIA", hops: [{ ttl: 1, address: "59.43.*.*", asn: "AS4809", rtt: 12.3 }] }
    : { job_id: job.id, status: "ok", sent: 3, received: 3, latency: 21.5, route: "未知", hops: [] };
  const posted = await fetch(base + "/api/v1/agent/results", {
    method: "POST",
    headers: { Authorization: "Bearer " + nodeToken, "Content-Type": "application/json" },
    body: JSON.stringify(result),
  });
  assert.equal(posted.status, 204, "an agent result must be accepted");
}
$('[data-tab="nodes"]').click();
await until(() => $(`[data-inspect="${created.id}"]`), "node list after the agent results");
$(`[data-inspect="${created.id}"]`).click();
await until(() => $(".trace-group"), "the backhaul list");
const headings = [...w.document.querySelectorAll(".trace-group h4")].map((h) => h.textContent);
assert.equal(headings.length, 1, "only the backhaul list stays in the dialog: " + headings);
assert(headings[0].includes("三网回程"), "the backhaul list must be titled: " + headings);
// 延迟只由多折线图呈现：单目标的「查看趋势」折叠图和「地区延迟」行列表都应不复存在。
assert($("#network-overview"), "the dialog must keep the multi-line overview chart");
// 折线图要铺满 svg 盒子：手机盒子宽度只有两百来像素，默认的 preserveAspectRatio="meet"
// 会把绘图区等比缩小后垂直居中，折线和网格线就落不到 8/59/110 三条刻度线上；铺满后
// 描边必须不跟着缩放（否则横向压缩把线宽压到半像素）。横轴标签一律绝对定位钉在刻度上。
assert(
  /svg viewBox="0 0 680 120" preserveAspectRatio="none"/.test(
    await (await fetch(base + "/detail.js")).text(),
  ),
  "the multi-line overview must stretch its plot to its box",
);
// 聚焦视图（点卡片上的某一格）不再给「查看全部信息」，改成时间范围下拉；网络历史最多 7 天，
// 所以那里的下拉只有 1 小时 / 24 小时 / 7 天三档，主机历史所在的综合视图保留 30 天。
const detailSource = await (await fetch(base + "/detail.js")).text();
const appSource = await (await fetch(base + "/app.js")).text();
// 数值每秒轻刷一次（只换数字，不重建卡片），探测明细仍是 10 秒一轮。
assert(
  /\}, 1000\);\n\s*setInterval\(\(\) => \{\n\s*if \(!document\.hidden\) refresh\(\)\.catch/.test(appSource),
  "the page must refresh the numbers every second and the probe details every 10s",
);
assert(
  /async function liveTick\(\)/.test(appSource) && /liveBusy/.test(appSource),
  "the one-second tick must exist and skip itself while a request is in flight",
);
// 滚动守卫已删：它原来的理由是「滚动中替换元素会弹位置」，而写入现在只改文本/属性和不换元素，
// 实测连续滚动中每 250ms 写一次文本，位置 2015→9440 单调、回退 0 次 —— 数字不必再停下来。
assert(
  !/scrolling\(\)/.test(appSource) && !/lastScrollAt/.test(appSource) && !/watchScrolling/.test(appSource),
  "the scroll-freeze guard must be gone - text-only writes do not move the view while scrolling",
);
// 贴底判定只认 8px：以前 48px，把停在离底 40px 的人每 10 秒拽到最底（现场反馈的「停下还是会跳」）。
assert(
  /const atBottom = scroller\.scrollHeight - scroller\.scrollTop - scroller\.clientHeight < 8;/.test(appSource),
  "the bottom pin must only apply within 8px - 48px yanked everyone sitting just above the bottom",
);
// 没变就不写 DOM：每秒那层只该写真正在动的数字。
assert(
  /function setHTML\(el, html\) \{\n\s*if \(!el \|\| el\.__html === html\) return;/.test(appSource) &&
    /if \(!patchInto\(el, fresh\)\) el\.innerHTML = html;/.test(appSource) &&
    /function setText\(el, text\) \{\n\s*if \(el && el\.textContent !== text\)/.test(appSource),
  "the one-second tick must compare before writing so untouched numbers never hit the DOM",
);
// 每秒那层与 10 秒那层都不再因滚动而暂停：取数回来直接写（写入本身是补丁式的，不动元素）。
assert(
  /const next = await api\("\/nodes"\);\n\s*nodes = next;/.test(appSource) &&
    /if \(liveBusy\) return;/.test(appSource),
  "both refresh loops must write straight after the fetch, with no scroll pause left",
);
// 贴底时重绘要续贴底部；并且重绘后要把滚动调回被盯住的那张卡片。
assert(
  /if \(atBottom\) scroller\.scrollTop = scroller\.scrollHeight/.test(appSource),
  "a re-render while pinned to the page bottom must stay pinned",
);
// 卡片集合没变时绝不能整块重绘（整块重绘会重建整个滚动子树，最容易被感知为「跳」）：
// 必须逐张比 HTML，只替换内容真的变了的那几张。
assert(
  /const sameSet = shown\.length === filtered\.length && shown\.every\(\(el, i\) => el\.dataset\.node === filtered\[i\]\.id\)/.test(appSource) &&
    /if \(renderedHTML\.get\(el\) === wanted\[i\]\) return;/.test(appSource) &&
    /const renderedHTML = new WeakMap\(\)/.test(appSource),
  "an unchanged card list must be updated card by card, never re-rendered wholesale",
);
// 手动锚定已删：逐张替换后位置由浏览器自己的滚动锚定保持（WebKit / Chromium 都实现了），
// 实测每次校正差值都是 0，留着只是空转。真正贴底的情况由上面那行续贴。
assert(
  !/anchorOf\(/.test(appSource) && !/scroller\.scrollTop \+=/.test(appSource),
  "the manual scroll anchoring must be gone (the browser keeps the position itself)",
);
// 逐张替换只能喂卡片内容（cardInner），喂整张卡（card）会把卡片嵌进自己里面：
// 外层 article 里再套一个同名 article，卡片高度、坐标、点击全部跟着错。
assert(
  /const wanted = filtered\.map\(cardInner\)/.test(appSource) &&
    /const cardShell = \(n, inner\) =>/.test(appSource) &&
    /function card\(n\) \{\n\s*return cardShell\(n, cardInner\(n\)\);/.test(appSource) &&
    /function patchInto\(el, next\)/.test(appSource) &&
    /if \(!patchInto\(el, fresh\)\) el\.innerHTML = wanted\[i\];/.test(appSource),
  "a per-card re-render must patch the card in place (whole-card replacement only as a structure fallback)",
);
// 桌面 Chrome 会把 body 的 overflow 传播给视口，WebKit 不一定：html/body 上不留 overflow-y: auto，
// 免得 body 自己变成滚动元素（那时 scrollY 恒 0、所有 scrollTop 写入落空）。
const cssSource = await (await fetch(base + "/styles.css")).text();
assert(
  !/html\s*\{[^}]*overflow-y/.test(cssSource) && !/body\s*\{[^}]*overflow-y/.test(cssSource),
  "html and body must not carry overflow-y - it can make the body itself the scroller",
);
assert(!/show-all|查看全部信息/.test(detailSource), "the focused view must not keep a 查看全部信息 button");
assert(
  /focusKind !== "route" \? historyRange\(168\)/.test(detailSource),
  "the focused latency view must offer the period dropdown",
);
assert(/hours <= maxHours/.test(detailSource), "the period dropdown must drop ranges the history cannot serve");
assert.equal(
  [...$("#history-range").options].length,
  4,
  "the combined view keeps all four host-history ranges",
);
assert(
  /\.chart svg \* \{[^}]*vector-effect:\s*non-scaling-stroke/.test(styles),
  "a stretched chart must keep its stroke width",
);
assert(
  /\.chart-axis span \{[^}]*position:\s*absolute/.test(styles) &&
    /\.chart-axis span:nth-child\(5\) \{ left: 100%; \}/.test(styles),
  "axis labels must be pinned to their tick, not spaced out by flex",
);
assert.equal($("#network-history"), null, "per-target trend rows must be gone");
assert(
  !/查看趋势/.test($(".modal-body")?.textContent || ""),
  "no per-target trend links may remain",
);
assert.equal(w.document.querySelector(".trace-kind"), null, "type chips are obsolete without latency rows");
$(".close").click();
// 回到网络探测页，继续后面的任务编辑流程。
$('[data-tab="tasks"]').click();
await until(() => $('[data-edit-task="' + createdTask.id + '"]'), "task list again");
// 列表里的「地区 / 运营商」按地区归并：同一个地区名只出现一次，运营商并排列在它后面，
// 超过三个地区折成「…等 N 个地区」，完整名单放在单元格悬停里。
const regionCell = $(`[data-edit-task="${createdTask.id}"]`).closest("tr").querySelector('td[data-label="地区 / 运营商"]');
const taskRegions = [...new Set(createdTask.targets.map((x) => x.region))];
for (const r of taskRegions) assert(regionCell.textContent.includes(r), `地区 ${r} 必须出现在列表里: ${regionCell.textContent}`);
for (const r of taskRegions)
  assert.equal((regionCell.textContent.match(new RegExp(r, "g")) || []).length, 1, `地区名 ${r} 不应按运营商重复: ${regionCell.textContent}`);
assert.equal(regionCell.title.split("、").length, taskRegions.length, "悬停里要是完整名单");
assert.equal(/…/.test(regionCell.textContent), taskRegions.length > 3, "只有超过三个地区才折行: " + regionCell.textContent);
if (taskRegions.length > 3) assert(/共 \d+ 个地区/.test(regionCell.textContent), "折行时要写明地区总数: " + regionCell.textContent);
else assert(!/个地区/.test(regionCell.textContent), "地区不多时不该再写数量: " + regionCell.textContent);
// 三组目标：电信只留 IPv4，其余双栈
assert.equal(createdTask.targets.length, 5);
assert.deepEqual(
  createdTask.targets.filter((x) => x.carrier === "电信").map((x) => x.family),
  ["ipv4"],
  "a group limited to IPv4 must not save its IPv6 address",
);
assert.equal(createdTask.route_interval, 3600);
assert.equal(createdTask.interval, 60);
$('[data-edit-task="' + createdTask.id + '"]').click();
await until(() => $("#task-form"), "edit combined task");
assert.equal(w.document.querySelectorAll("[data-remove]").length, 3);
// 自定义目标的地区候选同样要带上常用海外地区。
const taskRegionOptions = [...w.document.querySelectorAll("#task-region-options option")].map((o) => o.value);
for (const region of ["香港", "美国"])
  assert(taskRegionOptions.includes(region), `自定义目标地区候选缺少 ${region}（共 ${taskRegionOptions.length} 项）`);
assert($("#task-form").elements.ipv6.checked);
assert.equal(chipFamilies("电信").length, 1, "per-group families must round-trip through save and reopen");
assert.equal(chipFamilies("联通").length, 2);
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
assert.equal(editedTask.targets.length, 7, "5 catalog addresses (电信 only IPv4) plus the custom dual-stack pair");
assert.equal(editedTask.targets.filter((x) => x.carrier === "其他").length, 2);
assert.equal(editedTask.node_ids.length, 1);
// 节点搜索同样支持拼音与代号（web/search.js）：香港节点用 hk / hongkong / xg / xianggang / 中文 都能搜到。
$('[data-bind-task="' + createdTask.id + '"]').click();
await until(() => $("#node-search"), "bind dialog");
const nodeQuery = $("#node-search");
const shownNodes = () => [...w.document.querySelectorAll("[data-node]")].map((i) => i.closest("label").textContent.trim());
for (const query of ["香港", "hk", "HK", "hongkong", "xg", "xianggang", "xiang", "gang"]) {
  nodeQuery.value = query;
  nodeQuery.dispatchEvent(new w.Event("input", { bubbles: true }));
  assert(shownNodes().some((t) => t.includes(name)), `「${query}」应搜到香港节点，实际 ${JSON.stringify(shownNodes())}`);
}
nodeQuery.value = "美国";
nodeQuery.dispatchEvent(new w.Event("input", { bubbles: true }));
assert(!shownNodes().some((t) => t.includes(name)), "美国 不该命中香港节点");
$(".close").click();
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
    // 少了这个，jsdom 的 document.hidden 就是 true，面板里「每秒轻刷 / 10 秒全量」两处
    // setInterval 会被 if (!document.hidden) 全部跳过，测不到自动刷新后的界面。
    pretendToBeVisual: true,
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
// 公开页搜索同样吃拼音与代号：hk / xianggang / 香港 都要能搜到那台香港节点。
for (const query of ["hk", "xianggang", "xg", "香港"]) {
  publicSearch.value = query;
  publicSearch.dispatchEvent(new publicDOM.window.Event("input"));
  assert(publicDOM.window.document.body.textContent.includes(name), `公开页「${query}」应搜到香港节点`);
}
publicSearch.value = "";
publicSearch.dispatchEvent(new publicDOM.window.Event("input"));
// 延迟按运营商分四格显示，与三网回程同构；地区取延迟地区，其次代表地区。
// 数据目录跨多次运行复用，所以要按名字找到本轮创建的卡片，不能取第一张。
const card = [...publicDOM.window.document.querySelectorAll(".node-card")].find((c) =>
  c.textContent.includes(name),
);
assert(card, "the node created in this run must appear on the public panel");
const cardText = card.textContent;
for (const carrier of ["电信", "联通", "移动", "其他"])
  assert(cardText.includes(carrier), `latency grid must list ${carrier}`);
assert.equal(card.querySelector(".latency-grid").children.length, 4, "latency grid has four carrier cells");
// 窄卡片里四个运营商改成一家一行（容器查询），行内用 latency-row / loss-row 定位。
assert.equal(
  card.querySelectorAll(".latency-grid .latency-row").length,
  4,
  "each latency cell must mark its latency row for the stacked layout",
);
assert.equal(card.querySelectorAll(".latency-grid .loss-row").length, 4, "each latency cell must mark its loss row");
assert(
  /@container node-card \(max-width: 430px\)/.test(styles),
  "narrow cards must stack the carrier cells one per line",
);
// 滚动锚定必须留给浏览器：每 10 秒整块替换卡片列表，内容在视口上方变高变矮时靠它稳住视图，
// 关掉反而会让页面自己移动。贴底的情况由 app.js 的 renderCards 单独兜住。
assert(
  !/overflow-anchor:\s*none/.test(styles),
  "the stylesheet must not disable the browser's scroll anchoring",
);
assert(cardText.includes("TCP 延迟"), "latency section keeps its heading");
assert(cardText.includes("上海"), "the card shows the latency region it was configured with");
// 延迟 / 丢包条按 styles.css 的 q1..q20 上色：颜色必须来自 class，不能是行内 style，
// 因为页面 CSP 是 style-src 'self'，行内样式会被浏览器直接忽略（曾因此整条色带失效）。
for (const bar of card.querySelectorAll(".quality-bar")) {
  assert(!bar.hasAttribute("style"), "quality bars must not rely on inline styles (CSP blocks them)");
  if (bar.classList.contains("idle")) continue;
  assert(
    [...bar.classList].some((c) => /^q\d+$/.test(c)),
    `quality bar must carry a qN colour class, got "${bar.className}"`,
  );
  // 每个格子是一次探测，所以必须带那次自己的时间，而不是整行的聚合提示。
  assert(/·/.test(bar.getAttribute("title") || ""), `each cell needs its own probe tooltip, got "${bar.getAttribute("title")}"`);
}
const findCard = () =>
  [...publicDOM.window.document.querySelectorAll(".node-card")].find((c) =>
    c.textContent.includes(name),
  );
const clickedBtn = card.querySelector('[data-card-family="ipv6"]');
const clickedCardNode = card.dataset.node;
const clickedBtnNode = clickedBtn.dataset.cardNode;
clickedBtn.click();
// 切换会重绘整个列表，所以要重新取一次卡片。
assert(
  findCard().querySelector('[data-card-family="ipv6"]').classList.contains("active"),
  "the public card switches to IPv6 on its own",
);
// 机器只上报 IPv4 时：开关里就只剩 IPv4 这一段（不摆一个点不动的灰段），
// 宽度由外层定宽的 .family-slot 补齐，所以后面的 linux / 架构标签整列对齐。
// Server 侧 3 秒内不受理第二次上报（ErrConflict），所以先等过这个窗口。
await new Promise((resolve) => setTimeout(resolve, 3200));
const singleReport = await fetch(base + "/api/v1/agent/report", {
  method: "POST",
  headers: { Authorization: "Bearer " + nodeToken, "Content-Type": "application/json" },
  body: JSON.stringify({
    protocol: 1,
    metrics: { counter_id: "ui-smoke", cpu: 1, memory_used: 1, memory_total: 2, disk_used: 1, disk_total: 2, net_in: 0, net_out: 0, rate_in: 0, rate_out: 0, uptime: 60, os: "smoke", arch: "amd64", version: "smoke", ip_families: ["ipv4"] },
  }),
});
assert.equal(singleReport.status, 204, "agent report carrying a single address family must be accepted");
// 公开页每 10 秒才全量刷一次（每秒那层只换数字），所以等过一轮再让它重画。
await new Promise((resolve) => setTimeout(resolve, 11500));
publicSearch.value = "";
publicSearch.dispatchEvent(new publicDOM.window.Event("input"));
await until(() => {
  const c = findCard();
  return c && c.querySelectorAll(".segmented.card-family button").length === 1;
}, "single-stack address-family switch");
{
  const single = findCard();
  const segs = single.querySelectorAll(".segmented.card-family button");
  assert.equal(segs.length, 1, "单栈机器只给一段（不摆点不动的灰段）");
  assert.equal(segs[0].textContent, "IPv4", "留下的就是机器真有的那一族");
  assert(segs[0].classList.contains("active"), "唯一那段是选中态");
  assert.equal(
    single.querySelector('[data-card-family="ipv6"]'),
    null,
    "没有 IPv6 就不给 IPv6 那一段",
  );
  assert(single.querySelector(".family-slot"), "开关要套在定宽槽位里");
  assert.equal(
    single.querySelectorAll(".os-box .os-tag, .os-box .arch-tag").length,
    2,
    "系统与架构标签照旧在开关后面",
  );
  // 槽位定宽，单栈与双栈行后面的标签才对得齐（真机实测两行标签同在同一个 x）。
  assert(
    /\.node-grid\.node-list \.family-slot \{[^}]*min-width: 94px/.test(styles),
    "列表里地址族开关的槽位要定宽",
  );
}
// 整张卡片现在可点开综合信息，所以卡片内的控件必须被排除，否则点协议开关会误弹详情
assert(
  !publicDOM.window.document.querySelector("#dialog[data-node-id]"),
  "switching the address family must not open the detail dialog",
);
// 格子自己带聚焦信息：延迟格 → 该运营商延迟，回程格 → 该运营商回程
const latencyCell = findCard().querySelector(".latency-grid .route-label");
assert.equal(latencyCell.dataset.focusKind, "latency", "latency cell must carry its focus kind");
assert(latencyCell.dataset.focusCarrier, "latency cell must carry its carrier");
const routeCell = findCard().querySelector(".route-grid:not(.latency-grid) .route-label");
assert.equal(routeCell.dataset.focusKind, "route", "route cell must carry its focus kind");
assert(routeCell.dataset.focusCarrier, "route cell must carry its carrier");
const switchedId = clickedBtnNode;
// 展示方式开关：工具栏右侧切「卡片 / 列表」。列表视图一行一台、六格横向对齐，
// 在线绿点贴在名字前面（不单独占一列），并且所有文字给全——不省略、不截断。
const viewSwitch = publicDOM.window.document.querySelector(".view-switch");
assert(viewSwitch, "工具栏要有卡片 / 列表的展示方式开关");
const cardTab = viewSwitch.querySelector('[data-view="card"]');
const listTab = viewSwitch.querySelector('[data-view="list"]');
assert(cardTab && listTab, "开关要给出卡片与列表两个选项");
assert(cardTab.classList.contains("active"), "默认是卡片视图");
const cardViewIds = [...publicDOM.window.document.querySelectorAll("#nodes .node-card")].map((c) => c.dataset.node);
assert(
  !publicDOM.window.document.querySelector("#nodes").classList.contains("node-list"),
  "卡片视图不该带列表类名",
);
listTab.click();
assert(
  publicDOM.window.document.querySelector("#nodes").classList.contains("node-list"),
  "切到列表视图后容器要换成列表布局",
);
assert(listTab.classList.contains("active") && !cardTab.classList.contains("active"), "开关的高亮要跟着换");
assert.equal(
  publicDOM.window.localStorage.getItem("tza-view"),
  "list",
  "展示方式要记在浏览器本地",
);
const listRows = [...publicDOM.window.document.querySelectorAll("#nodes .node-card")];
assert.equal(listRows.length, cardViewIds.length, "切视图不能多出或少掉节点");
assert.deepEqual(
  listRows.map((c) => c.dataset.node),
  cardViewIds,
  "切视图要保持节点的顺序与身份",
);
const listRow = listRows.find((c) => c.textContent.includes(name));
assert(listRow, "列表视图里也要有本轮创建的节点");
assert.deepEqual(
  [...listRow.children].map((c) => c.className.split(" ")[0]),
  ["row-identity", "metrics-grid", "speeds-cell", "net-cell", "route-cell", "billing-cell"],
  "列表行固定六格：主机 / 指标 / 网速 / 延迟 / 回程 / 计费",
);
// 状态不单独占一列：绿点就在名字前面的 .node-identity 里。
const identityCell = listRow.querySelector(".row-identity .node-identity");
assert(identityCell, "主机格里要先给状态点与名字");
assert(identityCell.querySelector(".node-dot"), "在线绿点要贴在名字前面，而不是另占一列");
assert.equal(
  identityCell.querySelector(".node-dot").classList.contains("offline"),
  false,
  "在线节点的点是绿的",
);
assert.equal(
  identityCell.querySelector(".node-name").textContent,
  name,
  "节点名要给全，不做省略",
);
// 延迟与回程在列表视图里是行内文本（卡片那格是方形格子），每家运营商一行。
assert.equal(listRow.querySelectorAll(".net-cell .net-line").length, 4, "延迟四行（含其他）");
assert.equal(listRow.querySelectorAll(".route-cell .route-line").length, 3, "回程三行");
for (const line of listRow.querySelectorAll(".net-line")) {
  assert(line.dataset.focusKind === "latency" || line.dataset.focusKind === "route", "行内文本要带聚焦信息");
  assert(line.dataset.focusCarrier, "行内文本要带运营商");
  assert(line.getAttribute("title"), "行内文本要有各自的探测时间提示");
}
const listSubs = [...listRow.querySelectorAll(".metrics-grid .metric small")].map((s) => s.textContent);
assert.equal(listSubs.length, 4, "四个指标各留一行副标");
assert(
  listSubs.every((t) => t.trim().length),
  "指标副标不能为空",
);
assert(
  /额度/.test(listSubs[3]),
  `周期用量的副标要写明额度与计费方式，实际是「${listSubs[3]}」`,
);
// 文字完整性：省略号来自样式表，jsdom 不套用 CSS，所以按样式表文本断言。
assert(
  /\.node-grid\.node-list \.metric small \{[^}]*text-overflow:\s*clip/.test(styles),
  "列表视图的指标副标要显式取消省略号",
);
assert(
  /\.node-grid\.node-list \.route-line strong \{[^}]*white-space:\s*normal/.test(styles),
  "列表视图的回程标签要换行显示完整值",
);
assert(
  !/\.node-grid\.node-list[^{]*\{[^}]*text-overflow:\s*ellipsis/.test(styles),
  "列表视图里不该有任何省略号",
);
// 观感（用户点名两处）：延迟色带要够大（固定 120px、9px 高的格子），实时网速竖排成两行。
assert(
  /\.node-grid\.node-list \.net-line \.quality-bars \{[^}]*flex: 0 0 120px/.test(styles),
  "列表里的延迟色带要固定 120px 宽",
);
assert(
  /\.node-grid\.node-list \.net-line \.quality-bar \{[^}]*height: 9px !important/.test(styles),
  "列表里的延迟色带格子要 9px 高",
);
assert(
  /\.node-grid\.node-list \.speeds \{[^}]*grid-template-columns: minmax\(0, 1fr\)/.test(styles),
  "列表里的实时网速要竖排两行",
);
// 观感（用户点名）：网速格不用那么宽，做成一行一个方向的窄条：图标 + 数值靠右，标签收进 title。
const speedLines = [...listRow.querySelectorAll(".speeds-cell .speed-line")];
assert.equal(speedLines.length, 2, "网速一行一个方向（下载 / 上传）");
assert(
  speedLines.every((l) => l.querySelector(".speed-icon") && l.querySelector("strong")),
  "每条网速行要有图标与数值",
);
assert(
  speedLines.every((l) => l.getAttribute("title")),
  "标签收进 title，鼠标停上去仍能看出方向",
);
assert.equal(
  listRow.querySelectorAll(".speeds-cell .speed-box").length,
  0,
  "列表视图不该再出现卡片里的宽方块",
);
assert(
  /\.node-grid\.node-list \.speed-line \{[^}]*justify-content: space-between/.test(styles),
  "网速行要图标靠左、数值靠右",
);

// 架构（Agent 报的 GOARCH）要标在系统标签下面；卡片视图不动，两边都出这个标签。
assert(
  /class="arch-tag"/.test(appSource) && /m\?\.arch/.test(appSource),
  "要用 Agent 上报的架构渲染 arch-tag",
);
assert(
  /\.node-grid\.node-list \.os-box \{[^}]*flex-direction: column/.test(styles),
  "列表里系统标签要竖排，架构落在 linux 下面",
);

// 用户点名：「全部地区 / 自动匹配地区」是占位值不是信息，渲染层不能再出现这两个字样。
assert(
  !/"全部地区"|"自动匹配地区"/.test(appSource),
  "不再渲染「全部地区 / 自动匹配地区」占位字样",
);
// 手机端：一行仍是六格表格，放不下就整块横向滚动，不能竖排成卡片（那就与卡片视图没区别了）。
assert(
  /@media \(max-width: 1320px\)[\s\S]*?\.node-grid\.node-list \{[^}]*overflow-x: auto/.test(styles),
  "窄屏下列表要横向滚动",
);
assert(
  /\.node-grid\.node-list \.node-card \{[^}]*min-width: calc\(var\(--row-min\)/.test(styles),
  "列表行要撑到六格的最小宽度，横向滚动才有内容可滚",
);
// --row-min 必须等于 --row-cols 六个 min 之和，否则窄屏滚动的宽度与实际列宽对不上。
const rowCols = styles.match(/--row-cols:([^;]+);/)?.[1] || "";
const rowMin = styles.match(/--row-min: (\d+)px/);
const colMins = [...rowCols.matchAll(/minmax\((\d+)px/g)].map((m) => Number(m[1]));
const colMinSum = colMins.reduce((a, b) => a + b, 0);
assert.equal(colMins.length, 6, "列表行固定六格");
assert(rowMin, "列表样式里要写出 --row-min");
assert.equal(
  Number(rowMin[1]),
  colMinSum,
  `--row-min 要等于六格 min 之和（${colMinSum}）`,
);
cardTab.click();
assert(
  !publicDOM.window.document.querySelector("#nodes").classList.contains("node-list"),
  "切回卡片视图要换回栅格布局",
);
assert.equal(publicDOM.window.localStorage.getItem("tza-view"), "card", "选择要跟着一起写回");
// 刷新后仍然生效：新起一个页面，localStorage 里已经写着 list。
const listDOM = new JSDOM(
  '<div id="app"></div><div id="toast"></div><dialog id="dialog"></dialog>',
  {
    url: base + "/",
    runScripts: "outside-only",
    virtualConsole: consoleAdapter,
    pretendToBeVisual: true,
  },
);
listDOM.window.localStorage.setItem("tza-view", "list");
listDOM.window.fetch = (input, opts) => fetch(new URL(input, base), opts);
listDOM.window.HTMLDialogElement.prototype.showModal = function () {
  this.open = true;
};
listDOM.window.HTMLDialogElement.prototype.close = function () {
  this.open = false;
};
listDOM.window.eval(bundle.outputFiles[0].text);
await until(
  () => listDOM.window.document.querySelector("#nodes.node-list .node-card"),
  "localStorage 里的列表选择要在打开页面时生效",
);
listDOM.window.close();
const listCards = [...publicDOM.window.document.querySelectorAll("#nodes .node-card")];
const cardIds = listCards.map((c) => c.dataset.node);
// 期望值取自接口本身（测试库里的节点会被前面的用例增删，写死数量只会造假失败）。
const apiNodes = await (await fetch(base + "/api/v1/nodes")).json();
const apiList = Array.isArray(apiNodes) ? apiNodes : apiNodes.nodes || [];
const expectedIds = apiList
  .filter((n) => !n.searchQueryMismatch) // 保留接口全部（公开页会用同样的筛选逻辑）
  .map((n) => n.id);
const dupEntry = cardIds.find((id, i) => cardIds.indexOf(id) !== i);
assert(
  !dupEntry || apiList.filter((n) => n.id === dupEntry).length > 1,
  `the renderer must not duplicate a node the API returned once (dup=${dupEntry}, cards=${cardIds.length}, api=${apiList.length})`,
);
assert(
  cardIds.length === apiList.length && cardIds.every((id) => apiList.some((n) => n.id === id)),
  `the card list must match the API exactly (cards=${cardIds.length}, api=${apiList.length}, missing=${expectedIds.filter((id) => !cardIds.includes(id)).slice(0, 3)}, extra=${cardIds.filter((id) => !apiList.some((n) => n.id === id)).slice(0, 3)})`,
);
const strayFamily = listCards.find(
  (c) =>
    c.dataset.node !== switchedId &&
    c.querySelector('[data-card-family="ipv4"]') &&
    !c.querySelector('[data-card-family="ipv4"]').classList.contains("active"),
);
assert(
  !strayFamily,
  `another card's address family must not follow this card's switch (offender: ${strayFamily?.dataset.node}, switched: ${switchedId})`,
);
// 卡片里绝不能套卡片（曾经把整张卡喂给 innerHTML，外层 article 里又长出一个同名 article）。
assert.equal(
  publicDOM.window.document.querySelectorAll("#nodes .node-card .node-card").length,
  0,
  "a card must never contain another card",
);
// 折线图的悬停读数在多折线图上也成立：鼠标停在哪一列，就报那一列每个序列的数值。
const chartBundle = await build({
  entryPoints: [path.join(root, "web/charts.js")],
  bundle: true,
  format: "iife",
  globalName: "TZACharts",
  write: false,
});
w.eval(chartBundle.outputFiles[0].text);
w.SVGElement.prototype.getBoundingClientRect = () => ({
  x: 0, y: 0, top: 0, left: 0, right: 680, bottom: 120, width: 680, height: 120,
});
const probe = w.document.createElement("figure");
probe.className = "chart";
probe.dataset.series = JSON.stringify([
  { label: "四川 · 电信", full: "四川 · 电信 · 118.112.1.144", unit: "ms", color: "lc0", points: [[0, 10], [600, 20], [1200, 30]] },
  { label: "四川 · 联通 · 119.6.217.13", unit: "ms", color: "lc1", points: [[0, 40], [600, 50], [1200, 60]] },
]);
probe.dataset.range = "0,1200";
probe.dataset.ceiling = "100";
probe.innerHTML = '<div class="chart-body"><svg viewBox="0 0 680 120"></svg></div>';
w.document.body.append(probe);
// 单序列的图要额外把数据点画在曲线上，用同一套几何（120 高的 viewBox、0 在 y=110）。
const single = w.document.createElement("figure");
single.className = "chart";
single.dataset.series = JSON.stringify([
  { label: "四川 · 电信", unit: "ms", color: "lc0", points: [[0, 10], [600, 20], [1200, 30]] },
]);
single.dataset.range = "0,1200";
single.dataset.ceiling = "100";
single.innerHTML = '<div class="chart-body"><svg viewBox="0 0 680 120"></svg><div class="chart-axis"></div></div>';
w.document.body.append(single);
w.TZACharts.wireCharts(w.document.body);
const chartBody = probe.querySelector(".chart-body");
chartBody.dispatchEvent(new w.MouseEvent("pointermove", { clientX: 340, clientY: 40, bubbles: true }));
assert(!probe.querySelector(".chart-hover").hidden, "hovering a chart must reveal the readout");
const tip = probe.querySelector(".chart-tip");
assert.equal(tip.querySelectorAll("span").length, 2, "a multi-line chart reports every series");
assert(/20\.0 ms/.test(tip.textContent) && /50\.0 ms/.test(tip.textContent), "the readout must show the values at that column, got " + tip.textContent);
const guide = probe.querySelector(".chart-guide");
// 竖线只覆盖绘图区：位置按 svg 的实际盒子算（不是按 .chart-body 的百分比，那会被坐标轴带偏）。
assert(Math.abs(parseFloat(guide.style.left) - 340) < 0.1, "the guide must follow the pointer column, got " + guide.style.left);
assert(Math.abs(parseFloat(guide.style.top) - 0) < 0.1 && Math.abs(parseFloat(guide.style.height) - 120) < 0.1, "the guide must stay inside the plot, got " + guide.style.top + "/" + guide.style.height);
assert.equal(probe.querySelectorAll(".chart-tip i.lc0, .chart-tip i.lc1").length, 2, "each series keeps its own colour chip");
// 读数框先试绘图区正中，那一带有曲线就让到空的一侧。探针两条序列在 y=59 / 89.6，居中的
// 那一带被占住、曲线整体偏下，于是贴上沿（y=0）。jsdom 里 offsetHeight 为 0。
assert.equal(tip.style.top, "0px", "the readout must dodge the curve instead of covering it, got " + tip.style.top);
assert.equal(tip.querySelectorAll("span")[0].title, "四川 · 电信 · 118.112.1.144", "each row keeps the full target in its title");
assert(parseFloat(tip.style.left) >= 0 && parseFloat(tip.style.left) <= 680, "the readout must stay inside the chart, got " + tip.style.left);
// 单序列：数据点必须落在曲线的同一点上（value 20 在 120 高的绘图区里是 y=89.6）。
const singleBody = single.querySelector(".chart-body");
singleBody.dispatchEvent(new w.MouseEvent("pointermove", { clientX: 340, clientY: 40, bubbles: true }));
const singleTip = single.querySelector(".chart-tip");
const dot = single.querySelector(".chart-dot");
assert(/20\.0 ms/.test(singleTip.textContent), "a single-line chart reports its value, got " + singleTip.textContent);
// 曲线不在中点那一带时，读数框就留在正中（value 20 → y=89.6，离中心很远）。
assert.equal(singleTip.style.top, "60px", "a clear centre keeps the readout centred, got " + singleTip.style.top);
assert(!dot.hidden, "a single-line chart must mark the point it is reading");
assert(Math.abs(parseFloat(dot.style.left) - 340) < 0.1 && Math.abs(parseFloat(dot.style.top) - 89.6) < 0.1, "the dot must sit on the line, got " + dot.style.left + "/" + dot.style.top);
chartBody.dispatchEvent(new w.MouseEvent("pointerleave", { bubbles: true }));
assert(probe.querySelector(".chart-hover").hidden, "leaving the chart must hide the readout");
// 多折线图（带图例）不飘浮层：读数写进图例、时间写进标题行，曲线区域不被遮挡。
const multi = w.document.createElement("figure");
multi.className = "chart";
multi.dataset.series = JSON.stringify([
  { label: "四川 · 电信", unit: "ms", color: "lc0", points: [[0, 10], [600, 20], [1200, 30]] },
  { label: "四川 · 联通", unit: "ms", color: "lc1", points: [[0, 40], [600, 50], [1200, 60]] },
]);
multi.dataset.range = "0,1200";
multi.dataset.ceiling = "100";
multi.innerHTML =
  '<figcaption><strong>延迟趋势</strong><span>最近 24 小时</span></figcaption>' +
  '<div class="chart-body"><svg viewBox="0 0 680 120"></svg></div>' +
  '<div class="chart-readout"><b data-readout-time>2020/01/01 00:00</b>' +
  '<span><i class="lc0"></i>四川 · 电信<em data-legend-value="0">30.0 ms</em></span>' +
  '<span><i class="lc1"></i>四川 · 联通<em data-legend-value="1">60.0 ms</em></span>' +
  "</div>";
w.document.body.append(multi);
w.TZACharts.wireCharts(w.document.body);
const multiBody = multi.querySelector(".chart-body");
multiBody.dispatchEvent(new w.MouseEvent("pointermove", { clientX: 340, clientY: 40, bubbles: true }));
assert(
  multi.querySelector(".chart-tip").hidden,
  "the readout box must live outside the plot (the floating box covered the curves)",
);
assert(
  /^\d{4}\/\d{2}\/\d{2} \d{2}:\d{2}$/.test(multi.querySelector("[data-readout-time]").textContent),
  "the readout must carry the hovered time, got " + multi.querySelector("[data-readout-time]").textContent,
);
assert.equal(
  [...multi.querySelectorAll("[data-legend-value]")].map((e) => e.textContent).join(" "),
  "20.0 ms 50.0 ms",
  "hovering must write the column values into the legend",
);
multiBody.dispatchEvent(new w.MouseEvent("pointerleave", { bubbles: true }));
assert.notEqual(
  multi.querySelector("[data-readout-time]").textContent,
  "2020/01/01 00:00",
  "leaving the chart must put the readout back to the latest sample",
);
assert.equal(
  [...multi.querySelectorAll("[data-legend-value]")].map((e) => e.textContent).join(" "),
  "30.0 ms 60.0 ms",
  "leaving the chart must fall back to the latest sample",
);
multi.remove();
probe.remove();
single.remove();
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
