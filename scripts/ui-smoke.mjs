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
// 断言卡片上的数字用页面自己的格式化函数：写死字符串的话，格式化改了断言还在绿。
const { bytes } = await import(new URL("../web/format.js", import.meta.url).href);
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
      (w.document?.querySelector("#toast")?.textContent ?? "(没有管理端文档)"),
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
    metrics: { counter_id: "ui-smoke", cpu: 1, memory_used: 1, memory_total: 2, disk_used: 1, disk_total: 2, net_in: 0, net_out: 0, rate_in: 0, rate_out: 0, uptime: 60, os: "smoke", distro: "Debian 13", arch: "amd64", cpu_cores: 4, version: "smoke", ip_families: ["ipv4", "ipv6"] },
  }),
});
assert.equal(report.status, 204, "agent report carrying both address families must be accepted");
$("#refresh-nodes").click();
await until(() => $(`[data-inspect="${created.id}"]`), "node list after the agent report");
$(`[data-inspect="${created.id}"]`).click();
// 关闭后的对话框只是 open=false，旧标记仍在文档里，所以要等新内容真的渲染出来。
await until(() => $("[data-detail-family=ipv6]"), "address-family switch after the agent report");
// 这台节点此刻还没绑定任何探测任务：详情页不给空图表、不给「请在后台创建并绑定任务」，
// 只给一行说明（探测段在任务绑定后、结果回来时才画）。
assert(!$("#network-overview"), "没有任务的节点不该拿到一张空图表");
assert(!$("#route-details"), "没有任务的节点不该拿到回程占位");
assert.equal($(".probe-none")?.textContent, "仅主机指标", "没有任务时详情页给一行说明");
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
  [...$("#history-range").options].map((o) => o.value).join(","),
  "1,6,24,72,168,720",
  "the combined view offers every host-history range up to the 30-day retention",
);
assert.equal(
  [...$("#history-range").options].length,
  [...$("#history-range").options].filter((o) => /最近/.test(o.textContent)).length,
  "every range option is labelled",
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
// 改任务会把该任务的历史探测结果一并清掉（storage.SaveTask 更新时 DELETE FROM jobs），
// 所以公开页要看数值，就得在最后一次改完之后再租一次、把结果发回去——否则卡片只有结构和
// 「未检测」，值那一层的断言全是空转。
const leaseAgain = await (
  await fetch(base + "/api/v1/agent/jobs", { headers: { Authorization: "Bearer " + nodeToken } })
).json();
assert(leaseAgain.length >= 2, "编辑后重新租约要能拿到作业（历史已被改任务清掉）");
for (const job of leaseAgain) {
  const result = job.task.kind === "route"
    ? { job_id: job.id, status: "ok", sent: 1, received: 1, latency: 152.4, route: "CN2GIA", hops: [{ ttl: 1, address: "59.43.*.*", asn: "AS4809", rtt: 12.3 }] }
    : { job_id: job.id, status: "ok", sent: 3, received: 3, latency: 21.5, route: "未知", hops: [] };
  const posted = await fetch(base + "/api/v1/agent/results", {
    method: "POST",
    headers: { Authorization: "Bearer " + nodeToken, "Content-Type": "application/json" },
    body: JSON.stringify(result),
  });
  assert.equal(posted.status, 204, "an agent result must be accepted after the task edit");
}
// ===== 解锁检测：后台任务编辑器 + 真数据 =====
// 节点先建出来并上报一次带 unlock 能力的指标（不报这个能力，服务端不会把解锁任务派给它）。
const unlockNodeName = "解锁验收 " + Date.now();
const madeUnlock = await fetch(base + "/api/v1/admin/nodes", {
  method: "POST",
  headers: { ...adminHeaders, "Content-Type": "application/json" },
  body: JSON.stringify({
    name: unlockNodeName, region: "日本", group: "JP", public: true,
    billing_at: "2026-09-01T00:00:00Z", traffic_mode: "sum",
  }),
});
assert.equal(madeUnlock.status, 200, "解锁验收节点要能创建: " + (await madeUnlock.clone().text()));
const unlockNode = await madeUnlock.clone().json();
const unlockReport = await fetch(base + "/api/v1/agent/report", {
  method: "POST",
  headers: { Authorization: "Bearer " + unlockNode.token, "Content-Type": "application/json" },
  body: JSON.stringify({ protocol: 1, metrics: {
    counter_id: "ui-smoke-unlock", cpu: 3, memory_used: 1, memory_total: 2, disk_used: 1, disk_total: 2,
    net_in: 10, net_out: 5, rate_in: 0, rate_out: 0, uptime: 3600, os: "linux", distro: "Debian 13",
    arch: "amd64", cpu_cores: 1, version: "smoke", ip_families: ["ipv4"], capabilities: ["unlock"],
  } }),
});
assert.equal(unlockReport.status, 204, "带 unlock 能力的指标上报要被接受: " + (await unlockReport.clone().text()));
// 用后台的任务编辑器建一个解锁任务：类型切到「解锁检测」后不该再有目标 / 端口，间隔最短 3600。
// 先切到节点页等新节点出现——render() 是异步的，直接点「创建任务」会拿刷新前的 nodes。
$('[data-tab="nodes"]').click();
await until(
  () => [...w.document.querySelectorAll("tr")].some((tr) => tr.textContent.includes(unlockNodeName)),
  "unlock node in the admin node list",
);
$('[data-tab="tasks"]').click();
await until(() => $("#add-task"), "tasks page for unlock");
$("#add-task").click();
await until(() => $("#task-form"), "task editor for unlock");
{
  const form = $("#task-form");
  assert.deepEqual(
    [...form.querySelectorAll('input[name="kind"]')].map((r) => r.value),
    ["probe", "unlock"],
    "任务类型要给出网络探测与解锁检测两项",
  );
  assert.equal(form.elements.kind.value, "probe", "新建默认是网络探测");
  assert(!form.querySelector("#probe-sections").hidden && form.querySelector("#unlock-section").hidden, "网络探测默认显示目标区块、隐藏解锁设置");
  const unlockRadio = form.querySelector('input[name="kind"][value="unlock"]');
  unlockRadio.checked = true;
  unlockRadio.dispatchEvent(new w.Event("change", { bubbles: true }));
  assert(form.querySelector("#probe-sections").hidden, "选了解锁检测就不该再给目标与检测内容");
  assert(!form.querySelector("#unlock-section").hidden, "解锁检测要有自己的间隔 / 协议设置");
  assert.equal(form.elements.unlock_interval.min, "3600", "解锁检测间隔最短 3600 秒");
  assert.equal(form.elements.unlock_interval.value, "3600", "新建默认 3600 秒");
  assert.equal(form.elements.unlock_family.value, "", "协议默认自动（纯 v6 节点也能跑）");
  assert.equal(form.querySelector("#unlock-section").querySelectorAll("[data-target], input[name='custom-port']").length, 0, "解锁区块里没有目标 / 端口控件");
  form.elements.name.value = "解锁检测验收";
  form.elements.namedItem("node:" + unlockNode.id).checked = true;
  submit(form);
  await until(() => !$("#dialog").open, "unlock task saved");
}
await until(
  () => [...w.document.querySelectorAll("tr")].some((tr) => tr.textContent.includes("解锁检测验收")),
  "unlock task row in the list",
);
const unlockTasks = await (await fetch(base + "/api/v1/admin/tasks", { headers: adminHeaders })).json();
const unlockTask = unlockTasks.find((t) => t.name === "解锁检测验收");
assert(unlockTask, "解锁任务要落库");
assert.equal(unlockTask.kind, "unlock", "任务要以 kind=unlock 落库");
assert.equal(unlockTask.interval, 3600, "解锁间隔要落库");
assert.deepEqual(unlockTask.targets ?? [], [], "解锁任务不带目标");
assert(unlockTask.node_ids.includes(unlockNode.id), "解锁任务要绑上节点");
{
  const row = [...w.document.querySelectorAll("tr")].find((tr) => tr.textContent.includes("解锁检测验收"));
  assert(row, "解锁任务要出现在任务列表里");
  assert(row.textContent.includes("解锁检测"), "列表里要认得出它是解锁任务: " + row.textContent);
  assert(row.querySelector('td[data-label="目标"]').textContent.includes("内置目录"), "目标格写「内置目录」，不写地址数: " + row.querySelector('td[data-label="目标"]').textContent);
}
// 编辑已有解锁任务：直接进解锁表单，回填间隔。
$('[data-edit-task="' + unlockTask.id + '"]').click();
await until(() => $("#task-form"), "edit unlock task");
{
  const form = $("#task-form");
  assert(form.querySelector('input[name="kind"][value="unlock"]').checked, "编辑解锁任务要直接进解锁表单");
  assert(form.querySelector("#probe-sections").hidden, "编辑解锁任务不显示目标区块");
  assert.equal(form.elements.unlock_interval.value, "3600", "编辑要回填间隔");
  assert.equal(form.elements.name.value, "解锁检测验收", "编辑要回填名称");
  $(".close").click();
}
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
// 面板显示：延迟色带的时间窗后台可控（默认 60 分钟）。改完读回来确认落库，再改回默认，
// 免得后面那批色带断言跑在更窄的窗口上。
const windowField = $("#latency-window");
assert.equal(windowField.value, "60", "未设置时该显示默认 60 分钟（1 小时）");
assert.equal(
  [...windowField.options].map((o) => o.value).join(","),
  "60,360,1440,4320,10080",
  "时间范围该给到保留上限为止的档位",
);
const saveWindow = async (minutes) => {
  // 保存成功会重渲染整页，所以每次都要重新取输入框和按钮。
  $("#latency-window").value = String(minutes);
  $("#save-window").click();
  await until(
    async () => {
      const view = await (
        await fetch(base + "/api/v1/admin/settings", { headers: { Cookie: cookie } })
      ).json();
      return Number(view.latency_window_minutes) === minutes;
    },
    `latency window ${minutes}`,
  );
  assert.equal(
    $("#latency-window").value,
    String(minutes),
    "重渲染后输入框该显示已保存的时间窗",
  );
};
await saveWindow(1440);
await saveWindow(60);
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
// 探测段的格子按服务端给的「探测计划」先画好，数值要等这台机器的观测回来。等到色带里出现
// 实心格（非 idle）再断言：否则这些断言只看结构，值那一层整段是空转的——曾经一个渲染期的
// ReferenceError 被 refresh().catch(toast) 吞掉，测试照样全绿（真浏览器里整页报错）。
await until(
  () =>
    [...publicDOM.window.document.querySelectorAll(".node-card")]
      .find((c) => c.textContent.includes(name))
      ?.querySelector(".latency-bands .quality-bar:not(.idle)"),
  "public card values after the observations arrive",
);
assert.equal(
  publicDOM.window.document.querySelector("#toast").textContent,
  "",
  "公开页渲染不能抛错（抛了会被 refresh().catch(toast) 吞掉）",
);
assert(
  /\d+ ms/.test(card.querySelector(".latency-bands .net-band .band-metric strong").textContent),
  `延迟要给出数值，实际「${card.querySelector(".latency-bands .net-band .band-metric strong").textContent}」`,
);
assert.notEqual(
  card.querySelector(".route-grid .route-label strong").textContent,
  "未知",
  "回程标签要按真实结果渲染",
);
const cardText = card.textContent;
// 格子只画这台机器真有任务的组：它挂的是「上海 电信 / 联通 / 移动」，自定义的「其他」
// 目标在新加坡，而卡片配的延迟地区是上海——不在卡片地区里的组不给格子。以前固定画满四格，
// 那格永远「未检测」，看着像没加载完。
for (const carrier of ["电信", "联通", "移动"])
  assert(cardText.includes(carrier), `latency grid must list ${carrier}`);
assert(
  !cardText.includes("其他"),
  "卡片地区之外的探测组（自定义的新加坡目标）不占一格",
);
assert.equal(card.querySelectorAll(".latency-bands .net-band").length, 3, "延迟只画真有任务的三网（一家一块）");
// 服务端给的 probe_targets 里确实带着那些探测项（含自定义的「其他」目标），是卡片按地区筛掉的。
const plannedNode = (await (await fetch(base + "/api/v1/nodes")).json()).find(
  (x) => x.id === card.dataset.node,
);
assert(plannedNode, "节点要在公开列表里");
assert(
  plannedNode.probe_targets?.some(
    (t) => t.kind === "tcp" && t.carrier === "其他" && t.region === "新加坡",
  ),
  `服务端要列出这台机器的探测项（含自定义目标），实际 ${JSON.stringify(plannedNode.probe_targets)}`,
);
assert(
  plannedNode.probe_targets.some(
    (t) => t.kind === "route" && t.carrier === "电信" && t.family === "ipv4" && t.region === "上海",
  ),
  "回程项也要在探测计划里",
);
// 卡片里的延迟块照参考图的形状来：一家运营商一块，上面一行「延迟 / 丢包」的文字、下面两条
// 并排的小色带各 10 格（列表视图仍是行内一家一行、20 格）。以前卡片是一格一家、延迟与丢包
// 各占一行数值加一条色带（三格 174px），窄卡片还要靠容器查询换布局。
{
  const bands = card.querySelectorAll(".latency-bands .net-band");
  assert.equal(bands.length, 3, "延迟一家运营商一块");
  for (const band of bands) {
    assert(band.querySelector(".band-head .carrier-name"), "每块要给运营商");
    const metrics = [...band.querySelectorAll(".band-head .band-metric")];
    assert.equal(metrics.length, 2, "每块上面要给「延迟 / 丢包」两项");
    assert(/^延迟/.test(metrics[0].textContent), "第一项是延迟");
    assert(/^\d+ ms|未检测|超时/.test(metrics[0].querySelector("strong").textContent), "延迟要给数值或说明");
    assert(/^丢包/.test(metrics[1].textContent), "第二项是丢包");
    assert(/%|—/.test(metrics[1].querySelector("strong").textContent), "丢包要给百分比");
    const strips = [...band.querySelectorAll(".band-bars .quality-bars")];
    assert.equal(strips.length, 2, "下面要并排两条色带（延迟 / 丢包）");
    for (const strip of strips)
      assert.equal(strip.querySelectorAll(".quality-bar").length, 10, "每条色带 10 格");
    assert.equal(band.dataset.focusKind, "latency", "延迟块要带聚焦信息");
    assert(band.dataset.focusCarrier, "延迟块要带运营商");
    assert(band.getAttribute("title"), "延迟块要有各自的探测时间提示");
  }
  assert(!/\.latency-label|\.latency-grid/.test(styles), "方形延迟格那套样式已经撤掉");
  assert(!/@container node-card/.test(styles), "卡片不再按自身宽度换布局");
  // 两条色带必须落在上面那一列的正下方：头部与色带共用同一套三列栅格，色带各自认领第 2 / 3 列。
  // 少了 grid-column，第一条色带会掉进 30px 的运营商槽里（真浏览器里两条一长一短、和文字错开）。
  const head = styles.match(/\.net-band \.band-head,\s*\n\.net-band \.band-bars \{([^}]*)\}/);
  assert(
    head && /grid-template-columns: 30px minmax\(0, 1fr\) minmax\(0, 1fr\)/.test(head[1]),
    "延迟块的文字与色带要共用同一套三列栅格",
  );
  assert(
    /\.net-band \.band-bars \.quality-bars \{[^}]*grid-column: 2/.test(styles),
    "延迟色带要落在第 2 列（延迟数值正下方）",
  );
  assert(
    /\.net-band \.band-bars \.loss-bars \{[^}]*grid-column: 3/.test(styles),
    "丢包色带要落在第 3 列（丢包数值正下方）",
  );
}
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
    metrics: { counter_id: "ui-smoke", cpu: 1, memory_used: 1, memory_total: 2, disk_used: 1, disk_total: 2, net_in: 0, net_out: 0, rate_in: 0, rate_out: 0, uptime: 60, os: "smoke", distro: "Debian 13", arch: "amd64", cpu_cores: 4, version: "smoke", ip_families: ["ipv4"] },
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
  // 系统标签显示 Agent 报的发行版（读 /etc/os-release），不再只有笼统的 linux。
  assert.equal(
    single.querySelector(".os-tag").textContent,
    "Debian 13",
    "系统标签要显示发行版与版本",
  );
  assert.equal(single.querySelector(".arch-tag").textContent, "amd64", "架构标签照旧在发行版下面");
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
// 格子自己带聚焦信息：延迟块 → 该运营商延迟，回程胶囊 → 该运营商回程
const latencyCell = findCard().querySelector(".latency-bands .net-band");
assert.equal(latencyCell.dataset.focusKind, "latency", "延迟块要带上聚焦类型");
assert(latencyCell.dataset.focusCarrier, "延迟块要带上运营商");
const routeCell = findCard().querySelector(".route-grid .route-label");
assert.equal(routeCell.dataset.focusKind, "route", "回程胶囊要带上聚焦类型");
assert(routeCell.dataset.focusCarrier, "回程胶囊要带上运营商");
// 紧凑排版（用户点名「卡片太长了」，照参考图压）：实时速率 + 到期 + 价格收进一条信息带，
// 不再有独立的两块速率方块与卡底计费页脚；头部也只留名字 + 系统标签，地区 / 分组与地址族
// 开关落到第二行（否则窄卡片里的名字会被挤成三行）。
{
  const compact = findCard();
  const band = compact.querySelector(".card-stats");
  assert(band, "卡片要有一条实时速率 + 到期 + 价格的信息带");
  assert.equal(band.querySelectorAll(".speeds .speed-line").length, 2, "信息带里两个方向各一行速率");
  // 顺序：上行在上、下行在下（用户点名「上下行换下位置，上行在上面」）。两个视图共用这份 HTML。
  const speedLines = [...band.querySelectorAll(".speeds .speed-line")];
  assert(
    speedLines[0].title === "实时上传" && speedLines[1].title === "实时下载",
    `速率要上行在上、下行在下，实际「${speedLines.map((l) => l.title).join(" / ")}」`,
  );
  const pill = band.querySelector(".expiry-pill");
  assert(
    pill && /剩余 \d+ 天|长期|未设到期|已过期/.test(pill.textContent),
    `信息带要有到期胶囊，实际「${pill?.textContent}」`,
  );
  assert(band.querySelector(".price-label"), "信息带要有价格");
  // 中列是这张机器开机以来的网卡计数（↑ / ↓ 各一张，顺序跟速率一致：上行在上），
  // 照参考图补上「单独的上下行流量」。取值来自公开接口，所以按接口的值断言，
  // 顺带证明取的是 metrics.net_in/out。
  const totals = [...band.querySelectorAll(".stat-totals .stat-item")];
  assert.equal(totals.length, 2, "信息带中列要给上下行两张累计流量");
  assert(
    totals[0].textContent.trim() === `累计 ${bytes(plannedNode.metrics.net_out)}` &&
      totals[1].textContent.trim() === `累计 ${bytes(plannedNode.metrics.net_in)}`,
    `累计要按网卡计数分别给上下行，实际「${totals.map((t) => t.textContent).join(" / ")}」`,
  );
  assert.equal(compact.querySelectorAll(".speed-box").length, 0, "卡片里不再有宽大的速率方块");
  assert.equal(compact.querySelectorAll(".card-bottom").length, 0, "卡片底部不再有独立页脚");
  const sub = compact.querySelector(".node-sub");
  assert(sub?.querySelector("p"), "地区与分组在头部第二行");
  assert(sub.querySelector("[data-card-family]"), "地址族开关也在第二行");
  assert(
    /\.card-stats \.speeds \{[^}]*flex-direction: column/.test(styles) &&
      /\.stat-facts \{[^}]*align-items: flex-end/.test(styles),
    "信息带里速率靠左竖排、计费靠右竖排",
  );
  assert(/\.net-band \{[^}]*flex-direction: column/.test(styles), "卡片里的延迟块要竖排文字 + 色带");
  // 信息带的字号只有两档：数值 12px（速率与累计）、说明文字 10px（单位、累计二字、价格）。
  // 以前是 13 / 11 / 10 / 11 四种混在一行，相邻两列的基线看着就对不上。
  assert(
    /\.speed-line strong \{[^}]*font-size: 12px/.test(styles) &&
      /\.card-stats \.stat-item b \{[^}]*font-size: 12px/.test(styles),
    "信息带里的数值（速率与累计）要同一档字号——速率的 12px 写在全局 .speed-line strong 上，列表也一并跟上",
  );
  assert(
    /\.card-stats \.unit,\s*\n\.card-stats \.speed-line \.unit-none,\s*\n\.card-stats \.stat-item,\s*\n\.card-stats \.price-label \{[^}]*font-size: 10px[^}]*color: var\(--muted\)/.test(styles),
    "信息带里的说明文字（单位、累计、价格）要同一档字号与颜色",
  );
  assert(
    [...band.querySelectorAll(".stat-item .unit")].length === 2,
    "累计流量的单位也要与速率的「/s」同样弱化（10px 灰字，不是跟着数字加粗）",
  );
  // 回程三格等宽（用户点名「要这种等比例的」），每格「彩色圆点 + 回程值」一行读完——
  // 用户从三版对比里挑的方案③：圆点颜色代表运营商（电信蓝 / 联通红 / 移动绿），
  // 格子里不写运营商名。名字不能真丢：aria-label / title / data-focus-carrier 里必须有。
  const chip = compact.querySelector(".route-label");
  assert.equal(chip.children[0].tagName, "SPAN", "回程格第一项要是代表运营商的圆点");
  assert(
    /carrier-dot ct|carrier-dot cu|carrier-dot cm/.test(chip.children[0].className),
    `回程圆点要带运营商配色类，实际「${chip.children[0].className}」`,
  );
  assert(
    chip.dataset.focusCarrier && chip.getAttribute("aria-label")?.includes(chip.dataset.focusCarrier),
    "格子里不写运营商名，就得在 aria-label / data-focus-carrier 里留着（筛选与详情按它走）",
  );
  assert(
    /\.route-label \{[^}]*flex-wrap: wrap/.test(styles) &&
      /\.route-label \{[^}]*align-items: center/.test(styles) &&
      // 圆点不是文字，没有基线可言；值与圆点在同一行居中（用户：能居中还是居中）
      !/\.route-label \{[^}]*align-items: baseline/.test(styles) &&
      /\.route-label \{[^}]*justify-content: center/.test(styles),
    "回程格是圆点 + 值一行居中；更长的值整段落到第二行（不断在箭头上）",
  );
  assert(
    /\.route-grid \{[^}]*grid-template-columns: repeat\(3, minmax\(0, 1fr\)\)/.test(styles),
    "回程三格要等宽（不按内容定宽）",
  );
  assert(
    /\.carrier-dot \{[^}]*width: 7px[^}]*height: 7px/s.test(styles) &&
      /\.carrier-dot\.ct \{[^}]*var\(--accent\)/.test(styles) &&
      /\.carrier-dot\.cu \{[^}]*var\(--red\)/.test(styles) &&
      /\.carrier-dot\.cm \{[^}]*var\(--green\)/.test(styles) &&
      /\.route-label strong \{[^}]*font-size: 11px/.test(styles),
    "回程圆点 7px、按运营商配色（电信蓝/联通红/移动绿）；值 11px —— 12px 时多骨干值装不下一行",
  );
  // 方向徽章 16px：原来是 20px，比 12px 的文字大一圈、比文字行高还高，看着和后面的数字不是一档。
  assert(
    /\.speed-icon \{[^}]*width: 16px[^}]*height: 16px/s.test(styles),
    "方向徽章要 16px（落在 12px 文字的行高之内）",
  );
  assert(!/\.card-bottom|\.speed-box/.test(styles), "旧的两块（速率方块 / 卡底页脚）样式已清掉");
}
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
assert.equal(listRow.querySelectorAll(".net-cell .net-line").length, 3, "延迟三行（这台机器只挂了三网）");
assert.equal(listRow.querySelectorAll(".route-cell .route-line").length, 3, "回程三行");
for (const line of listRow.querySelectorAll(".net-line")) {
  assert(line.dataset.focusKind === "latency" || line.dataset.focusKind === "route", "行内文本要带聚焦信息");
  assert(line.dataset.focusCarrier, "行内文本要带运营商");
  assert(line.getAttribute("title"), "行内文本要有各自的探测时间提示");
}
const listSubs = [...listRow.querySelectorAll(".metrics-grid .metric small")].map((s) => s.textContent);
assert.equal(listSubs.length, 4, "四个指标各留一行副标");
// CPU 那格的副标是核心数（Agent 报的 runtime.NumCPU），不再写「系统计算负载」。
assert.equal(listSubs[0], "4 核", `CPU 副标要是核心数，实际是「${listSubs[0]}」`);
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
  /\.node-grid\.node-list \.speeds \{[^}]*flex-direction: column/.test(styles),
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
  /class="os-tag">\$\{os\}/.test(appSource) && /osLabel\(n\)/.test(appSource),
  "系统标签要走 osLabel（优先发行版，退回 GOOS）",
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
// 长期（按量付费）与免费机器：到期日填到十年以外要写「长期」而不是「剩余 72965 天」，
// 0 价且周期写着「免费」要写「免费」而不是「未设置价格」。
const longName = "长期免费验收 " + Date.now();
const madeLong = await fetch(base + "/api/v1/admin/nodes", {
  method: "POST",
  headers: { ...adminHeaders, "Content-Type": "application/json" },
  body: JSON.stringify({
    name: longName,
    region: "香港",
    public: true,
    currency: "CNY",
    price: 0,
    price_period: "免费",
    billing_at: "2026-09-01T00:00:00Z",
    traffic_mode: "max",
    expires_at: "2226-07-04T00:00:00Z",
  }),
});
// 正文只读一次：这个响应下面还要用它的 id，先 clone 再读副本会让原响应在这里被消耗掉。
const longNode = await madeLong.json();
assert.equal(
  madeLong.status,
  200,
  "creating a long-term free node must succeed: " + JSON.stringify(longNode),
);
await new Promise((resolve) => setTimeout(resolve, 11500));
publicSearch.value = longName;
publicSearch.dispatchEvent(new publicDOM.window.Event("input"));
await until(
  () => publicDOM.window.document.querySelector("#nodes .node-card"),
  "long-term node card",
);
{
  const long = publicDOM.window.document.querySelector("#nodes .node-card");
  const pill = long.querySelector(".expiry-pill");
  assert.equal(pill.textContent, "长期", "长期机器写「长期」，不写天数");
  assert(/\d{4}-\d{2}-\d{2}/.test(pill.getAttribute("title") || ""), "真实到期日放进 title");
  assert.equal(
    long.querySelector(".price-label").textContent,
    "免费",
    "0 价且周期写着免费就是「免费」",
  );
  // 这台机器没有绑定任何探测任务（按设计不挂任务，例如国际线路的机器）：探测段整段不画——
  // 不摆空格子、不摆灰色空条，只留一行说明。以前固定画满四组延迟 + 三组回程，
  // 整块是「未检测 / 未知 + 160 个灰格」的骨架，看着像没加载完。
  assert.equal(long.querySelectorAll(".route-grid").length, 0, "没任务的卡片不画延迟 / 回程格子");
  assert.equal(long.querySelectorAll(".route-heading").length, 0, "没有分组就不该留着分组标题");
  assert.equal(long.querySelectorAll(".quality-bar").length, 0, "没任务的卡片不画灰色空条");
  assert.equal(long.querySelectorAll(".route-label, .net-band, .net-line").length, 0, "没任务就不给格子与延迟块");
  assert.equal(long.querySelector(".probe-none")?.textContent, "仅主机指标", "没任务的卡片给一行说明");
  assert(!long.textContent.includes("未检测"), "没任务就不该出现「未检测」");
  // 卡片高度靠「栅格拉伸 + 计费行贴底」保持齐平（说明行吃掉剩余空白居中），不靠占位元素。
  assert(
    /\.probe-none \{[^}]*flex: 1 1 auto/.test(styles) &&
      /\.node-grid\.node-list \.probe-none \{[^}]*justify-content: flex-start/.test(styles),
    "说明行在卡片里居中吃掉空白、在列表里左对齐",
  );
  // 行与行也要齐平：卡片视图 grid-auto-rows: 1fr，单列与列表视图改回 auto。
  assert(
    /\.node-grid \{[^}]*grid-auto-rows: 1fr/.test(styles),
    "卡片视图要把行高拉齐（卡片尺寸一致）",
  );
  assert(
    /\.node-grid\.node-list \{[^}]*grid-auto-rows: auto/.test(styles) &&
      /grid-template-columns: 1fr;\s*\/\* 单列时不再拉齐行高/.test(styles),
    "列表与单列视图不该拉平行高",
  );
  // 详情页同一处也不该给空图表或「请在后台创建并绑定任务」。
  publicDOM.window.document.querySelector(`[data-detail="${longNode.id}"]`).click();
  await until(
    () => publicDOM.window.document.querySelector("#dialog .probe-none"),
    "节点详情：没有任务时给一行说明",
  );
  assert(
    !publicDOM.window.document.querySelector("#dialog #network-overview"),
    "没任务的详情页不给空图表",
  );
  assert(
    !publicDOM.window.document.querySelector("#dialog #route-details"),
    "没任务的详情页不给「请在后台创建并绑定任务」占位",
  );
  publicDOM.window.document.querySelector("#dialog").close();
}
// 达量降速（线路按「窗口内平均带宽」降速，德国商家常见：近 24 小时平均超过 100 Mbit/s 降速）：
// 只有真填了降速阈值的线路才多出这一行，没填的机器一行都不加。平均值来自 Agent 上报的网卡
// 计数增量（存储按整点桶累加），所以这里造一台机器并连发两次上报（间隔要过 3 秒去重窗口）。
const throttleName = "降速验收 " + Date.now();
const madeThrottle = await fetch(base + "/api/v1/admin/nodes", {
  method: "POST",
  headers: { ...adminHeaders, "Content-Type": "application/json" },
  body: JSON.stringify({
    name: throttleName,
    region: "德国",
    group: "DE",
    public: true,
    billing_at: "2026-09-01T00:00:00Z",
    traffic_mode: "sum",
    line_mbps: 1000,
    throttle_mbps: 1,
    throttle_hours: 1,
    alerts: { throttle: true },
  }),
});
assert.equal(
  madeThrottle.status,
  200,
  "带降速阈值的节点要能创建: " + (await madeThrottle.clone().text()),
);
const throttleNode = await madeThrottle.clone().json();
const throttleReport = (metrics) =>
  fetch(base + "/api/v1/agent/report", {
    method: "POST",
    headers: { Authorization: "Bearer " + throttleNode.token, "Content-Type": "application/json" },
    body: JSON.stringify({ protocol: 1, metrics }),
  });
const throttleMetrics = {
  counter_id: "ui-smoke-throttle", cpu: 1, memory_used: 1, memory_total: 2, disk_used: 1, disk_total: 2,
  net_in: 1000, net_out: 0, rate_in: 0, rate_out: 0, uptime: 60, os: "smoke", distro: "Debian 13", arch: "amd64",
  cpu_cores: 2, version: "smoke", ip_families: ["ipv4"],
};
assert.equal((await throttleReport(throttleMetrics)).status, 204, "第一次上报建立流量基线");
await new Promise((resolve) => setTimeout(resolve, 3500));
const throttleAfter = { ...throttleMetrics, net_in: throttleMetrics.net_in + 675000000, uptime: 3660 };
assert.equal((await throttleReport(throttleAfter)).status, 204, "第二次上报带上网卡计数增量");
// 服务端先把窗口平均带宽算出来（公开接口就能读），再用它断言界面：DOM 滞后一拍，
// 直接断言界面会读到「0 bit/s」那一帧。
const throttleView = async () =>
  (await (await fetch(base + "/api/v1/nodes")).json()).find((x) => x.id === throttleNode.id);
await until(async () => ((await throttleView())?.avg_bps || 0) > 0, "服务端算出窗口平均带宽");
{
  const view = await throttleView();
  assert.equal(view.window_hours, 1, "窗口按节点配置的 1 小时");
  assert.equal(view.covered_hours, 1, "覆盖小时数不能超过窗口");
  assert(view.avg_bps > 1.4e6, `6.75 亿字节在 1 小时窗口里的均值至少 1.5 Mbit/s，实际 ${view.avg_bps} bit/s`);
  assert.equal(view.throttled, true, "均值超过阈值要标 throttled");
}
publicSearch.value = throttleName;
publicSearch.dispatchEvent(new publicDOM.window.Event("input"));
const throttleCard = () =>
  [...publicDOM.window.document.querySelectorAll(".node-card")].find((c) =>
    c.textContent.includes(throttleName),
  );
await until(
  () => /近 1 小时均值 [\d.]+ Mbit\/s/.test(throttleCard()?.querySelector(".throttle-note")?.textContent || ""),
  "带阈值的卡片要出现降速那一行（含窗口均值）",
);
{
  const note = throttleCard().querySelector(".throttle-note");
  assert(
    /近 1 小时均值 [\d.]+ Mbit\/s · 阈值 1 Mbit\/s/.test(note.textContent),
    `降速行要写窗口均值与阈值，实际「${note.textContent}」`,
  );
  assert(note.classList.contains("over"), "平均值远超阈值时这一行要标红（.over）");
  // 同一张卡片上的累计流量：6.75 亿字节要按方向写成 675 MB / 0 B（非零值把单位换算也一并盯住）。
  // 卡片是打补丁更新的，这一帧可能还停在上一份数据上（作者在同一段落里也是这样处理
  // avg_bps 的），所以等它收敛再断言，别把「还没刷新」读成「没更新」。
  const totals = () =>
    [...throttleCard().querySelectorAll(".card-stats .stat-totals .stat-item")].map((t) =>
      t.textContent.trim(),
    );
  const wantTotals = [`累计 ${bytes(throttleAfter.net_out)}`, `累计 ${bytes(throttleAfter.net_in)}`];
  await until(
    () => JSON.stringify(totals()) === JSON.stringify(wantTotals),
    `累计要给上下行两张（上行在上）、按网卡计数换算单位，实际「${totals().join(" / ")}」`,
  );
}
// 详情页同样只在配了阈值时多给一格，并且把线路带宽写出来。
throttleCard().querySelector("[data-detail]").click();
await until(
  () => publicDOM.window.document.querySelector("#dialog .detail-wide"),
  "详情页的降速格",
);
{
  const tile = publicDOM.window.document.querySelector("#dialog .detail-wide");
  assert(tile.textContent.includes("线路带宽 1000 Mbit/s"), `详情格要写线路带宽，实际「${tile.textContent}」`);
  assert(/近 1 小时平均带宽\s*[\d.]+ Mbit\/s/.test(tile.textContent), `详情格要写窗口平均带宽，实际「${tile.textContent}」`);
  assert(tile.textContent.includes("已达阈值"), "超过阈值时详情格要写明");
  assert(
    publicDOM.window.document.querySelector("#dialog .detail-stats")?.textContent.includes("Debian 13"),
    "详情页要写出 Agent 报的发行版",
  );
  assert(/\.detail-stats > \.detail-wide \{[^}]*grid-column: 1 \/ -1/.test(styles), "详情格要占满整行");
  publicDOM.window.document.querySelector("#dialog").close();
}
publicSearch.value = "";
publicSearch.dispatchEvent(new publicDOM.window.Event("input"));
// 没配阈值的机器（长期免费那台，本轮创建且仍在）不许出现降速行，也不许留一个空行占位。
{
  const plain = [...publicDOM.window.document.querySelectorAll(".node-card")].find((c) =>
    c.textContent.includes(longName),
  );
  assert(plain, "搜索复位后要能找回没配阈值的节点卡片");
  assert(!plain.querySelector(".throttle-note"), "没配阈值的机器不显示降速行");
}
// 按周期额度降速（DMIT T1 这类「1000 GB 取高值 @ 4 Gbps，用满后不计量但限速到 50/100
// Mbit/s」）：这一行不写窗口均值，而是写「用满多少额度 · 限速多少」，用满后标红。
const quotaName = "额度降速验收 " + Date.now();
const madeQuota = await fetch(base + "/api/v1/admin/nodes", {
  method: "POST",
  headers: { ...adminHeaders, "Content-Type": "application/json" },
  body: JSON.stringify({
    name: quotaName,
    region: "香港",
    group: "HK",
    public: true,
    billing_at: "2026-09-01T00:00:00Z",
    traffic_limit: 1000000,
    traffic_mode: "max",
    line_mbps: 4000,
    throttle_mbps: 50,
    throttle_on: "quota",
    alerts: { throttle: true },
  }),
});
assert.equal(
  madeQuota.status,
  200,
  "带额度降速规则的节点要能创建: " + (await madeQuota.clone().text()),
);
const quotaNode = await madeQuota.clone().json();
// 创建接口只回 id/token/安装命令，要确认触发条件真的落库得读一次节点列表。
{
  const stored = (await (await fetch(base + "/api/v1/admin/nodes", { headers: adminHeaders })).json()).find(
    (x) => x.id === quotaNode.id,
  );
  assert.equal(stored?.throttle_on, "quota", `创建接口要落库 throttle_on，实际 ${JSON.stringify(stored?.throttle_on)}`);
}
{
  const quotaReport = (metrics) =>
    fetch(base + "/api/v1/agent/report", {
      method: "POST",
      headers: { Authorization: "Bearer " + quotaNode.token, "Content-Type": "application/json" },
      body: JSON.stringify({ protocol: 1, metrics }),
    });
  const base1 = { ...throttleMetrics, counter_id: "ui-smoke-quota", net_in: 1000 };
  assert.equal((await quotaReport(base1)).status, 204, "第一次上报建立流量基线");
  await new Promise((resolve) => setTimeout(resolve, 3500));
  assert.equal(
    (await quotaReport({ ...base1, net_in: base1.net_in + 2000000, uptime: 3660 })).status,
    204,
    "第二次上报把本周期用量顶过额度",
  );
}
const quotaView = async () =>
  (await (await fetch(base + "/api/v1/nodes")).json()).find((x) => x.id === quotaNode.id);
await until(async () => (await quotaView())?.throttled === true, "用满额度的节点要标 throttled");
{
  const view = await quotaView();
  assert.equal(view.throttle_on, "quota", "公开接口要给出降速触发条件");
  assert(view.avg_bps < 1e6, `按额度降速的线路不该因为均值而降速，实际均值 ${view.avg_bps} bit/s`);
}
publicSearch.value = quotaName;
publicSearch.dispatchEvent(new publicDOM.window.Event("input"));
const quotaCard = () =>
  [...publicDOM.window.document.querySelectorAll(".node-card")].find((c) =>
    c.textContent.includes(quotaName),
  );
await until(
  () => /已用满额度 · 限速 50 Mbit\/s/.test(quotaCard()?.querySelector(".throttle-note")?.textContent || ""),
  "额度降速的卡片要出现「已用满额度 · 限速 50 Mbit/s」那一行",
);
{
  const note = quotaCard().querySelector(".throttle-note");
  assert(
    /^已用满额度 · 限速 50 Mbit\/s$/.test(note.textContent),
    `额度降速行要写「已用满额度 · 限速 N Mbit/s」，实际「${note.textContent}」`,
  );
  assert(!/小时均值/.test(note.textContent), "额度降速的线路不该写窗口均值");
  assert(note.classList.contains("over"), "用满额度时这一行要标红（.over）");
}
quotaCard().querySelector("[data-detail]").click();
await until(
  () => /用满额度后的限速/.test(publicDOM.window.document.querySelector("#dialog .detail-wide")?.textContent || ""),
  "额度降速的详情格",
);
{
  const tile = publicDOM.window.document.querySelector("#dialog .detail-wide");
  assert(tile.textContent.includes("用满额度后的限速"), `详情格要写清是额度口径，实际「${tile.textContent}」`);
  assert(tile.textContent.includes("50 Mbit/s"), "详情格要写用满后的速率");
  assert(tile.textContent.includes("峰值 4000 Mbit/s"), "详情格要写突发峰值");
  assert(tile.textContent.includes("已达额度"), "用满时详情格要写明");
  publicDOM.window.document.querySelector("#dialog").close();
}
publicSearch.value = "";
publicSearch.dispatchEvent(new publicDOM.window.Event("input"));
await fetch(base + "/api/v1/admin/nodes/" + quotaNode.id, { method: "DELETE", headers: adminHeaders });
await fetch(base + "/api/v1/admin/nodes/" + throttleNode.id, { method: "DELETE", headers: adminHeaders });
// ===== 解锁检测：公开卡片徽标 + 分类浮层 + 详情块（真数据） =====
// 租约 → 上报 24 项结论 → 服务端算分 → 公开页渲染。分数固定按 24 项算：流媒体 7/9、
// AI 3/3、交易所 3/3、其他 6/9，合计 19/24（region_only 的 5 项只显示地区 / 币种文字）。
const unlockVerdicts = [
  { service: "netflix", status: "originals_only", region: "JP" },
  { service: "disneyplus", status: "yes", region: "JP" },
  { service: "youtube_premium", status: "yes" },
  { service: "prime_video", status: "no" },
  { service: "tvb_anywhere", status: "no" },
  { service: "iqiyi", status: "yes" },
  { service: "dazn", status: "yes" },
  { service: "netflix_cdn", status: "yes" },
  { service: "youtube_cdn", status: "yes" },
  { service: "openai", status: "yes" },
  { service: "gemini", status: "yes" },
  { service: "claude", status: "yes" },
  { service: "bybit", status: "yes" },
  { service: "binance", status: "yes" },
  { service: "okx", status: "yes" },
  { service: "bing", status: "yes", region: "Japan" },
  { service: "apple", status: "yes", region: "JP" },
  { service: "google_play", status: "yes", region: "Japan" },
  { service: "google_search", status: "yes" },
  { service: "wikipedia", status: "yes" },
  { service: "reddit", status: "banned" },
  { service: "steam", status: "yes", region: "JPY" },
  { service: "onetrust", status: "banned", region: "US" },
  { service: "sdggge", status: "failed" },
];
const unlockLease = await (
  await fetch(base + "/api/v1/agent/jobs", { headers: { Authorization: "Bearer " + unlockNode.token } })
).json();
const unlockJob = unlockLease.find((j) => j.task.kind === "unlock");
assert(unlockJob, "带 unlock 能力的节点要能领到解锁作业: " + JSON.stringify(unlockLease.map((j) => j.task.kind)));
const unlockPosted = await fetch(base + "/api/v1/agent/results", {
  method: "POST",
  headers: { Authorization: "Bearer " + unlockNode.token, "Content-Type": "application/json" },
  body: JSON.stringify({ job_id: unlockJob.id, status: "ok", sent: 0, received: 0, route: "未知", hops: [], unlocks: unlockVerdicts }),
});
assert.equal(unlockPosted.status, 204, "24 项解锁结论要被接受: " + (await unlockPosted.clone().text()));
// 服务端先算出来（公开接口就能读），DOM 会滞后一拍，先等接口再断界面。
const unlockView = async () =>
  (await (await fetch(base + "/api/v1/nodes")).json()).find((x) => x.id === unlockNode.id);
await until(async () => (await unlockView())?.unlock?.score === 19, "服务端解锁分数");
{
  const view = await unlockView();
  assert.equal(view.unlock.total, 24, "分母固定 24，不按上报条数缩水");
  assert.equal(view.unlock.services.length, 24, "24 项结论都要落库");
  assert(view.unlock.tested_at > 0, "结论要带检测时间");
  assert(view.probe_targets?.some((t) => t.kind === "unlock"), "挂了解锁任务的机器要在 probe_targets 里带上解锁项");
  assert.equal(view.unlock.score, 19, "已解锁 7+3+3+6=19");
}
// 公开页每 10 秒才全量刷一次，等过一轮再让它重画（数值类断言不能跑在刷新前那一帧上）。
await new Promise((resolve) => setTimeout(resolve, 11500));
publicSearch.value = unlockNodeName;
publicSearch.dispatchEvent(new publicDOM.window.Event("input"));
const unlockCard = () =>
  [...publicDOM.window.document.querySelectorAll(".node-card")].find((c) => c.textContent.includes(unlockNodeName));
await until(() => unlockCard()?.querySelector(".unlock-badge"), "公开卡片的解锁徽标");
{
  const badge = unlockCard().querySelector(".unlock-badge");
  assert.equal(badge.querySelector(".unlock-badge-score").textContent, "19/24", "徽标要给分数");
  assert(badge.querySelector(".unlock-lock"), "徽标要有锁图标");
  // 徽标挂在 .card-top 的 .card-actions 里、排在系统标签（.os-box）前面。
  const actions = badge.closest(".card-actions");
  assert(actions && actions.closest(".card-top"), "徽标要挂在 .card-top 的 .card-actions 里");
  assert(actions.querySelector(".os-box"), "系统标签与徽标同一行");
  assert.equal(actions.firstElementChild, badge, "徽标在系统标签前面");
  // 点击展开分类浮层（手机上就是这条路）。
  badge.click();
  const pop = publicDOM.window.document.querySelector("#unlock-pop");
  assert(pop && !pop.hidden, "点徽标要展开解锁分类浮层");
  assert.equal(badge.getAttribute("aria-expanded"), "true", "展开时 aria-expanded 为 true");
  // 浮层挂在 body 上：卡片 overflow:hidden，塞在卡内的浮层会被裁掉。
  assert.equal(pop.closest(".node-card"), null, "浮层不能塞在卡片里（会被 overflow 裁掉）");
  assert.equal(pop.parentElement, publicDOM.window.document.body, "浮层是挂在 body 上的单例");
  const popText = pop.textContent;
  assert(popText.includes("解锁检测"), "浮层要有标题行");
  assert(popText.includes("19/24"), "浮层要有分数");
  assert(
    popText.includes("流媒体 7/9") && popText.includes("AI 3/3") && popText.includes("交易所 3/3") && popText.includes("其他 6/9"),
    "浮层要有分类统计: " + popText,
  );
  assert(popText.includes("Netflix") && popText.includes("仅自制剧"), "服务列表要给名称与状态: " + popText);
  assert(popText.includes("IP 被封禁") && popText.includes("检测失败") && popText.includes("未解锁"), "状态词表要齐全: " + popText);
  // region_only 的 5 项只给地区 / 币种文字，不画锁、也不写「未解锁」。
  assert.equal(pop.querySelectorAll(".unlock-state.region").length, 5, "region_only 的 5 项走地区文字");
  assert.equal(pop.querySelector(".unlock-state.region .unlock-lock"), null, "地区项不画锁");
  assert(popText.includes("Japan") && popText.includes("JPY"), "地区项要显示地区 / 币种文字: " + popText);
  // 再点一下收起。
  badge.click();
  assert(pop.hidden, "再点徽标要收起浮层");
  assert.equal(badge.getAttribute("aria-expanded"), "false", "收起时 aria-expanded 为 false");
}
// 详情弹窗里的「解锁检测」块：标题 + 分数 + 分类 + 服务列表 + 检测时间。
unlockCard().querySelector(".node-name").click();
await until(() => publicDOM.window.document.querySelector("#dialog .unlock-block"), "详情里的解锁块");
{
  const text = publicDOM.window.document.querySelector("#dialog .unlock-block").textContent;
  assert(text.includes("解锁检测") && text.includes("19/24"), "详情块要有标题与分数: " + text);
  assert(text.includes("流媒体 7/9") && text.includes("其他 6/9"), "详情块要有分类统计: " + text);
  assert(text.includes("Netflix") && text.includes("仅自制剧"), "详情块要有服务列表: " + text);
  assert(/检测于 \d{4}\/\d{1,2}\/\d{1,2}/.test(text), "详情块要有检测时间: " + text);
  publicDOM.window.document.querySelector("#dialog").close();
}
// 没有解锁数据的节点一个占位都不给（用户明确反感占位元素）。
publicSearch.value = longName;
publicSearch.dispatchEvent(new publicDOM.window.Event("input"));
await until(
  () => [...publicDOM.window.document.querySelectorAll(".node-card")].some((c) => c.textContent.includes(longName)),
  "long-term card for the no-unlock check",
);
{
  const plain = [...publicDOM.window.document.querySelectorAll(".node-card")].find((c) => c.textContent.includes(longName));
  assert.equal(plain.querySelector(".unlock-badge"), null, "没有解锁数据的节点不渲染徽标");
  assert.equal(plain.querySelector(".unlock-pop"), null, "也不渲染空浮层");
}
publicSearch.value = "";
publicSearch.dispatchEvent(new publicDOM.window.Event("input"));
// 样式文本断言：jsdom 不套用 styles.css，栅格 / 定位只能按样式表文本盯住。
assert(/\.unlock-badge \{/.test(styles), "徽标样式要落在 styles.css");
assert(/\.unlock-pop \{[\s\S]*?position: fixed/.test(styles), "浮层用 fixed 定位（卡片 overflow:hidden 会裁掉卡内浮层）");
assert(/\.unlock-state \.unlock-lock \{[^}]*color: var\(--green\)/.test(styles), "已解锁的锁是绿色");
assert(/\.unlock-block \.unlock-panel \{[^}]*font-size: 13px/.test(styles), "详情块的解锁面板字号独立");
publicDOM.window.close();
console.log(
  "PASS: login, node creation/edit, billing and alerts, search/filter/batch catalog, combined checks and dual stack, overseas custom targets, edit round-trip and explicit binding, notifications page, logout, public cards/search/IPv6, bandwidth throttle row, quota throttle row, unlock task type + card badge/popover/detail. DOM simulation; visual layout not covered.",
);
