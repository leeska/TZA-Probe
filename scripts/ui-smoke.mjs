// Integration checks against a disposable local Server; never target production.
// Install jsdom 26 and esbuild 0.25 into a temporary directory, then set
// TZA_UI_DEPS, TZA_TEST_URL, TZA_TEST_ADMIN_FILE, TZA_TEST_CREDENTIALS_FILE and TZA_TEST_THEMES as shown in docs/VERIFICATION.md.
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
const { bytes, gib } = await import(new URL("../web/format.js", import.meta.url).href);
const credentials = await readFile(process.env.TZA_TEST_CREDENTIALS_FILE, "utf8");
const username = credentials.match(/^后台用户名：(.+)$/m)?.[1];
const password = credentials.match(/^后台密码：(.+)$/m)?.[1];
assert(username && password, "需要一次性 Server 的 init 输出");
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
const rawStyles = await readFile(path.join(root, "web/styles.css"), "utf8");
// 样式表里的字号、圆角、间距、字体都写成设计令牌（var(--text-sm) 之类，见 styles.css 开头的 :root）。
// 下面按样式表文本断言的那些规则关心的是默认值，所以先把令牌换回 :root 里的默认值再断言：
// var(--space-3) → 12px，calc(14px * var(--radius-scale)) → 14px，没定义的组件令牌取 fallback。
const tokenDefaults = Object.fromEntries(
  [...rawStyles.slice(0, rawStyles.indexOf("font-family: var(--font-sans);")).matchAll(/^\s*(--[a-z0-9-]+):\s*([^;]+);/gm)].map(([, k, v]) => [k, v.replace(/\s+/g, " ").trim()]),
);
function resolveTokens(css) {
  const varCall = /var\((--[a-z0-9-]+)(?:,\s*((?:[^()]|\((?:[^()]|\([^()]*\))*\))*))?\)/g;
  for (let i = 0, prev = ""; i < 5 && prev !== css; i++) {
    prev = css;
    css = css
      .replace(varCall, (m, name, fallback) => (name in tokenDefaults ? tokenDefaults[name] : /^--(card|panel|tile)-/.test(name) && fallback ? fallback : m))
      .replace(/calc\((\d+(?:\.\d+)?px) \* 1\)/g, "$1");
  }
  return css;
}
const styles = resolveTokens(rawStyles);
const selectSource = await readFile(path.join(root, "web/ui-select.js"), "utf8");
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
  this.dispatchEvent(new this.ownerDocument.defaultView.Event("close"));
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
async function until(predicate, label, budget = 8000) {
  const deadline = Date.now() + budget;
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
assert(!$('#login [name="token"]') && !$("#login-mode"), "仅提供用户名密码登录");
set($("#login"), "username", username);
set($("#login"), "password", password);
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
set(form, "line_mbps", 1000);
set(form, "expires_at", "2027-09-18 03:04:05");
// 重置时间与到期时间都精确到秒：重置时间默认落在当月 1 日 00:00:00（UTC），
// 旧的「每月重置日」数字框与步进箭头整体取消。
assert.equal(form.elements.billing_day, undefined, "旧的每月重置日数字框应已删除");
assert.equal(form.elements.billing_at.type, "text");
assert.match(form.elements.billing_at.value, /^\d{4}-\d{2}-01 00:00:00$/, "重置时间默认值: " + form.elements.billing_at.value);
assert.match(form.elements.billing_at.placeholder, /\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}/, "输入框要给出带秒的格式示例");
assert.match(form.elements.expires_at.placeholder, /\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}/);
// 价格周期与货币固定成下拉：周期「周、月、季、半年、年、两年、三年」，
// 货币固定成常见几种。货币的 value 仍是 ISO 代码（卡片一直写 CNY / USD），中文名只做标签，
// 免得把既有节点的显示改成「人民币」。
assert.equal(form.elements.price_period.tagName, "SELECT", "价格周期要固定成选项");
assert.equal(form.elements.currency.tagName, "SELECT", "货币要固定成选项");
{
  const periods = [...form.elements.price_period.options].map((o) => o.value);
  for (const period of ["周", "月", "季", "半年", "年", "两年", "三年"])
    assert(periods.includes(period), `价格周期缺少 ${period}（共 ${periods.join("、")}）`);
  assert.equal(form.elements.price_period.value, "月", "新节点默认月付");
  const currencies = [...form.elements.currency.options].map((o) => o.value);
  for (const code of ["CNY", "USD", "EUR", "GBP", "JPY", "CAD", "HKD", "SGD"])
    assert(currencies.includes(code), `货币缺少 ${code}（共 ${currencies.join("、")}）`);
  assert(
    [...form.elements.currency.options].some(
      (o) => o.value === "USD" && o.textContent.includes("美元"),
    ),
    "货币选项要带上中文名（美元（USD））",
  );
  assert.equal(form.elements.currency.value, "CNY", "新节点默认人民币");
}
// 滚动不再误改数值：滚到数字框上时先失焦（jsdom 自身不会按滚轮改值，只验证这个约定）。
const quota = form.elements.quota;
quota.focus();
assert.equal(w.document.activeElement, quota);
quota.dispatchEvent(new w.Event("wheel", { bubbles: true }));
assert.notEqual(w.document.activeElement, quota, "滚轮经过数字框时必须失焦，避免滑动页面时误改数值");
// 提醒规则不在节点表单里改，而是在告警页按范围单独设置（见下方告警页那一段）。
assert.equal(form.elements.namedItem("offline"), null, "节点表单里不该再有提醒规则输入");
assert(/新节点自动套用/.test($("#node-form").textContent), "新节点表单要说明它会套用哪些提醒规则、在哪设置");
// 内置目录只有国内三网，而节点多半在海外：建议列表必须补上常用海外地区，且仍可自由填写。
// 2026-10-10 起地区控件是可检索的组合框（web/region-picker.js）：建议表在弹层里，真正的
// 表单字段仍然是那个输入框，所以目录外的值照样能手打进去，只是多了一份可搜的建议。
const regionPool = new Set(
  [...w.document.querySelectorAll("[data-combo] [data-region-option]")].map(
    (b) => b.dataset.regionOption,
  ),
);
for (const region of ["香港", "美国", "日本", "新加坡"])
  assert(regionPool.has(region), `节点地区建议列表缺少 ${region}（共 ${regionPool.size} 项）`);
assert(regionPool.size > 30, "建议列表不能只剩目录里的省份");
assert.equal(form.elements.region.tagName, "INPUT", "地区控件仍是输入框：目录外的值要能直接打");
assert.equal(
  w.document.querySelectorAll("#node-form [data-combo]").length,
  3,
  "节点表单的三个地区字段（所在地区 / 回程地区 / 延迟地区）都要可检索",
);
// 检索走的是 web/search.js 那一套：中文、全拼、拼音首字母、英文代号命中同一条建议。
const matchesFor = (name, query) => {
  const box = w.document
    .querySelector(`[data-combo] input[name="${name}"]`)
    .closest("[data-combo]");
  const input = box.querySelector(".combo-input");
  input.value = query;
  input.dispatchEvent(new w.Event("input", { bubbles: true }));
  return [...box.querySelectorAll("[data-region-option]")]
    .filter((b) => !b.hidden)
    .map((b) => b.dataset.regionOption);
};
const pinyinHit = matchesFor("region", "xg");
assert(pinyinHit.includes("香港"), `首字母 xg 要命中香港，实际 ${JSON.stringify(pinyinHit)}`);
// 全拼与中文必须同结果（首字母是子串匹配，像「墨西哥 moxige」里也含 xg，所以只要求命中不要求相等）。
assert.deepEqual(matchesFor("region", "xianggang"), matchesFor("region", "香港"), "全拼与中文必须同结果");
assert(matchesFor("region", "hongkong").includes("香港"), "英文代号 hongkong 要命中香港");
assert(matchesFor("region", "hk").includes("香港"), "代号 hk 要命中香港");
assert.equal(matchesFor("region", "火星地区").length, 0, "没有匹配的地区时一条建议都不给");
assert(
  matchesFor("region", "火星地区").length === 0 &&
    !w.document.querySelector("[data-combo] .combo-empty").hidden,
  "没有匹配时要给一行说明（并且不影响直接输入）",
);
// 点一条建议：写进输入框（表单字段）并关掉弹层。
const regionBox = w.document
  .querySelector('[data-combo] input[name="region"]')
  .closest("[data-combo]");
regionBox.querySelector('[data-region-option="日本"]').click();
assert.equal(form.elements.region.value, "日本", "点建议要把它写进表单字段");
assert(!regionBox.classList.contains("open"), "选完要关掉弹层");
set(form, "region", "香港");
submit(form);
await until(() => $("#agent-config"), "one-time agent configuration");
const nodeToken = JSON.parse($("#agent-config").textContent).token;
assert(nodeToken.length === 64);
assert($("#install-command").textContent === `curl -fsSL ${base}/install.sh | sh -s -- --endpoint ${base} --token ${nodeToken}`, "one-line install command must carry endpoint and key");
$(".close").click();
const adminHeaders = { Authorization: "Bearer " + token };
// 复古印刷 / 二次元不内置，是仓库 themes/ 下的官方主题包（发到 TZA-CC 市场）。
// 流水线把它们打成 zip（tools/themepack）放进 TZA_TEST_THEMES，这里像管理员上传一样装上，
// 后面的皮肤断言才有东西可测。没装之前，内置的是现代极简（默认）与液态玻璃。
{
  const before = await (await fetch(base + "/api/v1/admin/themes", { headers: adminHeaders })).json();
  assert.deepEqual(before.map((t) => t.id), ["shadcn"], "没装主题包时只有内置的现代极简");
  assert(process.env.TZA_TEST_THEMES, "需要 TZA_TEST_THEMES（tools/themepack 打出的官方主题包目录）");
  for (const id of ["press", "anime", "glass"]) {
    const zip = await readFile(path.join(process.env.TZA_TEST_THEMES, id + ".zip"));
    const r = await fetch(base + "/api/v1/admin/themes", { method: "POST", headers: { ...adminHeaders, "Content-Type": "application/zip" }, body: zip });
    assert.equal(r.status, 200, `装官方主题包 ${id}: ${await r.clone().text()}`);
    assert.equal((await r.json()).id, id);
  }
}
let list = await (
  await fetch(base + "/api/v1/admin/nodes", { headers: adminHeaders })
).json();
const created = list.find((n) => n.name === name);
assert(created && Array.isArray(created.alerts) && created.alerts.length === 0, "新节点默认不开提醒（规则列表为空）");
assert.equal(created.billing.traffic_limit, 2e12);
assert.equal(created.region, "香港", "地区输入框里的值要原样落库（控件换组合框不改取值）");
assert.equal(created.billing.currency, "CNY", "默认人民币");
assert.equal(created.billing.price_period, "月", "默认月付");
assert.equal(created.line.mbps, 1000, "线路带宽要落库（前台要显示它）");
assert.equal(created.billing.expires_at, "2027-09-18T03:04:05Z", "到期时间要带上输入框里的秒");
assert.match(created.billing.reset_at, /^\d{4}-\d{2}-01T00:00:00Z$/, "重置时间 = " + created.billing.reset_at);
assert.equal(created.display.route_region, "上海", "卡片回程地区落在 display.route_region");
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
    metrics: { counter_id: "ui-smoke", cpu: 1, memory_used: 2609623040, memory_total: 8333946880, disk_used: 12095188992, disk_total: 105467994112, net_in: 0, net_out: 0, rate_in: 0, rate_out: 0, uptime: 60, os: "smoke", distro: "Debian 13", arch: "amd64", cpu_cores: 4, version: "smoke", ip_families: ["ipv4", "ipv6"] },
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
assert.match(form.elements.billing_at.value, /^\d{4}-\d{2}-01 00:00:00$/, "重置时间要能原样回填");
assert.equal(form.elements.expires_at.value, "2027-09-18 03:04:05", "到期时间回填必须保留秒");
assert.equal(form.elements.latency_region.value, "", "latency region defaults to empty (falls back to the representative region)");
set(form, "latency_region", "上海");
submit(form);
await until(async () => {
  list = await (await fetch(base + "/api/v1/admin/nodes", { headers: adminHeaders })).json();
  return list.find((n) => n.id === created.id)?.display.latency_region === "上海";
}, "latency region saved and read back");
// 编辑保存（PUT 204，无正文）之后要提示「节点已保存」，而不是读空正文抛错。
await until(() => $("#toast").textContent === "节点已保存", "edit save toast: " + $("#toast").textContent);
// 卡片地区只认内置目录或已有任务用到的地区：对不上的值保存时直接拒绝，而不是让卡片悄悄空着。
{
  const resp = await fetch(base + "/api/v1/admin/nodes/" + created.id, {
    method: "PATCH",
    headers: { ...adminHeaders, "Content-Type": "application/json" },
    body: JSON.stringify({ display: { route_region: "上海市" } }),
  });
  assert.equal(resp.status, 400, "对不上任何目标的卡片地区要被拒绝");
  assert.match((await resp.json()).error, /上海市/, "错误要指出是哪个地区");
}
// 回程运营商筛选：表单里有三网勾选，取消一项后落库为剩下的两项。
await until(
  () => {
    $(`[data-edit="${created.id}"]`).click();
    form = $("#node-form");
    const ok = form?.elements.namedItem("route_carrier:移动") && form.elements.latency_region.value === "上海";
    if (!ok) $(".close")?.click();
    return ok;
  },
  "node form with route carrier checkboxes",
);
assert(!form.elements.namedItem("route_carrier:其他"), "回程只有三网，不给「其他」");
form.elements.namedItem("route_carrier:移动").checked = false;
submit(form);
await until(async () => {
  list = await (await fetch(base + "/api/v1/admin/nodes", { headers: adminHeaders })).json();
  return JSON.stringify(list.find((n) => n.id === created.id)?.display.route_carriers) === JSON.stringify(["电信", "联通"]);
}, "route carriers saved");
await until(() => !$("#dialog").open, "node form closed after saving route carriers");
// 复原成「全部显示」，后面公开卡片那段按三网回程断言。
assert.equal(
  (await fetch(base + "/api/v1/admin/nodes/" + created.id, {
    method: "PATCH",
    headers: { ...adminHeaders, "Content-Type": "application/json" },
    body: JSON.stringify({ display: { route_carriers: [] } }),
  })).status,
  200,
  "清空回程运营商筛选",
);
// 保存之后管理页会整块重渲染（save 里 `await render()` 重新拉 /admin/nodes），服务端读回不代表
// 页面已经换好——抢在那次重渲染之前打开表单，会把上一份节点数据填进输入框（读到空串）。
// 所以这里重试「打开 → 读 → 不对就关掉再来」，而不是打开一次就断言。
await until(
  () => {
    $(`[data-edit="${created.id}"]`).click();
    form = $("#node-form");
    const ok = form?.elements.latency_region.value === "上海";
    if (!ok) $(".close")?.click();
    return ok;
  },
  "saved latency region must reappear in the form",
);
$(".close").click();
// 通过 API 造一个阈值全 0 的节点（旧数据或脚本创建），编辑表单必须回填可保存的值
const zeroAlerts = {
  name: "UI 零阈值 " + Date.now(), group: "", region: "", order: 0, public: false,
  display: { route_region: "" },
  billing: { traffic_limit: 0, traffic_mode: "sum", reset_at: "2026-09-01T00:00:00Z", price: 0, currency: "", free: true, price_period: "", expires_at: "" },
};
const zeroResponse = await fetch(base + "/api/v1/admin/nodes", { method: "POST", headers: { ...adminHeaders, "Content-Type": "application/json" }, body: JSON.stringify(zeroAlerts) });
assert.equal(zeroResponse.status, 200, "server accepts a node with every threshold at zero");
list = await (await fetch(base + "/api/v1/admin/nodes", { headers: adminHeaders })).json();
const zeroNode = list.find((n) => n.name === zeroAlerts.name);
$("#refresh-nodes").click();
await until(() => $(`[data-edit="${zeroNode.id}"]`), "node list including the zero-threshold node");
$(`[data-edit="${zeroNode.id}"]`).click();
form = $("#node-form");
// 免费是单独的勾选：勾上时价格、货币、周期都禁用；周期里不再有「免费」这一项，
// 取消勾选时周期从「月」起。
assert(form.elements.free.checked, "免费机器要回填勾选");
for (const name of ["price", "currency", "price_period"])
  assert(form.elements[name].disabled, `免费机器的 ${name} 要禁用`);
assert(
  ![...form.elements.price_period.options].some((o) => o.value === "免费"),
  "「免费」不是价格周期",
);
assert.equal(form.elements.price_period.value, "月", "免费机器取消勾选时周期从「月」起");
// 选项表里没有的现值要补成一条额外选项：这台机器没填过货币，打开表单保存一遍不能把它
// 悄悄换成人民币（现值必须原样出现在下拉里）。
assert.equal(form.elements.currency.value, "", "没填货币的节点不能被改成人民币");
assert(
  [...form.elements.currency.options].some((o) => o.value === "" && /未设置/.test(o.textContent)),
  "空货币要有一条「未设置」选项",
);
$(".close").click();
// 新建规则时每种类型都回填可保存的默认值（比对值与 min）。规则编辑在告警页，切过去再回来。
$('[data-tab="alerts"]').click();
// 告警页分三个子页，默认是「当前告警」；规则列表在「提醒规则」里。
await until(() => $('[data-alert-view="rules"]'), "alert sub-views");
assert($('[data-alert-view="active"]').classList.contains("active"), "告警页默认先看当前告警");
$('[data-alert-view="rules"]').click();
await until(() => $("#new-rule"), "rule list");
$("#new-rule").click();
form = $("#rule-form");
for (const kind of ["offline", "renewal", "traffic", "throttle", "cpu", "memory"]) {
  form.elements.kind.value = kind;
  form.elements.kind.dispatchEvent(new w.Event("change", { bubbles: true }));
  for (const el of form.querySelectorAll("#rule-params input"))
    assert(Number(el.value) >= Number(el.min), `${kind}.${el.name}=${el.value} is below min=${el.min}: the rule could never be saved`);
}
assert.equal(form.elements.threshold.value, "90", "内存默认 90%");
$(".close").click();
$('[data-tab="nodes"]').click();
await until(() => $("#add-node"), "node list again");
await fetch(base + "/api/v1/admin/nodes/" + zeroNode.id, { method: "DELETE", headers: adminHeaders });
// 拖动排序：松手时把整份可见 id 列表交给 POST /api/v1/admin/nodes/order，服务端按 0,1,2…
// 重写 order（前端不自己算号，也不逐台 PUT 整份配置）。jsdom 没有 PointerEvent，elementFromPoint
// 也不做命中测试（没有布局），所以这里按 clientY 当行号直接喂给 elementFromPoint：验的是手柄
// 接线、落点判断后的 DOM 顺序、请求体与服务端落库。真浏览器里的实际拖动另外单独验收过。
{
  const dragName = "拖动验收 " + Date.now();
  const madeIDs = [];
  for (const suffix of ["甲", "乙"]) {
    const resp = await fetch(base + "/api/v1/admin/nodes", {
      method: "POST",
      headers: { ...adminHeaders, "Content-Type": "application/json" },
      body: JSON.stringify({
        name: `${dragName}${suffix}`,
        region: "香港",
        billing: { reset_at: "2026-09-01T00:00:00Z", traffic_mode: "sum" },
        public: false,
      }),
    });
    assert.equal(resp.status, 200, "拖动验收的节点要能创建");
    madeIDs.push((await resp.json()).id);
  }
  $("#refresh-nodes").click();
  await until(() => $(`[data-drag="${madeIDs[1]}"]`), "拖动排序的手柄");
  const rows = () => [...w.document.querySelectorAll("table.stack tbody tr[data-node]")];
  const before = rows().map((r) => r.dataset.node);
  assert(before.length >= 3, `拖动验收至少要有三台节点，实际 ${before.length}`);
  const target = before[before.length - 1];
  const realElementFromPoint = w.document.elementFromPoint;
  w.document.elementFromPoint = (x, y) => rows()[y] || null;
  const pointer = (type, y) => {
    const event = new w.Event(type, { bubbles: true });
    event.clientX = 0;
    event.clientY = y;
    event.button = 0;
    event.pointerId = 1;
    return event;
  };
  const handle = $(`[data-drag="${target}"]`);
  handle.dispatchEvent(pointer("pointerdown", before.length - 1));
  assert(
    handle.closest("tr").classList.contains("row-dragging"),
    "按住手柄要给出拖动中的标记",
  );
  handle.dispatchEvent(pointer("pointermove", 0));
  assert.equal(rows()[0].dataset.node, target, "拖到第一行就插在第一位");
  handle.dispatchEvent(pointer("pointerup", 0));
  w.document.elementFromPoint = realElementFromPoint;
  await until(async () => {
    const list = await (
      await fetch(base + "/api/v1/admin/nodes", { headers: adminHeaders })
    ).json();
    return list[0]?.id === target && list.every((n, i) => n.order === i);
  }, "拖动后的顺序要落库（服务端按 0,1,2… 重写 order）");
  await until(() => rows()[0]?.dataset.node === target, "拖动后列表按服务端顺序重排");
  const after = rows().map((r) => r.dataset.node);
  assert.equal(after[0], target, "原来最后一行要排到第一");
  assert.deepEqual(after.slice(1), before.slice(0, -1), "只动被拖的那一行");
  assert(!rows()[0].classList.contains("row-dragging"), "松手要清掉拖动中标记");
  // 松手只存顺序、不整页重画；本地 state 同步成服务端的 0,1,2…，之后编辑节点带回的 order 不会是旧值。
  // 键盘：聚焦手柄按 End 移到最后，停 0.5 秒后存一次。
  const first = rows()[0].dataset.node;
  $(`[data-drag="${first}"]`).dispatchEvent(new w.KeyboardEvent("keydown", { key: "End", bubbles: true }));
  assert.equal(rows().at(-1).dataset.node, first, "End 要把这一行移到最后");
  await until(async () => {
    const list = await (await fetch(base + "/api/v1/admin/nodes", { headers: adminHeaders })).json();
    return list.at(-1)?.id === first;
  }, "键盘调整后的顺序要落库");
  // 拖动中按 Esc：放回按下时的位置，不发请求。
  const snapshot = rows().map((r) => r.dataset.node);
  const second = $(`[data-drag="${snapshot[1]}"]`);
  w.document.elementFromPoint = (x, y) => rows()[y] || null;
  second.dispatchEvent(pointer("pointerdown", 1));
  second.dispatchEvent(pointer("pointermove", 0));
  assert.equal(rows()[0].dataset.node, snapshot[1], "拖到第一行就插在第一位");
  w.document.dispatchEvent(new w.KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
  w.document.elementFromPoint = realElementFromPoint;
  assert.deepEqual(rows().map((r) => r.dataset.node), snapshot, "Esc 要把拖动中的行放回原位");
  assert(!rows()[1].classList.contains("row-dragging"), "Esc 要清掉拖动中标记");
  // 一键排序：按名称重排全部节点（拼音 / 数字大小），页面与服务端的顺序都要跟着变。
  $("#sort-nodes").click();
  await until(() => $('[data-sort="name"]'), "一键排序弹窗");
  $('[data-sort="name"]').click();
  const collator = new Intl.Collator("zh-Hans-CN", { numeric: true, sensitivity: "base" });
  await until(async () => {
    const list = await (await fetch(base + "/api/v1/admin/nodes", { headers: adminHeaders })).json();
    const names = list.map((n) => n.name);
    return names.every((name, i) => i === 0 || collator.compare(names[i - 1], name) <= 0) &&
      list.every((n, i) => n.order === i) &&
      rows().map((r) => r.dataset.node).join() === list.map((n) => n.id).join();
  }, "一键按名称排序后服务端与页面顺序一致");
  await until(() => $("#toast").textContent === "已按名称排序", "一键排序的提示: " + $("#toast").textContent);
  for (const id of madeIDs)
    await fetch(base + "/api/v1/admin/nodes/" + id, { method: "DELETE", headers: adminHeaders });
}
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
    ?.querySelectorAll(".chip-family.on[data-family]");
assert.equal(chipFamilies("电信").length, 2, "a freshly selected group follows the global default");
// 延迟和回程都勾上时，每组还有自己的检测项开关；三网目录的组默认两项都跑。
assert.deepEqual(
  [...[...w.document.querySelectorAll(".target-chip")]
    .find((c) => c.querySelector("strong").textContent.includes("电信"))
    .querySelectorAll(".chip-family.on[data-check]")].map((b) => b.dataset.check),
  ["tcp", "route"],
  "三网目录的组默认延迟与回程都测",
);
{
  // 批量检测项：「回程 · 全部不测」关掉每组的回程，「全部测」再全部打开（之后的保存仍按全开断言）。
  const routeOn = () =>
    [...w.document.querySelectorAll('.target-chip .chip-family[data-check="route"]')].map((b) => b.classList.contains("on"));
  w.document.querySelector('[data-batch-check="route"][data-batch-mode="none"]').click();
  assert(routeOn().length && routeOn().every((on) => !on), "批量「回程 · 全部不测」要关掉每组的回程");
  w.document.querySelector('[data-batch-check="route"][data-batch-mode="all"]').click();
  assert(routeOn().every((on) => on), "批量「回程 · 全部测」要把每组的回程打开");
}
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
assert.equal(createdTask.type, "probe", "网络探测任务以 type=probe 落库");
assert.deepEqual(createdTask.probe.checks, ["tcp", "route"]);
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
// 每秒那层只取 /nodes/live（会动的那部分），并进已有节点后直接写；10 秒那层取完整的 /nodes。
assert(
  /const live = await api\("\/nodes\/live"\);\n\s*for \(const update of live\) \{/.test(appSource) &&
    /const next = await api\("\/nodes"\);/.test(appSource) &&
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
const cssSource = resolveTokens(await (await fetch(base + "/styles.css")).text());
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
const taskRegions = [...new Set(createdTask.probe.targets.map((x) => x.region))];
for (const r of taskRegions) assert(regionCell.textContent.includes(r), `地区 ${r} 必须出现在列表里: ${regionCell.textContent}`);
for (const r of taskRegions)
  assert.equal((regionCell.textContent.match(new RegExp(r, "g")) || []).length, 1, `地区名 ${r} 不应按运营商重复: ${regionCell.textContent}`);
assert.equal(regionCell.title.split("、").length, taskRegions.length, "悬停里要是完整名单");
assert.equal(/…/.test(regionCell.textContent), taskRegions.length > 3, "只有超过三个地区才折行: " + regionCell.textContent);
if (taskRegions.length > 3) assert(/共 \d+ 个地区/.test(regionCell.textContent), "折行时要写明地区总数: " + regionCell.textContent);
else assert(!/个地区/.test(regionCell.textContent), "地区不多时不该再写数量: " + regionCell.textContent);
// 三组目标：电信只留 IPv4，其余双栈
assert.equal(createdTask.probe.targets.length, 5);
assert.deepEqual(
  createdTask.probe.targets.filter((x) => x.carrier === "电信").map((x) => x.family),
  ["ipv4"],
  "a group limited to IPv4 must not save its IPv6 address",
);
assert.equal(createdTask.probe.route_interval, 3600);
assert.equal(createdTask.probe.interval, 60);
$('[data-edit-task="' + createdTask.id + '"]').click();
await until(() => $("#task-form"), "edit combined task");
assert.equal(w.document.querySelectorAll("[data-remove]").length, 3);
// 自定义目标的地区候选同样要带上常用海外地区，并且是同一个可检索控件（节点表单那套
// region-picker）：打 xg 就命中香港，中文 / 全拼 / 代号都走 web/search.js。
const taskRegionBox = w.document
  .querySelector('[data-combo] input[name="custom-region"]')
  .closest("[data-combo]");
const taskRegionHits = (query) => {
  const input = taskRegionBox.querySelector(".combo-input");
  input.value = query;
  input.dispatchEvent(new w.Event("input", { bubbles: true }));
  return [...taskRegionBox.querySelectorAll("[data-region-option]")]
    .filter((b) => !b.hidden)
    .map((b) => b.dataset.regionOption);
};
const taskRegionOptions = new Set(
  [...taskRegionBox.querySelectorAll("[data-region-option]")].map((b) => b.dataset.regionOption),
);
for (const region of ["香港", "美国"])
  assert(taskRegionOptions.has(region), `自定义目标地区候选缺少 ${region}（共 ${taskRegionOptions.size} 项）`);
assert(taskRegionHits("xg").includes("香港"), "自定义目标地区也要能按拼音检索");
taskRegionHits("");
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
assert.equal(editedTask.probe.targets.length, 7, "5 catalog addresses (电信 only IPv4) plus the custom dual-stack pair");
assert.equal(editedTask.probe.targets.filter((x) => x.carrier === "其他").length, 2);
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
    billing: { reset_at: "2026-09-01T00:00:00Z", traffic_mode: "sum" },
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
assert.equal(unlockTask.type, "unlock", "任务要以 type=unlock 落库");
assert.equal(unlockTask.unlock?.interval, 3600, "解锁间隔要落库");
assert.equal(unlockTask.probe, undefined, "解锁任务不带 probe（没有目标）");
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
// 行内不再有「绑定」：勾选任务后用顶部的「批量绑定节点」（单个任务就是一批一个）。
$('[data-select-task="' + createdTask.id + '"]').checked = true;
$("#bind-selected").click();
await until(() => $("#node-search"), "bind dialog");
// 只勾一个任务时标题写明是哪个任务，不叫「批量」。
assert(/^绑定节点 · /.test($("#dialog-title").textContent), "单个任务的绑定弹窗要写明任务名: " + $("#dialog-title").textContent);
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
await until(() => $('[data-alert-view="rules"]'), "alert page");
$('[data-alert-view="rules"]').click();
await until(() => $("#configure-webhook"), "rules sub-view");
$("#configure-webhook").click();
assert($('#dialog input[name="webhook"]'));
$(".close").click();
// 提醒规则：一条规则一种类型，范围是全部节点 / 分组 / 指定节点；同类型按 指定节点 > 分组 > 全部 只生效一条。
const ruleNodes = async () => (await (await fetch(base + "/api/v1/admin/nodes", { headers: adminHeaders })).json());
const pickScope = (form, scope) => {
  const radio = form.querySelector(`input[name="scope"][value="${scope}"]`);
  radio.checked = true;
  radio.dispatchEvent(new w.Event("change", { bubbles: true }));
};
const pickKind = (form, kind) => {
  form.elements.kind.value = kind;
  form.elements.kind.dispatchEvent(new w.Event("change", { bubbles: true }));
};
{
  const fleet = await ruleNodes();
  const other = fleet.find((n) => n.id !== created.id);
  // 一条作用于全部节点的 CPU 规则：默认范围就是全部，预览写明覆盖几台、以后新加的也套用。
  $("#new-rule").click();
  form = $("#rule-form");
  assert(form.textContent.includes("重复间隔填 0"), "提示行要说明重复间隔的规则");
  pickKind(form, "cpu");
  assert.equal(form.elements.threshold.value, "90");
  assert.equal(form.elements.hold_seconds.value, "300");
  assert(form.querySelector('[data-part="nodes"]').hidden, "范围是全部节点时不显示节点列表");
  assert(new RegExp(`覆盖 ${fleet.length} 台节点`).test($("#rule-preview").textContent), "预览写明覆盖几台: " + $("#rule-preview").textContent);
  assert(/以后新加的节点也会自动套用/.test($("#rule-preview").textContent), "全部节点的规则要说明新节点也会套用");
  set(form, "repeat", "60");
  submit(form);
  await until(async () => (await ruleNodes()).every((n) => {
    const cpu = n.alerts.find((r) => r.kind === "cpu");
    return cpu?.scope === "all" && cpu.threshold === 90 && cpu.repeat_seconds === 3600;
  }), "all-nodes CPU rule reaches every node");
  // 再给其中一台单独设一条：这台以单独的为准，其余仍按全部节点那条。
  await until(() => $("#new-rule") && !$("#dialog").open && w.document.querySelectorAll("[data-delete-rule]").length === 1, "rule list re-rendered with the all-nodes rule");
  $("#new-rule").click();
  form = $("#rule-form");
  pickKind(form, "cpu");
  pickScope(form, "nodes");
  assert(!form.querySelector('[data-part="nodes"]').hidden, "指定节点时显示节点列表");
  form.querySelector(`[data-node="${created.id}"]`).checked = true;
  set(form, "threshold", "70");
  form.dispatchEvent(new w.Event("change"));
  assert(/覆盖 1 台节点/.test($("#rule-preview").textContent), "预览随勾选更新: " + $("#rule-preview").textContent);
  submit(form);
  await until(async () => {
    const list = await ruleNodes();
    const mine = list.find((n) => n.id === created.id).alerts.find((r) => r.kind === "cpu");
    const theirs = list.find((n) => n.id === other.id).alerts.find((r) => r.kind === "cpu");
    return mine?.scope === "nodes" && mine.threshold === 70 && theirs?.scope === "all" && theirs.threshold === 90;
  }, "node rule overrides the all-nodes rule on that node only");
  await until(
    () => !$("#dialog").open && w.document.querySelectorAll("[data-delete-rule]").length === 2 && /另 1 台以更具体的规则为准/.test($("#admin-content").textContent),
    "rule list with both rules",
  );
  assert(/另 1 台以更具体的规则为准/.test($("#admin-content").textContent), "宽规则要写明有几台被更具体的规则盖过");
  assert($("#admin-content").textContent.includes("单独"), "生效总览要写明规则来自哪一级");
  // 同类型同范围不能重叠：第二条全部节点的 CPU 规则被拒，弹窗留着、规则不增加。
  $("#new-rule").click();
  form = $("#rule-form");
  pickKind(form, "cpu");
  submit(form);
  await until(() => /全部节点的CPU规则/.test($("#toast").textContent), "overlapping rule refused: " + $("#toast").textContent);
  assert($("#dialog").open, "被拒时弹窗不关");
  $(".close").click();
  // 删掉单独那条：这台回到全部节点那条。
  const rules = await (await fetch(base + "/api/v1/admin/alert-rules", { headers: adminHeaders })).json();
  assert.equal(rules.length, 2, "重叠的规则不该存下");
  const nodeRule = rules.find((r) => r.scope === "nodes");
  $(`[data-delete-rule="${nodeRule.id}"]`).click();
  $("#confirm").click();
  await until(async () => (await ruleNodes()).find((n) => n.id === created.id).alerts.find((r) => r.kind === "cpu")?.threshold === 90, "deleting the node rule falls back to the wider one");
  // 收尾：后面的验收不该被 CPU 规则的告警干扰。
  for (const r of await (await fetch(base + "/api/v1/admin/alert-rules", { headers: adminHeaders })).json())
    await fetch(base + "/api/v1/admin/alert-rules/" + r.id, { method: "DELETE", headers: adminHeaders });
  await until(() => !$("#dialog").open, "dialog closed");
  // 从接口（MCP 同理）建的规则：回到规则页时节点要一起重读，否则读的还是旧节点上的 alerts，
  // 列表会写成「生效 0 台」；值不在面板步长上（85.5%、7 分钟）也要能在面板里原样存回去。
  const made = await (
    await fetch(base + "/api/v1/admin/alert-rules", {
      method: "POST",
      headers: { ...adminHeaders, "Content-Type": "application/json" },
      body: JSON.stringify({ kind: "memory", threshold: 85.5, hold_seconds: 60, repeat_seconds: 420, scope: "all" }),
    })
  ).json();
  $('[data-alert-view="active"]').click();
  await until(() => $('[data-alert-view="active"]').classList.contains("active"), "active view");
  $('[data-alert-view="rules"]').click();
  await until(() => $(`[data-edit-rule="${made.id}"]`), "rule made through the API is listed");
  const madeRow = $(`[data-edit-rule="${made.id}"]`).closest("tr").textContent;
  assert(madeRow.includes(`生效 ${fleet.length} 台`), "规则页要按刚读的节点算生效台数: " + madeRow);
  $(`[data-edit-rule="${made.id}"]`).click();
  form = $("#rule-form");
  assert.equal(form.elements.threshold.value, "85.5");
  assert.equal(form.elements.repeat.value, "7");
  assert(form.checkValidity(), "接口存下的值在面板里要能原样保存: " + [...form.elements].filter((el) => el.validity && !el.validity.valid).map((el) => el.name).join(","));
  submit(form);
  await until(() => !$("#dialog").open && /提醒规则已保存/.test($("#toast").textContent), "rule re-saved unchanged: " + $("#toast").textContent);
  const saved = (await (await fetch(base + "/api/v1/admin/alert-rules", { headers: adminHeaders })).json()).find((r) => r.id === made.id);
  assert(saved.threshold === 85.5 && saved.repeat_seconds === 420, "原样存回: " + JSON.stringify(saved));
  await fetch(base + "/api/v1/admin/alert-rules/" + made.id, { method: "DELETE", headers: adminHeaders });
  // 分组改名或写错后，分组规则存得下但一台都不覆盖：列表与编辑框都要标出来。
  const ghost = await (
    await fetch(base + "/api/v1/admin/alert-rules", {
      method: "POST",
      headers: { ...adminHeaders, "Content-Type": "application/json" },
      body: JSON.stringify({ kind: "offline", hold_seconds: 0, scope: "group", groups: ["已改名的分组"] }),
    })
  ).json();
  $("#refresh-events").click();
  await until(() => $(`[data-edit-rule="${ghost.id}"]`), "group rule listed");
  const ghostRow = $(`[data-edit-rule="${ghost.id}"]`).closest("tr");
  assert(ghostRow.querySelector(".warn-text")?.textContent.includes("已改名的分组"), "没有节点的分组要在列表里标出: " + ghostRow.textContent);
  $(`[data-edit-rule="${ghost.id}"]`).click();
  form = $("#rule-form");
  assert(form.textContent.includes("已改名的分组（没有节点）"), "编辑框要标出没有节点的分组");
  $("#dialog").close();
  await fetch(base + "/api/v1/admin/alert-rules/" + ghost.id, { method: "DELETE", headers: adminHeaders });
}
// 面板显示项（延迟色带 / 默认外观）在单独的「面板设置」页，不再挂在告警页里。
assert(!$("#latency-window"), "告警页不该再出现面板显示项");
$('[data-tab="settings"]').click();
await until(() => $("#latency-window"), "settings page");
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
        await fetch(base + "/api/v1/admin/panel", { headers: { Cookie: cookie } })
      ).json();
      return Number(view.latency_window_minutes) === minutes;
    },
    `latency window ${minutes}`,
  );
  // 接口读回只说明落了库；页面是保存后再取一次设置才重渲染的，要等那次渲染完再看输入框。
  await until(
    () => $("#latency-window")?.value === String(minutes),
    "重渲染后输入框该显示已保存的时间窗",
  );
};
await saveWindow(1440);
await saveWindow(60);
// 前台默认外观：后台设的默认明暗 / 皮肤落进设置，并且首屏脚本（theme-boot.js 读的那份
// /theme-default.js）必须跟着它走——否则访客第一眼还是内置默认。
const themeField = $("#default-theme");
const skinField = $("#default-skin");
assert.equal(themeField.value, "auto", "未设置时默认明暗是跟随系统");
assert.equal(skinField.value, "shadcn", "未设置时默认皮肤是现代极简");
assert.equal(
  [...themeField.options].map((o) => o.value).join(","),
  "auto,light,dark",
  "明暗三档",
);
// 皮肤清单与 web/skins.js 的 SKINS 同源，两边对不上会被 web/look_choices_test.go 抓到。
assert.equal(
  [...skinField.options].map((o) => o.value).join(","),
  "shadcn,anime,glass,press",
  "默认皮肤只列内置的现代极简、液态玻璃与已安装的主题包",
);
// 这三个下拉在页面上是自定义外壳（ui-select.js 把原生 select 换成 .select-trigger + .select-pop），
// jsdom 里不加载 ui-select.js，所以下列断言只能按源码与样式表文本盯住。教训：选项一旦写回
// <button>，按钮基样式 `.webhook-actions button`（0,1,1）就会压过 glass.css 的 .select-opt
// （0,1,0），把选项排成一行白底按钮、从弹层两侧溢出去（线上「默认外观」就是这样被切掉的）。
assert(
  /<div class="select-opt/.test(selectSource) && !/<button[^>]*class="select-opt/.test(selectSource),
  "下拉选项要用 div，不能用 button（会吃 .webhook-actions button 那套按钮基样式）",
);
assert(
  /<div class="select-opt[^>]*role="option"/.test(selectSource) &&
    /setAttribute\("role", "listbox"\)/.test(selectSource),
  "弹层要带 listbox / option 语义",
);
assert(
  /\.panel:has\(\.select-shell\.open\) \{[^}]*overflow: visible/.test(styles),
  "展开下拉的面板要放开 overflow（否则弹层被 .panel 的 overflow:hidden 裁掉）",
);
assert(
  /\.select-pop\.pop-up \{[^}]*bottom: calc\(100% \+ 6px\)/.test(styles),
  "上方更宽时弹层要能翻上去（手机下半屏的下拉）",
);
const saveLook = async (theme, skin) => {
  $("#default-theme").value = theme;
  $("#default-skin").value = skin;
  $("#save-look").click();
  await until(
    async () => {
      const view = await (
        await fetch(base + "/api/v1/admin/panel", { headers: { Cookie: cookie } })
      ).json();
      return view.default_theme === theme && view.default_skin === skin;
    },
    `default look ${theme}/${skin}`,
  );
  await until(
    () => $("#default-theme")?.value === theme && $("#default-skin")?.value === skin,
    "重渲染后要显示已保存的明暗与皮肤",
  );
  const js = await (await fetch(base + "/theme-default.js")).text();
  assert(
    js.includes(`theme:"${theme}"`) && js.includes(`skin:"${skin}"`),
    "首屏脚本没跟上设置: " + js,
  );
};
await saveLook("dark", "press");
await saveLook("auto", "shadcn");
// 站点品牌：改名称后本页的标签页标题与侧栏品牌当场换掉，首屏脚本也跟上；logo 上传后侧栏、
// favicon 与设置页预览都用它；两项清掉回到内置的 TZA Probe（t·z·a 字标 + PROBE）。
const brandBoot = async () => {
  const js = await (await fetch(base + "/theme-default.js")).text();
  return JSON.parse(js.match(/^window\.__tzaBrand=(.*);$/m)[1]);
};
assert.deepEqual(await brandBoot(), { name: "", logo: "" }, "未配置时首屏品牌为空");
assert.equal($("#site-name").value, "", "未配置时站点名称为空");
assert($(".sidebar .brand .brand-mark") && !$("#remove-site-logo"), "未配置时用内置字标、没有移除按钮");
$("#site-name").value = "  冒烟 <b>监控</b> ";
$("#save-site-name").click();
await until(() => $("#site-name")?.value === "冒烟 <b>监控</b>", "重渲染后显示已保存的站点名称");
assert.equal(w.document.title, "冒烟 <b>监控</b>", "标签页标题当场换掉");
assert.equal($(".sidebar .brand .brand-name").firstChild.textContent, "冒烟 <b>监控</b>", "侧栏品牌当场换掉（按文本转义）");
assert(!$(".sidebar .brand b"), "站点名称不能被当成 HTML");
assert.equal((await brandBoot()).name, "冒烟 <b>监控</b>", "首屏脚本跟上站点名称");
const smokeLogo = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 8"><rect width="8" height="8" fill="#2b8a3e"/></svg>';
const uploaded = await fetch(base + "/api/v1/admin/panel/logo", { method: "PUT", headers: { Cookie: cookie, "Content-Type": "image/svg+xml" }, body: smokeLogo });
assert.equal(uploaded.status, 200, "上传 logo");
const { site_logo: logoURL } = await uploaded.json();
assert.equal((await brandBoot()).logo, logoURL, "首屏脚本跟上 logo");
assert.equal(await (await fetch(base + logoURL)).text(), smokeLogo, "/site-logo 原样提供上传的图");
$('[data-tab="settings"]').click();
await until(() => $("#remove-site-logo"), "上传 logo 后设置页出现移除按钮");
assert.equal($(".brand-preview img")?.getAttribute("src"), logoURL, "设置页预览用新 logo");
$("#remove-site-logo").click();
await until(() => $("#dialog #confirm"), "移除 logo 的确认框");
$("#dialog #confirm").click();
await until(() => !$("#remove-site-logo"), "移除 logo 后重渲染");
assert.equal((await brandBoot()).logo, "", "移除后首屏没有 logo");
assert.equal(w.document.querySelector('link[rel="icon"]')?.getAttribute("href") ?? "/favicon.svg", "/favicon.svg", "favicon 回到默认");
$("#site-name").value = "";
$("#save-site-name").click();
await until(() => w.document.title === "TZA Probe", "清空站点名称后标题回到默认");
assert($(".sidebar .brand .brand-mark") && $(".sidebar .brand").textContent.startsWith("tzaPROBE"), "侧栏回到内置品牌");
assert.deepEqual(await brandBoot(), { name: "", logo: "" }, "清除后首屏品牌为空");
// 设置页分子标签：MCP Key、后台账号与 TZA-CC 账号在「账号与安全」，备份在「数据」。
assert.deepEqual(
  [...w.document.querySelectorAll("[data-settings-view]")].map((b) => b.textContent),
  ["外观与品牌", "账号与安全", "数据"],
  "设置页的三个子标签",
);
assert(!$("#generate-mcp-key") && !$("#download-backup"), "「外观与品牌」里不该有 MCP Key 与备份");
$('[data-settings-view="data"]').click();
await until(() => $("#download-backup"), "数据子页");
$('[data-settings-view="account"]').click();
await until(() => $("#generate-mcp-key"), "账号与安全子页");
assert($(".market-connect")?.textContent.includes("市场已关闭"), "市场关闭时 TZA-CC 账号一栏写明不需要连接");
// MCP Key management is reachable from settings and never exposed on later reads.
$("#generate-mcp-key").click();
await until(() => $("#mcp-api-key"), "generate MCP key");
const firstMCPKey = $("#mcp-api-key").value;
assert(firstMCPKey.startsWith("tza_mcp_"));
await until(() => $("#revoke-mcp-key"), "configured MCP key");
const mcpCall = async (key) => fetch(base + "/mcp", {
  method: "POST", headers: { Authorization: "Bearer " + key, "Content-Type": "application/json" },
  body: JSON.stringify({ jsonrpc: "2.0", id: 1, method: "tools/list" }),
});
assert.equal((await mcpCall(firstMCPKey)).status, 200);
const keyState = await (await fetch(base + "/api/v1/admin/mcp-key", { headers: adminHeaders })).json();
assert(keyState.configured && !keyState.api_key && !keyState.hash);
$("#dialog .close").click();
$("#generate-mcp-key").click();
assert($("#confirm-mcp"), "rotation confirmation");
$("#confirm-mcp").click();
await until(() => $("#mcp-api-key"), "rotate MCP key");
const secondMCPKey = $("#mcp-api-key").value;
assert.notEqual(secondMCPKey, firstMCPKey);
assert.equal((await mcpCall(firstMCPKey)).status, 401);
assert.equal((await mcpCall(secondMCPKey)).status, 200);
await until(() => $("#revoke-mcp-key"), "rotated settings");
$("#dialog .close").click();
$("#revoke-mcp-key").click();
$("#confirm-mcp").click();
await until(() => !$("#revoke-mcp-key"), "revoke MCP key");
assert.equal((await mcpCall(secondMCPKey)).status, 401);

// 主题与插件各占侧栏一栏，各带一块市场；不再有单独的「市场」栏。
assert(!$('[data-tab="market"]'), "市场不再单独成栏");
// 一次性 Server 用 --cc-url off 关掉了市场（不连官方 CC）：市场块写明怎么打开，市场接口 503，已安装列表照常出来。
// 主题、插件页都分「已安装」「市场」两个子页，默认在已安装。
$('[data-tab="themes"]').click();
await until(() => $("#theme-file"), "themes page");
assert(!$(".market-off") && $('[data-subview="installed"]').getAttribute("aria-selected") === "true", "主题页默认是已安装子页，不带市场");
assert(!$(".theme-panel .list-pager"), "一页放得下时没有翻页条");
$('[data-subview="market"]').click();
await until(() => $(".market-off") && !$("#theme-file"), "themes market without TZA-CC");
assert($(".market-off").textContent.includes("--cc-url"), "未配置时要说明怎么配");
$('[data-subview="installed"]').click();
await until(() => $("#theme-file"), "back to installed themes");
assert.deepEqual(
  [...w.document.querySelectorAll(".theme-panel .theme-list > .theme-row > .theme-copy > b")].map((b) => b.textContent),
  ["现代极简", "二次元", "液态玻璃", "复古印刷"],
  "主题页列出内置的现代极简、液态玻璃和刚装的两个官方主题包",
);
// 主题设置：声明了 settings 的主题旁边有「设置」，点开是它自己的菜单；二次元的角色包就在里面开关。
assert(!$('[data-theme-settings="press"]') && !$('[data-theme-settings="glass"]'), "没声明设置的主题没有设置入口");
const animeGear = $('[data-theme-settings="anime"]');
assert(animeGear, "二次元旁边有「设置」");
assert(!$("#theme-settings-anime"), "主题列表不内联详细设置");
animeGear.click();
assert($("#dialog").open && $("#theme-settings-frame"), "设置在弹窗中的开发者界面打开");
assert($("#theme-settings-frame").getAttribute("sandbox") === "allow-scripts", "开发者弹窗隔离运行");
$("#dialog .close").click();
await until(() => !$("#theme-settings-frame"), "closing settings disposes the developer frame");
assert($('[data-remove-theme="glass"]') && !$('[data-remove-theme="shadcn"]') && $('[data-remove-theme="press"]'), "只有现代极简不能删除，液态玻璃是普通主题包");
$('[data-tab="plugins"]').click();
// 面包屑在切页时立刻换，内容要等接口回来：等插件页自己的上传入口出现，别拿主题页残留的 .market-off 当成功。
await until(() => $("#plugin-file"), "plugins page without TZA-CC");
assert($(".theme-panel") && !$(".market-off"), "插件页默认是已安装列表");
$('[data-subview="market"]').click();
await until(() => $(".market-off") && !$("#plugin-file"), "plugins market without TZA-CC");
assert.equal((await fetch(base + "/api/v1/admin/market/catalog", { headers: adminHeaders })).status, 503);
assert.equal((await (await fetch(base + "/api/v1/admin/market/status", { headers: adminHeaders })).json()).configured, false);

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
// 主题包的样式表在切到它时才挂上，面板等它加载完再切 data-skin（不先按默认样式画一帧）。jsdom 不加载
// 样式表、也不发 load 事件，这里替浏览器发：新挂上的主题包样式表一律当作已加载。
new publicDOM.window.MutationObserver((records) => {
  for (const r of records) for (const n of r.addedNodes) if (n.matches?.("link[data-theme-css]")) n.onload?.();
}).observe(publicDOM.window.document.head, { childList: true });
const themeCSSLoaded = () => new Promise((resolve) => setTimeout(resolve, 0));
publicDOM.window.HTMLDialogElement.prototype.showModal = function () {
  this.open = true;
};
publicDOM.window.HTMLDialogElement.prototype.close = function () {
  this.open = false;
};
// 与浏览器一样先跑 /theme-default.js（皮肤清单：内置 + 已安装的主题包），再加载面板。
publicDOM.window.eval(await (await fetch(base + "/theme-default.js")).text());
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
// 延迟按运营商分四格显示，与三网回程同构；地区取延迟地区，其次回程地区。
// 数据目录跨多次运行复用，所以要按名字找到本轮创建的卡片，不能取第一张。
const findPublicCard = () =>
  [...publicDOM.window.document.querySelectorAll(".node-card")].find((c) => c.textContent.includes(name));
assert(findPublicCard(), "the node created in this run must appear on the public panel");
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
// 等待期间 10 秒一轮的全量刷新可能整卡重建，先取的卡片元素会停在旧数据上（偶发「未检测」）；
// 等到值之后重新取一次。
const card = findPublicCard();
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
// 格子只画这台机器真有任务的组：它挂的是「上海 电信 / 联通 / 移动」，外加一个在新加坡的
// 自定义「其他」目标。三网按卡片的延迟地区（上海）筛；三网之外的目标一个一格、写上自己的
// 地区名，不受延迟地区限制（用户点名：多选了几个国际目标，卡片上却只剩一行）。
// 延迟块的行首是官方标志（用户点名：文字换成 logo），按 data-focus-carrier 认家；
// 名字没丢——aria-label 与筛选用的 data-focus-carrier 里都在。
const bandCarriers = [...card.querySelectorAll(".latency-bands .net-band")].map(
  (b) => b.dataset.focusCarrier,
);
for (const carrier of ["电信", "联通", "移动"])
  assert(bandCarriers.includes(carrier), `延迟块要覆盖 ${carrier}，实际 ${bandCarriers.join("/")}`);
const overseasBands = [...card.querySelectorAll('.latency-bands .net-band[data-focus-carrier="其他"]')];
assert.equal(overseasBands.length, 1, "三网之外的目标一个一格：这台只有新加坡那一个");
assert.equal(overseasBands[0].dataset.focusRegion, "新加坡", "海外格带上自己的地区，点开只看这一个目标");
assert.equal(
  overseasBands[0].querySelector(".band-name")?.textContent,
  "新加坡",
  "海外格把「延迟」二字换成目标名",
);
assert(overseasBands[0].querySelector(".carrier-mark.cx svg"), "海外格行首是地球图标");
assert.equal(card.querySelectorAll(".latency-bands .net-band").length, 4, "三网各一块 + 海外目标一块");
{
  // 商家名牌：节点填了 vendor，公开卡片上就多一枚 .vendor-plate（名字转义后显示），搜索也能按它找到。
  const patched = await fetch(base + "/api/v1/admin/nodes/" + card.dataset.node, {
    method: "PATCH",
    headers: { ...adminHeaders, "Content-Type": "application/json" },
    body: JSON.stringify({ vendor: "netcup<b>" }),
  });
  assert.equal(patched.status, 200, "PATCH vendor: " + (await patched.clone().text()));
  await until(() => findPublicCard()?.querySelector(".vendor-plate")?.textContent === "netcup<b>", "商家名牌出现在公开卡片上", 15000);
  assert.equal(findPublicCard().querySelector(".vendor-plate b"), null, "商家名要转义，不能当 HTML 渲染");
  const restored = await fetch(base + "/api/v1/admin/nodes/" + card.dataset.node, {
    method: "PATCH",
    headers: { ...adminHeaders, "Content-Type": "application/json" },
    body: JSON.stringify({ vendor: "" }),
  });
  assert.equal(restored.status, 200, "清空 vendor");
  await until(() => findPublicCard() && !findPublicCard().querySelector(".vendor-plate"), "清空商家后名牌消失", 15000);
}
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
// 并排的小色带各 10 格（列表视图也是 10 格——两个视图共用 BAR_COUNT，用户点名看齐卡片）。
// 以前卡片是一格一家、延迟与丢包各占一行数值加一条色带（三格 174px），窄卡片还要靠容器查询换布局。
// 格数只有一处定义（app.js 的 BAR_COUNT），这里按源码守一道：三处色带都传这个常量，
// 谁再写死格数这条就挂。
assert(/const BAR_COUNT = 10;/.test(appSource), "两个视图共用同一个色带格数常量");
assert.equal(
  (appSource.match(/sampleBars\([^`]*?BAR_COUNT\)/g) || []).length,
  3,
  "三处色带（列表 1 条 + 卡片 2 条）都要传 BAR_COUNT，不许写死格数",
);
{
  // 三网那三块（海外格的地球图标与目标名在上面单独验过）。
  const bands = card.querySelectorAll('.latency-bands .net-band:not([data-focus-carrier="其他"])');
  assert.equal(bands.length, 3, "延迟一家运营商一块");
  for (const band of bands) {
    const mark = band.querySelector(".band-head .carrier-name .carrier-mark");
    assert(mark && mark.querySelector("svg path[fill]"), "每块的行首要给运营商标志（与回程格子同一颗）");
    assert(mark.getAttribute("aria-hidden") === "true", "卡片里的标志同样是装饰");
    assert(!mark.querySelector("[id]") && !mark.querySelector("[style]"), "标志不能带 id / style");
    assert(
      band.getAttribute("aria-label")?.startsWith(band.dataset.focusCarrier),
      `卡片里的延迟块不写运营商名就得在 aria-label 里留着，实际「${band.getAttribute("aria-label")}」`,
    );
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
  // 少了 grid-column，第一条色带会掉进运营商槽里（真浏览器里两条一长一短、和文字错开）。
  // 槽宽只给标志本身的 11px：30px 是「写运营商名」时代的宽度，换成标志后那段空白白挂在
  // 标志与「延迟」之间（用户标红框点名「空得有点多」）。
  const head = styles.match(/\.net-band \.band-head,\s*\n\.net-band \.band-bars \{([^}]*)\}/);
  assert(
    head && /grid-template-columns: 11px minmax\(0, 1fr\) minmax\(0, 1fr\)/.test(head[1]),
    "延迟块的文字与色带要共用同一套三列栅格，且运营商槽只有标志的宽度（11px）",
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
// 外观风格（皮肤）：默认「现代极简」，另有「液态玻璃」与主题包。偏好走 localStorage 的 tza-skin，
// 由 theme-boot.js 在样式表之前写到 <html data-skin>（否则先闪一下默认皮肤）。
{
  const pubDoc = publicDOM.window.document;
  assert.equal(pubDoc.documentElement.dataset.skin, "shadcn", "默认外观风格是现代极简");
  const skinBtn = pubDoc.querySelector("[data-skin-toggle]");
  assert(skinBtn, "公开页头部要有外观风格入口");
  assert(/现代极简/.test(skinBtn.textContent), "按钮上写当前皮肤名");
  skinBtn.dispatchEvent(new publicDOM.window.MouseEvent("click", { bubbles: true }));
  const menu = pubDoc.getElementById("skin-menu");
  assert(menu && !menu.hidden, "点一下要展开皮肤菜单");
  assert.equal(skinBtn.getAttribute("aria-expanded"), "true");
  const opts = [...menu.querySelectorAll(".skin-option")];
  assert.deepEqual(
    opts.map((o) => o.dataset.skinOption),
    ["shadcn", "anime", "glass", "press"],
    "内置的现代极简、液态玻璃与已安装的主题包都要列出来",
  );
  assert.equal(opts.find((o) => o.classList.contains("on"))?.dataset.skinOption, "shadcn", "当前皮肤要有选中态");
  for (const o of opts) {
    assert(o.querySelector(".skin-copy b")?.textContent, "每个皮肤要有名字");
    assert(o.querySelector(".skin-copy small")?.textContent, "每个皮肤要有一句说明");
  }
  // 色板不能写成 HTML 里的 style 属性：页面 CSP 是 style-src 'self'，标记里的行内 style 会被浏览器直接忽略。
  // 内置皮肤的颜色在样式表里；主题包的颜色只有清单知道，由 paintSwatches 经 CSSOM 逐格写（CSP 不拦）。
  const skinsSource = await readFile(path.join(root, "web/skins.js"), "utf8");
  assert(!/skin-swatch[^`]*style=/.test(skinsSource), "色板标记里不能带 style 属性（CSP 会忽略）");
  for (const sw of menu.querySelectorAll('[data-swatch="shadcn"], [data-swatch="shadcn"] i'))
    assert(!sw.hasAttribute("style"), "内置皮肤的色板颜色来自样式表");
  for (const id of ["press", "anime"])
    assert(
      [...menu.querySelectorAll(`[data-swatch="${id}"] i`)].every((cell) => cell.style.background),
      `主题包 ${id} 的色板要按清单逐格上色`,
    );
  pubDoc.querySelector('[data-skin-option="press"]').dispatchEvent(new publicDOM.window.MouseEvent("click", { bubbles: true }));
  await themeCSSLoaded();
  assert.equal(pubDoc.documentElement.dataset.skin, "press", "选「复古印刷」要落到 <html data-skin>");
  assert.equal(publicDOM.window.localStorage.getItem("tza-skin"), "press", "偏好要存进 localStorage");
  assert(menu.hidden, "选完菜单要收起");
  assert(/复古印刷/.test(skinBtn.textContent), "按钮文字跟着换");
  // 第三张皮肤：内置的液态玻璃。
  skinBtn.dispatchEvent(new publicDOM.window.MouseEvent("click", { bubbles: true }));
  pubDoc.querySelector('[data-skin-option="glass"]').dispatchEvent(new publicDOM.window.MouseEvent("click", { bubbles: true }));
  await themeCSSLoaded();
  assert.equal(pubDoc.documentElement.dataset.skin, "glass", "选「液态玻璃」要落到 <html data-skin>");
  assert.equal(publicDOM.window.localStorage.getItem("tza-skin"), "glass", "偏好要存进 localStorage");
  assert(/液态玻璃/.test(skinBtn.textContent), "按钮文字跟着换");
  // 第四张皮肤：二次元（线路机角色立绘）。
  skinBtn.dispatchEvent(new publicDOM.window.MouseEvent("click", { bubbles: true }));
  pubDoc.querySelector('[data-skin-option="anime"]').dispatchEvent(new publicDOM.window.MouseEvent("click", { bubbles: true }));
  await themeCSSLoaded();
  assert.equal(pubDoc.documentElement.dataset.skin, "anime", "选「二次元」要落到 <html data-skin>");
  assert.equal(publicDOM.window.localStorage.getItem("tza-skin"), "anime", "偏好要存进 localStorage");
  assert(/二次元/.test(skinBtn.textContent), "按钮文字跟着换");
  // 名单外的节点也摆立绘：落到二次元清单里标了 default 的通用人物（所有线路机在二次元皮肤下都有人物、观感统一）。
  const genericCasts = [...pubDoc.querySelectorAll('[data-part="card-backdrop"]')];
  assert(genericCasts.length > 0, "名单外的节点也要摆立绘：落到通用人物 anime/generic（二次元 theme.json 的 default 装饰项）");
  assert(
    genericCasts.every((el) => ["anime/generic", "anime/jp-nosla-gen2"].includes(el.dataset.deco)),
    "日本按地区匹配，香港等无对应角色的地区使用通用人物",
  );
  // 折叠开关：点卡片头那颗名字按钮 → 卡壳挂上 data-rolled、状态按节点 id 落 localStorage；再点一下 →
  // 摊开、本地那份也清掉。这里塞一张合成卡——走的仍是页面里同一段代码（点击是 document 上的委托，
  // 状态机与真实卡片共用）。
  const foldHost = pubDoc.createElement("div");
  foldHost.innerHTML =
    '<article class="node-card" data-node="fold-fixture" data-deco="anime/us-vmiss-tri" data-deco-pack="anime">' +
    '<div data-part="card-backdrop" class="card-backdrop" data-deco="anime/us-vmiss-tri" data-bleed><span data-part="card-backdrop-layer" class="card-backdrop-layer"></span></div>' +
    '<div class="card-body"><div class="card-top">' +
    '<button class="fold-name" type="button" data-fold="fold-fixture" aria-expanded="true">' +
    '<span class="node-dot"></span><span class="fold-name-text">US-BWH-DC9</span></button>' +
    '</div></div><div data-part="card-overlay" class="card-overlay"></div></article>';
  pubDoc.body.appendChild(foldHost);
  const foldCard = foldHost.querySelector(".node-card");
  const foldClick = (el) =>
    el.dispatchEvent(new publicDOM.window.MouseEvent("click", { bubbles: true }));
  foldClick(foldHost.querySelector(".fold-name"));
  assert.equal(foldCard.dataset.rolled, "true", "点「卷起」要把卡片标成卷起");
  assert.deepEqual(
    JSON.parse(publicDOM.window.localStorage.getItem("tza-folded-cards")),
    ["fold-fixture"],
    "卷起状态要记住（刷新之后还卷着）",
  );
  assert.equal(
    foldHost.querySelector(".fold-name").getAttribute("aria-expanded"),
    "false",
    "折叠后名字那颗按钮自己就是开关（aria-expanded=false）——名字位置不动，折叠只影响数据",
  );
  // 闭眼层的显示在下面「二次元皮肤里 display:block」那条 CSS 断言里查：
  // jsdom 里没有装样式表，这里量 computed style 永远是空字符串。
  foldClick(foldHost.querySelector(".fold-name"));
  assert.equal(foldCard.dataset.rolled, undefined, "再点一次名字要摊开");
  assert.equal(
    foldHost.querySelector(".fold-name").getAttribute("aria-expanded"),
    "true",
    "摊开后 aria-expanded 回到 true",
  );
  assert.deepEqual(
    JSON.parse(publicDOM.window.localStorage.getItem("tza-folded-cards")),
    [],
    "摊开后本地那份状态也要清掉",
  );
  // 点卡片空白处 / 立绘空白处不进详情页：详情只由内容里那枚「详细」按钮进。
  const detailOpenOnBlank = () => pubDoc.querySelector("#detail-overlay:not([hidden]), .detail-overlay:not([hidden])");
  const blankClick = (el) => el.dispatchEvent(new publicDOM.window.MouseEvent("click", { bubbles: true }));
  const beforeBlank = !!detailOpenOnBlank();
  blankClick(foldCard);                       // 卡片空白处
  blankClick(foldHost.querySelector('[data-part="card-backdrop"]'));  // 立绘空白处
  assert.equal(
    !!detailOpenOnBlank(),
    beforeBlank,
    "点卡片空白处 / 立绘空白处都不许弹出详细信息（只有「详细」按钮能进）",
  );
  foldHost.remove();
  // 选回默认皮肤要把偏好键清掉，不在浏览器里留一份与默认值重复的设置。
  skinBtn.dispatchEvent(new publicDOM.window.MouseEvent("click", { bubbles: true }));
  pubDoc.querySelector('[data-skin-option="shadcn"]').dispatchEvent(new publicDOM.window.MouseEvent("click", { bubbles: true }));
  assert.equal(pubDoc.documentElement.dataset.skin, "shadcn", "能切回默认皮肤");
  assert.equal(publicDOM.window.localStorage.getItem("tza-skin"), null, "默认皮肤不留偏好键");

  // 皮肤表本身：只在 data-skin="<id>" 下生效。每张皮肤各有一条必须有的底座：
  // 印刷＝纸底 + 圆角归零，极简＝中性令牌 + 圆角统一到 --shadcn-radius。
  // 复古印刷与二次元是主题包：样式表由 /theme-default.js 的清单给出地址（/themes/<id>/…），
  // 只在选中时挂上；index.html 里没有它们。现代极简是内置默认，样式表就在 index.html 里。
  const homeHTML = await (await fetch(base + "/")).text();
  const themeList = publicDOM.window.__tzaThemes;
  const themeCSS = async (id) => {
    assert(!homeHTML.includes(`/${id}.css`), `${id}.css 不该再写死在 index.html 里`);
    const href = themeList.find((t) => t.id === id)?.css?.[0];
    assert(href?.startsWith(`/themes/${id}/`), `清单要给出 ${id} 的样式表地址`);
    return (await fetch(base + href)).text();
  };
  const pressCSS = await themeCSS("press");
  assert(/\[data-skin="press"\]/.test(pressCSS), "皮肤规则要限定在 data-skin=press 下");
  assert(/--press-paper:/.test(pressCSS) && /border-radius: 0 !important/.test(pressCSS), "纸底与圆角归零是皮肤底座");
  assert(/:root\[data-skin="press"\]\[data-theme="dark"\]/.test(pressCSS), "深色下要有自己的一份令牌");
  assert(homeHTML.includes('href="/shadcn.css"'), "内置的现代极简样式表写在 index.html 里");
  const shadcnCSS = resolveTokens(await (await fetch(base + "/shadcn.css")).text());
  const animeCSS = await themeCSS("anime");
  // anime.css 是所有皮肤共用的（index.html 里无条件挂），但每条规则都带 html[data-skin="anime"] 前缀；
  // 于是别的皮肤下那三件套是**没有样式的裸元素**——必须靠底座样式表默认藏起来（漏一个就会冒出一颗裸按钮）。
  // 这就是「加动画不要影响到其他皮肤」的第一道闸。
  const baseCSS = resolveTokens(await (await fetch(base + "/styles.css")).text());
  for (const part of ["card-backdrop", "card-backdrop-layer", "card-overlay", "card-plate"]) {
    assert(
      new RegExp('\\[data-part="' + part + '"\\][^{]*\\{[^}]*display: none', "s").test(baseCSS),
      `底座样式表要把装饰槽 ${part} 默认藏掉（主题包的规则都带皮肤前缀，别的皮肤下它没有样式）`,
    );
  }
  assert(
    /html\[data-skin="anime"\] \[data-part="card-backdrop"\] \{[^}]*display: block/s.test(animeCSS),
    "二次元皮肤里立绘要放开显示",
  );
  assert(/\[data-skin="shadcn"\]/.test(shadcnCSS), "皮肤规则要限定在 data-skin=shadcn 下");
  assert(
    /:root\[data-skin="shadcn"\]\s*\{[^}]*--shadcn-bg: #ffffff[^}]*--shadcn-fg: #0a0a0a/s.test(shadcnCSS) &&
      /:root\[data-skin="shadcn"\]\[data-theme="dark"\]\s*\{[^}]*--shadcn-bg: #0a0a0a[^}]*--shadcn-fg: #fafafa/s.test(shadcnCSS),
    "极简皮肤的底座是 shadcn Neutral 的两份令牌（浅色纯白 / 深色纯黑）",
  );
  assert(
    /border-radius: var\(--shadcn-radius\) !important/.test(shadcnCSS) &&
      /backdrop-filter: none !important/.test(shadcnCSS),
    "圆角统一到 shadcn 的尺度、且没有毛玻璃，是这张皮肤的定义性声明",
  );
  // 玻璃皮肤的复合阴影写法 `box-shadow: var(--glass-shadow), var(--glass-inner-rim)` 里，
  // 内描边一旦写成 none 就会让整条声明作废（连投影一起丢），必须给一个全透明阴影。
  assert(
    /--glass-inner-rim: 0 0 #0000/.test(shadcnCSS),
    "内描边要用透明阴影而不是 none，否则复合的 box-shadow 整条失效",
  );
  // 先把注释剥掉再解析规则：注释里也会出现 ::-webkit-progress-value 之类的字样。
  const pressRules = pressCSS.replace(/\/\*[\s\S]*?\*\//g, "");
  const shadcnRules = shadcnCSS.replace(/\/\*[\s\S]*?\*\//g, "");
  const animeRules = animeCSS.replace(/\/\*[\s\S]*?\*\//g, "");
  // 选择器列表按「顶层逗号」拆——:is(a, b) 里的逗号不是分隔符。
  const splitTop = (list) => {
    const out = [];
    let depth = 0, cur = "";
    for (const ch of list) {
      if (ch === "(") depth++;
      else if (ch === ")") depth--;
      if (ch === "," && depth === 0) { out.push(cur); cur = ""; continue; }
      cur += ch;
    }
    out.push(cur);
    return out;
  };
  // 每一条皮肤规则都要带自己的 data-skin 前缀（不加前缀会改到默认玻璃皮肤），
  // 且 webkit / moz 两个进度条伪元素不能写进同一个选择器列表——Chromium 把 ::-moz-progress-bar
  // 当未知伪元素，整条规则会被判无效丢弃（进度条退回玻璃皮肤的渐变填充，Chrome 里看不出来）。
  const assertSkinSheet = (rules, id) => {
    for (const rule of rules.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
      const sel = rule[1];
      assert(
        !(sel.includes("::-webkit-progress-value") && sel.includes("::-moz-progress-bar")),
        `${id} 的 webkit / moz 进度条伪元素必须分成两条规则：` + sel.trim().slice(0, 60),
      );
    }
    // @keyframes 里的 0% / 50% 步骤不是选择器，先整块拿掉再查（否则会被判成「没限定皮肤」）。
    const withoutKeyframes = rules.replace(/@(-webkit-)?keyframes[^{]*\{(?:[^{}]*\{[^{}]*\})*[^{}]*\}/g, "");
    for (const rule of withoutKeyframes.matchAll(/([^{}]+)\{/g)) {
      const sel = rule[1].trim();
      if (!sel || sel.startsWith("@") || sel.startsWith("/*")) continue;
      if (/^[\d.]+%?(\s*,\s*[\d.]+%?)*$/.test(sel)) continue;
      for (const one of splitTop(sel)) {
        const s = one.trim();
        if (!s) continue;
        assert(new RegExp(`data-skin="${id}"`).test(s), `${id}.css 里的选择器必须限定皮肤：${s.slice(0, 60)}`);
      }
    }
  };
  assert(/\[data-skin="anime"\]/.test(animeCSS), "皮肤规则要限定在 data-skin=anime 下");
  assert(
    /:root\[data-skin="anime"\]\s*\{[^}]*--anime-bg: #fdeef6[^}]*--anime-primary: #ff4f9a/s.test(animeCSS) &&
      /:root\[data-skin="anime"\]\[data-theme="dark"\]\s*\{[^}]*--anime-bg: #150f29[^}]*--anime-fg: #f7efff/s.test(animeCSS),
    "二次元皮肤要有浅色与深色两份令牌（2026-10-14 加浓：浅色樱花粉底纸 / 夜版星空紫）",
  );
  // 没有毛玻璃靠清单里的 "base": "plain"（不加载液态玻璃的材质层），样式表不再整片关模糊。
  const installedLooks = await (await fetch(base + "/api/v1/admin/themes", { headers: adminHeaders })).json();
  assert(
    /border-radius: var\(--anime-radius\) !important/.test(animeCSS) &&
      installedLooks.find((t) => t.id === "anime")?.base === "plain" &&
      !/backdrop-filter/.test(animeCSS.replace(/\/\*[\s\S]*?\*\//g, "")),
    "圆角统一到二次元的尺度、且没有毛玻璃（base: plain，样式表里不再关模糊），是这张皮肤的定义性声明",
  );
  assert(
    installedLooks.find((t) => t.id === "press")?.base === "plain" && !/backdrop-filter/.test(pressCSS.replace(/\/\*[\s\S]*?\*\//g, "")),
    "复古印刷同样是 base: plain，样式表里不再整片关模糊",
  );
  // 立绘层：默认（styles.css）不显示，二次元皮肤里放开；图片按 data-deco 挂在样式表上
  // （页面 CSP 是 style-src 'self'，行内 background-image 会被丢掉）。
  // 底座里现在是「人物层 / 双手层 / 闭眼层 / 卷起按钮」一起默认藏，
  // 逐条列出来，免得以后新增一个元素只加在 anime.css 里、别的皮肤冒裸元素。
  assert(
    // 立绘（人物层）/ 双手层 / 闭眼层 / 卷起按钮默认都不显示（逐条见上面的循环）。
    /\[data-part="card-backdrop"\],\s*\n?\[data-part="card-backdrop-layer"\],\s*\n?\[data-part="card-overlay"\],\s*\n?\[data-part="card-plate"\] \{\s*display: none;\s*\}/.test(cssSource),
    "人物层 / 双手层 / 闭眼层 / 卷起按钮默认都不显示：只在二次元皮肤里放开（别的皮肤下是没样式的裸图 / 裸 div）",
  );
  // 人物层是**一张完整连续的立绘**（用户 2026-10-10 原话「上下体不要分开，现在状态很乱」，
  // 否掉了「上/下半身两段分别裁切定位」那版）：绝对定位、固定像素高、整张图按图高缩放、顶对齐居中。
  assert(
    !/\.anime-legs\b/.test(animeCSS) && !/\.anime-legs\b/.test(appSource) &&
      !/\.anime-stage\b/.test(animeCSS) && !/\.anime-stage\b/.test(appSource),
    "不再有上半身层 / 下半身层：人物永远是一张完整连续的图（.anime-stage / .anime-legs 的元素与样式都要清干净）",
  );
  // 补空白用的徽记 / 斜纹装饰整条去掉（用户嫌乱，2026-10-10 原话「现在状态很乱」）。
  assert(
    !/--cast-stripe/.test(animeCSS) && !/cast-emblem/.test(animeCSS) && !/data-plate-tall/.test(animeCSS) && !/\.card-body::after/.test(animeCSS),
    "不再为补板面空白加徽记 / 斜纹装饰（--cast-stripe、cast-emblem、data-plate-tall、板条 ::after 都要清干净）",
  );
  const castLayerRule = /html\[data-skin="anime"\] \[data-part="card-backdrop"\] \{([^}]*)\}/.exec(animeCSS)?.[1] || "";
  assert(
    /position: absolute/.test(castLayerRule) && /height: var\(--cast-figure-h/.test(castLayerRule) &&
      /background-size: auto 100%/.test(castLayerRule) && /background-position: top center/.test(castLayerRule) &&
      /background-image: var\(--cast-art-url\)/.test(castLayerRule) && /pointer-events: none/.test(castLayerRule),
    "人物层：绝对定位、固定像素高（--cast-figure-h）、整张图按图高缩放（auto 100%）、顶对齐居中、不吃指针事件",
  );
  assert(
    /\[data-part="card-backdrop"\]\[data-deco-pack="anime"\] \{\s*background-image: var\(--cast-art-url\);\s*\}/.test(animeCSS),
    "随主题捆绑的所有地区角色都挂上立绘",
  );
  // 夜版：牌子的底色/描边要有暗色那一份，卡片子树的浅色令牌只能作用在浅色下，
  // 立绘在夜版里只裁到牌沿——不然深色的牌面上会露一条白牌面。
  assert(
    /--anime-sign: #22183d;[\s\S]*?--anime-sign-line: #4a3478;/.test(animeCSS),
    "夜版要给牌子自己的底色与描边（否则卡片还是白的）",
  );
  assert(
    /html\[data-skin="anime"\]:not\(\[data-theme="dark"\]\) \.node-card \{/.test(animeRules),
    "浅色那套令牌只能作用在浅色下，夜版要让暗色令牌落进卡片",
  );
  assert(
    /html\[data-skin="anime"\]\[data-theme="dark"\] \[data-part="card-backdrop"\],[\s\S]*?\[data-part="card-overlay"\] \{[\s\S]*?filter: brightness\(0\.93\)/s.test(animeCSS),
    "夜版把人物（整张图）与双手两层一起压一档亮度（两层同一张图、同一套处理，手才不会从手腕上断色；只轻压一档，深色鞋/裤不和夜色糊在一起）",
  );
  // 一张完整人物图（2026-10-10 v2）：① 完整人物图 card-backdrop（看板下沿以下是同一张图的裙/腿/鞋）
  // → ② HTML 看板 .card-body（直接盖在人物图上）→ ③ 双手层 card-overlay 压在看板上沿。
  // 缺一条就退回旧毛病（板顶/手边透出原图牌子的描边残线、手被 CSS 抠出硬边、手浮在板外）。
  // 双手层的**主规则**：`@media (prefers-reduced-motion: no-preference)` 里的呼吸动画那条也以同一
  // 选择器开头（缩进 2 空格），只按选择器抓会先抓到它、抓出一段只有 animation 的规则。锚在行首只取
  // 缩进为 0 的那条主规则。
  const handsRule = /^html\[data-skin="anime"\] \[data-part="card-overlay"\] \{([^}]*)\}/m.exec(animeCSS)?.[1] || "";
  assert(
    /position: absolute/.test(handsRule) && /z-index: 3/.test(handsRule) &&
      /background-image: var\(--cast-hands-url\)/.test(handsRule) &&
      /background-size: auto 100%/.test(handsRule) && /height: var\(--cast-figure-h/.test(handsRule) &&
      /top: var\(--cast-fig-top/.test(handsRule) &&
      /background-position: top center/.test(handsRule) && /pointer-events: none/.test(handsRule),
    "双手层要绝对定位、z-index 3（压在看板上沿）、用 --cast-hands-url，且与人物层同一套几何（同一个 --cast-figure-h / --cast-fig-top / auto 100% / top center，手才不会从手腕上错开）",
  );
  const boardRule = /html\[data-skin="anime"\] \.node-card\[data-deco\] \.card-body \{([^}]*)\}/.exec(animeCSS)?.[1] || "";
  assert(
    /z-index: 2/.test(boardRule) && /border: 0/.test(boardRule) && /#fffdfb/.test(boardRule) &&
      /height: auto;/.test(boardRule) && /width: calc\(\(100% \+ 2px\) \* 0\.94\)/.test(boardRule),
    "看板是 HTML 自己画的（z-index 2 压住人物层；白底、无实线边框——2px 实线会横穿压在板沿上的手指；板宽 94% 卡宽、板高由数据决定）",
  );
  // 有立绘的卡壳左右内边距收成 0：看板本来就该通铺到卡边（94% 卡宽），不收的话它会被
  // card-overflow-check 判成越过内容盒。内容盒 = 卡宽 − 2px 边框，(100% + 2px) 才是卡宽。
  assert(
    /html\[data-skin="anime"\] \.node-card\[data-deco\] \{[^}]*padding-left: 0;[^}]*padding-right: 0;/s.test(animeCSS),
    "有立绘的卡壳左右内边距要收成 0：看板/立绘按卡宽比例通铺，内容盒才等于卡宽 − 2px 边框",
  );
  // 看板高度必须由数据决定：定高 + 板内滚动这一版被用户否掉了（「内容都没了，为啥做得这么小」）。
  // 只额外允许一条 min-height 兜底（--cast-board-min）：12 台里内容少的（8 台通用人物里就有）
  // 板面不会被数据撑矮 —— 立绘按常量缩放，板位与人物大小 12 台一致；内容更多时板面照旧往下长。
  assert(
    !/--cast-board-h/.test(animeCSS) && !/overflow-y: auto/.test(animeCSS) &&
      /min-height: var\(--cast-board-min, 457px\)/.test(boardRule) && /height: auto;/.test(boardRule),
    "看板不许定高、不许板内竖滚（板高跟数据走，全部数据一眼看全）；只留一条 min-height 兜底，保证 12 台板面等高、立绘缩放有统一基准",
  );
  const castBodyRule = /html\[data-skin="anime"\] \.node-card\[data-deco\] \.card-body > \[id\] \{([^}]*)\}/.exec(animeCSS)?.[1] || "";
  assert(
    /overflow: visible/.test(castBodyRule) && /min-height: 0/.test(castBodyRule),
    "板内数据区要 overflow: visible：全部数据排进板里、一眼看全，不裁也不滚",
  );
  assert(
    /html\[data-skin="anime"\] \[data-part="card-backdrop"\] \{[^}]*z-index: 1/s.test(animeCSS) &&
      /html\[data-skin="anime"\] \.node-card\[data-deco\]\[data-rolled="true"\] \.card-body > \[id\] > :not\(\.node-sub\) \{ visibility: hidden;/s.test(animeCSS),
    "人物层 z-index 1（看板 z-index 2 盖在上面）；收起态只藏板内数据（visibility: hidden 仍占位 → 板盒高度 = 展开态板高 → 两态卡高恒定；.card-top 留着，板沿与名字坐标都不动）",
  );
  // 展开态看板从牌子上沿一直铺到卡片底部：原图那块牌子与她的下半身整段被板盖住（用户 2026-10-10
  // 「展开的时候就不用显示下半身了」）。两态卡片下内边距都收成 0，看板下沿才贴着卡片底边；
  // 收起态也不能靠下内边距把卡撑高（那会让两态卡高差 20px）。
  assert(
    /html\[data-skin="anime"\] \.node-card\[data-deco\] \{[^}]*padding-bottom: 0;/s.test(animeCSS),
    "两态卡片下内边距都收成 0：看板下沿贴着卡片底边，收起态卡高才与展开态恒等",
  );
  // 收起态板条：板盒高度不变（数据仍占位），板面本体改由 ::before 按 --cast-plate-h 画一条矮板，
  // 板顶与展开态完全相同；板条以上露头 / 上身，以下露出**同一张图里**的裙摆 / 腿 / 鞋。
  assert(
    /html\[data-skin="anime"\] \.node-card\[data-deco\]\[data-rolled="true"\] \.card-body::before \{[\s\S]*?height: var\(--cast-plate-h,/s.test(animeCSS) &&
      /html\[data-skin="anime"\] \.node-card\[data-deco\]\[data-rolled="true"\] \.card-body \{[^}]*overflow: visible/s.test(animeCSS),
    "收起态：板盒高度不变，板条由 ::before 按 --cast-plate-h 画（正好盖住原图那块牌子），板条以下露出同一张图里的裙摆 / 腿 / 鞋",
  );
  // 名字固定在板面正中：两态同一个盒子（居中靠 flex），状态点与折叠箭头绝对定位在两端不挤它。
  const foldNameRule = /html\[data-skin="anime"\] \.card-top \.fold-name \{([^}]*)\}/.exec(animeCSS)?.[1] || "";
  assert(
    /justify-content: center/.test(foldNameRule) && /height: 28px/.test(foldNameRule) &&
      /flex: 1 1 100%/.test(foldNameRule),
    "名字行要在板面里水平居中（收起 / 展开同一个盒子、同一个坐标，折叠只影响后面的数据）",
  );
  assert(
    /html\[data-skin="anime"\] \.card-top \.fold-name \.node-dot \{[^}]*position: absolute/s.test(animeCSS) &&
      /html\[data-skin="anime"\] \.card-top \.fold-name \.fold-caret \{[^}]*position: absolute[^}]*right: 0/s.test(animeCSS),
    "状态点与折叠箭头要绝对定位在两端：它们不参与居中，名字的横向中心才是板面的中心",
  );
  // 每台机都要有双手层图址 + 逐台实测的牌子上/下沿占图高比例（--cast-top-frac / --cast-bot-frac）；
  // 立绘高 / 板顶 / 收起态板条高由一条几何规则按上沿比例算出（不再由面板脚本实测）。
  for (const id of ["jp-nosla-gen2", "de-nosla-fra", "us-vmiss-tri", "generic"]) {
    const block = new RegExp(`\\.node-card\\[data-deco="anime/${id}"\\] \\{([^}]*)\\}`).exec(animeCSS)?.[1] || "";
    assert(
      /--cast-hands-url: url\("anime\//.test(block) &&
        /--cast-top-frac: 0\.\d+/.test(block) && /--cast-bot-frac: 0\.\d+/.test(block),
      `${id}：要有双手层图址 + 实测的牌子上/下沿比例（--cast-top-frac / --cast-bot-frac）`,
    );
  }
  assert(
    /html\[data-skin="anime"\] \.node-card\[data-deco\] \{[^}]*--cast-figure-h: round\([^}]*--cast-board-top: round\([^}]*--cast-plate-h: 108px/s.test(animeCSS),
    "立绘高、板顶、收起态板条高由牌子上沿比例在 CSS 里算出",
  );
  assert(
    !/\.anime-roll\b/.test(animeCSS) && !/\.anime-roll\b/.test(appSource),
    "卷轴（.anime-roll、卷轴透明度滑块、牌面上的字）整条去掉：名字现在印在 HTML 看板正中的固定坐标上",
  );
  const blinkRule = /html\[data-skin="anime"\] \[data-part="card-backdrop"\] \[data-part="card-backdrop-layer"\] \{([^}]*)\}/
    .exec(animeCSS)?.[1] || "";
  assert(
    // 逐条查、不靠顺序：这条规则里属性顺序以后会变，正则串起来写会假失败。
    /bottom: 0/.test(blinkRule) &&
      /background-image: var\(--cast-blink-url\)/.test(blinkRule) &&
      /background-size: auto 100%/.test(blinkRule) &&
      !/mask-image/.test(blinkRule) &&
      /top: 0;/.test(blinkRule),
    "闭眼帧要和人物层同一张图同一套缩放（auto 100%、顶对齐居中）；盒子取人物层的高（inset: 0），整图切换，不许再有蒙版裁切",
  );
  assert(
    !/--cast-eye-/.test(animeCSS),
    "整图切换后不该再有眼睛带（--cast-eye-*）：裁切是「穿模 / 变形」的来源",
  );
  // 注释里正好写着「不放在 prefers-reduced-motion 里」，不剥注释会被当成命中（假失败）。
  const animeNoComments = animeCSS.replace(/\/\*[\s\S]*?\*\//g, "");
  assert(
    /html\[data-skin="anime"\] \[data-part="card-backdrop"\] \[data-part="card-backdrop-layer"\] \{[^}]*display: block[^}]*background-image: var\(--cast-blink-url\)/s.test(animeNoComments) &&
      /html\[data-skin="anime"\] \[data-part="card-backdrop"\] \[data-part="card-backdrop-layer"\] \{[^}]*animation: anime-blink var\(--cast-blink-dur, 5s\)/s.test(animeNoComments) &&
      !/prefers-reduced-motion[\s\S]{0,600}animation: anime-blink/.test(animeNoComments),
    "眨眼是纯透明度切换，不放进 prefers-reduced-motion —— 系统开了「减少动态效果」也该看得到她眨眼",
  );
  assert(
    /@media \(prefers-reduced-motion: no-preference\) \{[\s\S]*anime-breathe[\s\S]*\}/.test(animeNoComments),
    "会晃人的位移（呼吸 / 3D 倾斜）才听系统的：prefers-reduced-motion: no-preference 外不硬播",
  );
  // 裸眼 3D：指针在卡上时卡片转一点、人物 / 双手 / 下半身三层各自多走一点（层间差速）。
  assert(
    /\.node-card\[data-deco\]\.is-tilt \{[^}]*rotateX\(calc\(var\(--tilt-ny, 0\) \* -3deg\)\)[^}]*rotateY\(calc\(var\(--tilt-nx, 0\) \* 3\.4deg\)\)/s.test(animeCSS),
    "卡片要按指针转一点——层间差速就是裸眼 3D 的来源",
  );
  // 两个立绘层的位移写在同一条多选择器规则里（card-backdrop, card-overlay），
  // 选择器后面是逗号不是 `{`，所以抓这条规则整块（到第一个 `}`）再逐条查。
  const tiltShift = /\.node-card\[data-deco\]\.is-tilt \[data-part="card-backdrop"\],[\s\S]*?\n\}/.exec(animeCSS)?.[0] || "";
  assert(
    /translate3d\(calc\(var\(--tilt-nx, 0\) \* -9px\), calc\(var\(--tilt-ny, 0\) \* -6px\), 0\)/.test(tiltShift) &&
      /\.is-tilt \[data-part="card-overlay"\] \{/.test(tiltShift),
    "3D 倾斜时人物层 / 双手层要压在同一条位移规则里（手连着人，位移不同就会错位）",
  );
  assert(
    /addEventListener\(\s*"pointermove"/.test(appSource) && /setProperty\("--tilt-nx"/.test(appSource) &&
      /prefers-reduced-motion: reduce/.test(appSource),
    "3D 倾斜由 app.js 的指针委托写 --tilt-nx / --tilt-ny（无单位：CSS 里 deg 不能乘 px），且要听「减少动态效果」",
  );
  assert(
    // 飘带那一版被两次复核都当成「凭空浮着的贴纸」，整条去掉了。别再有人顺手加回来。
    !/anime-roll-ribbon/.test(animeCSS) && !/anime-roll-ribbon/.test(appSource),
    "卷轴不再挂书签飘带（复核说它像凭空浮着的贴纸）；元素、样式、动效三处都要干净",
  );
  assert(
    /html\[data-skin="anime"\] \.node-card\[data-deco="anime\/jp-nosla-gen2"\] \{[^}]*--cast-art-url: url\("anime\/jp-nosla-gen2\.webp"\)/s.test(animeCSS),
    "每台机的牌面配色里要带自己的人物层图址（人物层就是那半身像）",
  );
  // 点卡片空白处 / 数据行不许弹详情页：详情只由「详细」按钮进（app.js 里不再挂整卡 onclick，
  // 数据行也把 data-detail 摘了；列表视图的名字同理，改用同一枚「详细」按钮）。
  assert(
    !/c\.onclick = guard\(/.test(appSource) && !/if \(e\.target\.closest\("button, a, select, input, \[data-detail\]"\)\) return;/.test(appSource),
    "卡片不许再挂整卡 onclick 进详情（点空白区不该弹详情页）",
  );
  assert(
    !/data-detail="\$\{n\.id\}" data-focus-kind=/.test(appSource),
    "数据行（回程 / 延迟那一排）不再是 data-detail 按钮：点数据行不该弹详情页",
  );
  assert(
    /querySelectorAll\("\[data-focus-kind\]"\)/.test(appSource) &&
      /kind: cell\.dataset\.focusKind/.test(appSource),
    "延迟 / 回程格子要接回点击 → 只看那一格的聚焦视图（2026-10-10 曾整段丢掉，用户当场发现）",
  );
  assert(
    /class="node-name"/.test(appSource) && !/button class="node-name"[^>]*data-detail/.test(appSource),
    "列表视图的节点名不再导航（进详情只走「详细」按钮）",
  );
  assertSkinSheet(pressRules, "press");
  assertSkinSheet(shadcnRules, "shadcn");
  assertSkinSheet(animeRules, "anime");
  // 分段开关（用户说旧写法「有点怪怪的」）：选中那块填满自己那半格、实心版色红，不再自带描边。
  // 旧写法是红底 + 自己一圈墨边，与外框叠成双层描边、四周还露米色缝；后来用户又提了三条
  // ——选中块没铺满内高（「没填满」）、点击时黑线会动（:active 的盖章位移 + 线画在按钮上）、
  // 切换时右侧标签在临界点上左右跳（开关比列表里那 94px 的槽宽）。这四条都钉在这里。
  assert(
    /html\[data-skin="press"\] \.segmented \{[^}]*padding: 0[^}]*gap: 0/s.test(pressRules),
    "印刷皮肤的分段开关不留内边距与缝：选中的那半格要顶到外框",
  );
  assert(
    /html\[data-skin="press"\] \.segmented button\.active \{[^}]*background: var\(--accent\)[^}]*color: var\(--press-surface\)/s.test(pressRules),
    "选中块是实心版色红章 + 纸面字（浅色 / 夜印各自翻色）",
  );
  // 格间墨线是后一格左缘的内阴影：不占宽度、不随选中切换，三格（告警页子页）也落在真实分界上。
  // 画成容器正中的背景线只对两格对得上；画成某一格的边框则点一下线就跟着挪。
  assert(
    /\.segmented button \+ button,\s*html\[data-skin="press"\] \.segmented button \+ button\.active \{[^}]*box-shadow: inset 2px 0 0 var\(--glass-border\)/s.test(pressRules),
    "格间墨线要画成后一格左缘的内阴影（选中块也带上），不能是容器正中的背景线或某一格的边框",
  );
  assert(
    !/html\[data-skin="press"\] \.segmented \{[^}]*background-position: center/s.test(pressRules),
    "容器正中的背景线只对两格对得上，三格时会穿过中间那格的字",
  );
  assert(
    /\.segmented button \{[^}]*height: auto[^}]*align-self: stretch/s.test(pressRules),
    "两半要按格子内高铺满：写死高度会让选中块上下各露 1px 底色（「没填满」）",
  );
  assert(
    /\.segmented button:active \{[^}]*transform: none/s.test(pressRules),
    "控件里的按钮不跟着通用规则做 :active 位移，否则红块与墨线会跳",
  );
  assert(
    /\.segmented\.card-family button \{[^}]*padding: 0 8px/s.test(pressRules),
    "地址族开关要压在列表里那 94px 的槽宽以内，否则右边标签在临界点上左右跳",
  );
  assert(
    !/html\[data-skin="press"\] \.segmented button\.active \{[^}]*#[0-9a-f]{3,8}/i.test(pressRules),
    "选中片的底色 / 字色都走令牌，别写死色值——写死就没法跟着夜印翻",
  );
  // theme-boot.js 决定首屏那一刻的皮肤（在样式表之前执行），单独验一遍。
  const bootDOM = new JSDOM("<!doctype html><html><head></head><body></body></html>", {
    url: base + "/",
    runScripts: "outside-only",
  });
  bootDOM.window.localStorage.setItem("tza-skin", "press");
  bootDOM.window.localStorage.setItem("tza-theme", "light");
  // 和 index.html 一样先有 /theme-default.js 的清单：复古印刷等是主题包，不在清单里就认不出来。
  bootDOM.window.eval(await (await fetch(base + "/theme-default.js")).text());
  bootDOM.window.eval(await (await fetch(base + "/theme-boot.js")).text());
  assert.equal(bootDOM.window.document.documentElement.dataset.skin, "press", "首屏就要定好皮肤，不能等 app.js");
  assert.equal(bootDOM.window.document.documentElement.dataset.theme, "light", "明暗同样照旧");
  bootDOM.window.localStorage.setItem("tza-skin", "glass");
  bootDOM.window.eval(await (await fetch(base + "/theme-boot.js")).text());
  assert.equal(bootDOM.window.document.documentElement.dataset.skin, "glass", "第三张皮肤同样要在首屏定好");
  // 认不出来的值退回默认皮肤，别把 <html data-skin> 写成任意字符串。
  bootDOM.window.localStorage.setItem("tza-skin", "anime");
  bootDOM.window.eval(await (await fetch(base + "/theme-boot.js")).text());
  assert.equal(bootDOM.window.document.documentElement.dataset.skin, "anime", "第四张皮肤同样要在首屏定好");
  bootDOM.window.localStorage.setItem("tza-skin", "no-such-skin");
  bootDOM.window.eval(await (await fetch(base + "/theme-boot.js")).text());
  assert.equal(bootDOM.window.document.documentElement.dataset.skin, "shadcn", "无法识别的皮肤要退回默认（现代极简）");
}


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
    metrics: { counter_id: "ui-smoke", cpu: 1, memory_used: 2609623040, memory_total: 8333946880, disk_used: 12095188992, disk_total: 105467994112, net_in: 0, net_out: 0, rate_in: 0, rate_out: 0, uptime: 60, os: "smoke", distro: "Debian 13", arch: "amd64", cpu_cores: 4, version: "smoke", ip_families: ["ipv4"] },
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
    single.querySelector(".os-tag").getAttribute("aria-label"),
    "Debian 13",
    "发行版 Logo 的无障碍标签要保留完整版本",
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
// 点这两类格子要弹出「只看那一格」的聚焦视图：2026-10-10 撤掉整卡 onclick 时把它们一起弄哑了
// （用户点名「怎么点回程和延迟也不弹出来了」）。注意弹的是聚焦视图，不是含主机历史的综合详情。
{
  const dialog = () => publicDOM.window.document.querySelector("#dialog");
  latencyCell.click();
  await until(
    () => dialog()?.open && dialog().textContent.includes(`${latencyCell.dataset.focusCarrier} · TCP 延迟`),
    "点延迟格子弹出该运营商的聚焦视图",
  );
  assert(!dialog().querySelector("#charts"), "聚焦视图不该带主机历史（主机历史只在综合详情里）");
  dialog().close();
  routeCell.click();
  await until(
    () => dialog()?.open && dialog().textContent.includes(`${routeCell.dataset.focusCarrier} · 三网回程`),
    "点回程格子弹出该运营商的三网回程",
  );
  assert(!dialog().querySelector(".detail-stats"), "聚焦视图不摆综合详情那几格统计");
  dialog().close();
  // 用户点名的三条不变：点卡片空白处什么也不弹。
  findCard().querySelector(".card-body").click();
  assert(!dialog()?.open, "点卡片空白处不该弹详情");
  // 「详细」按钮照旧进综合详情（含主机历史）。
  findCard().querySelector("[data-detail]").click();
  await until(() => dialog()?.open && dialog().querySelector(".detail-stats"), "「详细」按钮进综合详情");
  dialog().close();
}
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
  // 线路带宽：后台填了才画，1000 Mbit/s 写
  // 「1 Gbit/s」；没填的机器整块不出现（不留「未标注」这类占位）。
  assert.equal(
    band.querySelector(".line-pill")?.textContent.trim(),
    "1 Gbit/s",
    `信息带要显示线路带宽（1000 Mbit/s → 1 Gbit/s），实际「${band.querySelector(".line-pill")?.textContent}」`,
  );
  assert(
    /线路带宽/.test(band.querySelector(".line-pill")?.getAttribute("title") || ""),
    "线路带宽的说明放在 title 里",
  );
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
  // 内存与磁盘按 1024 换算（gib），流量按十进制（bytes）——「100 GiB 的盘写成 105 GB」
  // 会让人以为算法算错了（用户就是这么问的），而商家规格与 df -h / free -h 都是 GiB。
  const metricSub = (label) =>
    [...compact.querySelectorAll(".metrics-grid .metric")]
      .find((x) => x.querySelector("span")?.textContent === label)
      ?.querySelector("small")?.textContent;
  assert(
    metricSub("内存") === `${gib(plannedNode.metrics.memory_used)} / ${gib(plannedNode.metrics.memory_total)}` &&
      metricSub("磁盘") === `${gib(plannedNode.metrics.disk_used)} / ${gib(plannedNode.metrics.disk_total)}`,
    `内存与磁盘要按 1024 换算，实际「${metricSub("内存")}」「${metricSub("磁盘")}」`,
  );
  assert(
    !/GiB/.test(metricSub("周期用量") || ""),
    `流量那一格不该跟着改成 1024（只有内存 / 磁盘是 GiB），实际「${metricSub("周期用量")}」`,
  );
  // 指标格的两列都必须写成 minmax(0, 1fr)：`1fr` 的最小值是内容的 min-content，而降速说明
  // （「近 24 小时均值 … · 阈值 …」）是一整句话，会把那一列撑到比容器还宽、整格顶出卡片，
  // 卡片 overflow: hidden 就把右对齐的数值切掉（用户 2026-10-08 截图报过「边界溢出了」）。
  // jsdom 不做布局，只有样式文本断言拦得住；真布局由 scripts/card-overflow-check.mjs 在
  // 部署验收里按 320–414px 逐张卡片量。
  assert(
    /\.metrics-grid \{[^}]*grid-template-columns: minmax\(0, 1fr\) minmax\(0, 1fr\)/.test(styles),
    "指标格两列要 minmax(0, 1fr)：1fr 的最小值是内容宽度，长说明会把整格顶出卡片",
  );
  assert(
    /\.metric small\.throttle-note \{[^}]*white-space: normal/.test(styles),
    "降速说明是整句话，要允许折行（截成省略号就把阈值/额度那半句丢了）",
  );
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
    /\.card-stats \.unit,\s*\n\.card-stats \.speed-line \.unit-none,\s*\n\.card-stats \.stat-item,\s*\n\.card-stats \.line-pill,\s*\n\.card-stats \.price-label \{[^}]*font-size: 10px[^}]*color: var\(--muted\)/.test(styles),
    "信息带里的说明文字（单位、累计、价格）要同一档字号与颜色",
  );
  assert(
    [...band.querySelectorAll(".stat-item .unit")].length === 2,
    "累计流量的单位也要与速率的「/s」同样弱化（10px 灰字，不是跟着数字加粗）",
  );
  // 回程三格等宽（用户点名「要这种等比例的」），每格「运营商标志 + 回程值」一行读完——
  // 标志是各家的官方图形（内联 SVG，web/carrier-logos.js），格子里不写运营商名；
  // 名字不能真丢：aria-label / title / data-focus-carrier 里必须有。
  const chip = compact.querySelector(".route-label");
  assert.equal(chip.children[0].tagName, "SPAN", "回程格第一项要是代表运营商的标志");
  assert(
    /carrier-mark ct|carrier-mark cu|carrier-mark cm/.test(chip.children[0].className),
    `回程标志要带运营商配色类，实际「${chip.children[0].className}」`,
  );
  const mark = chip.children[0];
  assert(
    mark.querySelector("svg path[fill]") && mark.querySelectorAll("svg").length === 1,
    "回程标志是一枚内联 SVG，颜色写在 path 的 fill 上",
  );
  // 同一份标志在一屏上重复几十次：带 id 就是重复 id；带 style 会被面板的 CSP（style-src 'self'）
  // 整条丢掉，尺寸只能走样式表。
  assert(
    !mark.querySelector("[id]") && !mark.querySelector("[style]"),
    "回程标志不能带 id / style（重复 id 与 CSP 会丢样式）",
  );
  assert(
    mark.getAttribute("aria-hidden") === "true",
    "标志是装饰，要 aria-hidden（运营商名在 aria-label 里）",
  );
  assert(
    chip.dataset.focusCarrier && chip.getAttribute("aria-label")?.includes(chip.dataset.focusCarrier),
    "格子里不写运营商名，就得在 aria-label / data-focus-carrier 里留着（筛选与详情按它走）",
  );
  assert(
    /\.route-label \{[^}]*flex-wrap: wrap/.test(styles) &&
      /\.route-label \{[^}]*align-items: center/.test(styles) &&
      // 标志不是文字，没有基线可言；值与标志在同一行居中
      !/\.route-label \{[^}]*align-items: baseline/.test(styles) &&
      /\.route-label \{[^}]*justify-content: center/.test(styles),
    "回程格是标志 + 值一行居中；更长的值整段落到第二行（不断在箭头上）",
  );
  assert(
    /\.route-grid \{[^}]*grid-template-columns: repeat\(3, minmax\(0, 1fr\)\)/.test(styles),
    "回程三格要等宽（不按内容定宽）",
  );
  assert(
    /\.carrier-mark \{[^}]*width: 11px[^}]*height: 11px/s.test(styles) &&
      /\.carrier-mark svg \{[^}]*width: 100%[^}]*height: 100%/s.test(styles),
    "标志定一个 11px 方框、svg 撑满方框（图形按默认的 preserveAspectRatio 居中留白）",
  );
  assert(
    /\.route-label \{[^}]*padding: 8px 2px/.test(styles) &&
      /\.route-label strong \{[^}]*font-size: 11px/.test(styles),
    "回程格内边距 2px、值 11px —— 标志比圆点多占 4px，内边距从 4px 收到 2px 才把「10099 → 9929」留在同一行",
  );
  assert(
    /\.carrier-dot \{[^}]*width: 7px[^}]*height: 7px/s.test(styles) &&
      /\.carrier-dot\.cx \{[^}]*var\(--subtle\)/.test(styles),
    "认不出运营商的回程值（cx）退回那颗 7px 灰点",
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
// 计费格里也要写线路带宽（与卡片共用 billingInner 这一份渲染）。
assert.equal(
  listRow.querySelector(".billing-cell .line-pill")?.textContent.trim(),
  "1 Gbit/s",
  `列表的计费格同样要显示线路带宽，实际「${listRow.querySelector(".billing-cell .line-pill")?.textContent}」`,
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
  identityCell.querySelector(".node-name").textContent.slice(identityCell.querySelector(".node-flag").textContent.length),
  name,
  "节点名前显示国旗，名称要给全，不做省略",
);
// 延迟与回程在列表视图里是行内文本（卡片那格是方形格子），每家运营商一行。
assert.equal(listRow.querySelectorAll(".net-cell .net-line").length, 4, "延迟四行：三网各一行 + 新加坡的自定义目标一行");
{
  // 海外行多一个定宽名字槽；同一格里三网行留同宽空槽，数值列照样对齐。
  const names = [...listRow.querySelectorAll(".net-cell .net-line .net-name")].map((x) => x.textContent);
  assert.deepEqual(names, ["", "", "", "新加坡"], "名字槽：三网空着，海外行写目标名");
}
assert.equal(listRow.querySelectorAll(".route-cell .route-line").length, 3, "回程三行");
for (const line of listRow.querySelectorAll(".net-line")) {
  assert(line.dataset.focusKind === "latency" || line.dataset.focusKind === "route", "行内文本要带聚焦信息");
  assert(line.dataset.focusCarrier, "行内文本要带运营商");
  assert(line.getAttribute("title"), "行内文本要有各自的探测时间提示");
}
// 回程那三行的第一项跟卡片一致：官方标志，不再写运营商名（延迟三行仍是运营商名——
// 卡片里的延迟块也是文字，两边一致）。名字不能丢：标志在 aria-label / data-focus-carrier 里。
for (const line of listRow.querySelectorAll(".route-cell .route-line")) {
  const mark = line.querySelector(".carrier-name .carrier-mark");
  assert(mark && mark.querySelector("svg path[fill]"), "列表里的回程行第一项要是运营商标志");
  assert(mark.getAttribute("aria-hidden") === "true", "列表里的标志同样是装饰");
  assert(!mark.querySelector("[id]") && !mark.querySelector("[style]"), "列表里的标志也不能带 id / style");
  assert(
    line.getAttribute("aria-label") === `${line.dataset.focusCarrier} ${line.querySelector("strong").textContent}`,
    `列表里不写运营商名就得在 aria-label 里留着，实际「${line.getAttribute("aria-label")}」`,
  );
}
// 延迟三行也换成官方标志了（用户点名）：与同一行右边的回程行、与卡片里的延迟块口径一致。
// 名字同样不能丢——aria-label / data-focus-carrier 里都在。
for (const line of listRow.querySelectorAll('.net-cell .net-line:not([data-focus-carrier="其他"])')) {
  const mark = line.querySelector(".carrier-name .carrier-mark");
  assert(mark && mark.querySelector("svg path[fill]"), "列表里的延迟行第一项要是运营商标志");
  assert(mark.getAttribute("aria-hidden") === "true", "列表里的标志同样是装饰");
  assert(!mark.querySelector("[id]") && !mark.querySelector("[style]"), "列表里的标志也不能带 id / style");
  assert(
    line.getAttribute("aria-label")?.startsWith(line.dataset.focusCarrier),
    `列表里不写运营商名就得在 aria-label 里留着，实际「${line.getAttribute("aria-label")}」`,
  );
}
// 运营商槽只给标志的宽度：槽定宽是为了「其他」那种没有标志的行（退回 7px 灰点）仍与上下行
// 对齐，但 30px 是「写运营商名」时代的宽度——名字换成 11px 标志后，标志与数值之间白挂着一
// 大段空白（用户标红框点名「空得有点多」）。卡片那处槽宽在 .band-head 的栅格里，上面已断。
assert(
  /\.node-grid\.node-list \.net-line \.carrier-name \{[^}]*flex: 0 0 11px/.test(styles),
  "列表里运营商槽只有标志的宽度（11px），别把写名字用的 30px 槽留给标志",
);
// 色带格数两个视图统一到 10 格（用户点名：列表看齐卡片）。列表原来画 20 格、卡片 10 格。
assert(
  [...listRow.querySelectorAll(".net-cell .net-line .quality-bars")].every(
    (b) => b.querySelectorAll(".quality-bar").length === 10,
  ),
  "列表里的延迟色带也要画 10 格（与卡片一致）",
);
assert(
  [...listRow.querySelectorAll(".net-cell .net-line .quality-bars")].every((b) =>
    /最近 10 次/.test(b.getAttribute("title")),
  ),
  "列表里的色带要写明最近 10 次",
);
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
// 观感（用户点名两处）：延迟色带要够大（9px 高、吃掉格子剩余空间），实时网速竖排成两行。
// 色带原来是钉死 120px 的；槽收窄后那 19px 会挂在「丢包」与色带之间（用户标红框说「空得有点多」），
// 所以改成 flex: 1 1 120px 让它长过去——四行同格，宽度仍然一致。
assert(
  /\.node-grid\.node-list \.net-line \.quality-bars \{[^}]*flex: 1 1 120px/.test(styles),
  "列表里的延迟色带要吃掉剩余空间（不再钉死 120px，否则丢包与色带之间留一段空档）",
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
// 网速行是「图标靠左、数值靠右」，而网速那格必须**定宽 96px**（不是 fr）：
// 格子越宽，多出来的空白就越落在图标与数值之间（用户标红框「这里也是」，119px 时空 42px）。
// 96px 是量出来的下限 94px 加 2px 余量——正好装下最长的速率串「12.3 MB/s」（53.7px）。
// 两条都是纯排版，jsdom 不算布局，只能断言样式文本。
assert(
  /\.node-grid\.node-list \.speed-line \{[^}]*justify-content: space-between/.test(styles),
  "网速行要图标靠左、数值靠右",
);
assert(
  /--row-cols:[^;]*minmax\(96px, 96px\)/.test(styles),
  "网速那格要定宽 96px（写成 minmax(96px,96px)）——给 fr 会被撑到 119px，图标与数值之间又空 42px",
);

// 架构（Agent 报的 GOARCH）要标在系统标签下面；卡片视图不动，两边都出这个标签。
assert(
  /class="arch-tag"/.test(appSource) && /m\?\.arch/.test(appSource),
  "要用 Agent 上报的架构渲染 arch-tag",
);
assert(
  /distroBadge\(n\)/.test(appSource),
  "系统标签使用本地发行版 Logo，版本保留在无障碍标签与提示里",
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
  /@media \(max-width: 1336px\)[\s\S]*?\.node-grid\.node-list \{[^}]*overflow-x: auto/.test(styles),
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
// A real authenticated heartbeat must change the live card without inventing
// metric values; a subsequent valid report clears the diagnostic via null.
publicSearch.value = name;
publicSearch.dispatchEvent(new publicDOM.window.Event("input"));
await until(() => findCard(), "diagnostic test card");
const healthReply = await fetch(base + "/api/v1/agent/heartbeat", {
  method: "POST", headers: { Authorization: "Bearer " + nodeToken, "Content-Type": "application/json" },
  body: JSON.stringify({ protocol: 1, status: { state: "collect_failed", reason: "disk", os: "smoke", arch: "amd64", version: "smoke" } }),
});
assert.equal(healthReply.status, 204);
await until(() => findCard()?.querySelector(".collection-state")?.hidden === false, "live collection diagnostic");
assert.match(findCard().querySelector(".collection-state").title, /磁盘采集失败/);
assert.match(findCard().querySelector(".metrics-grid").textContent, /指标不可用/);
assert(!findCard().querySelector(".metrics-grid").textContent.includes("99%"));
const recoveryReply = await fetch(base + "/api/v1/agent/report", {
  method: "POST", headers: { Authorization: "Bearer " + nodeToken, "Content-Type": "application/json" },
  body: JSON.stringify({ protocol: 1, metrics: { cpu: 23, memory_used: 10, memory_total: 100, disk_used: 20, disk_total: 100, net_in: 0, net_out: 0, uptime: 60, os: "smoke", arch: "amd64", ip_families: ["ipv4"] } }),
});
assert.equal(recoveryReply.status, 204);
await until(() => findCard()?.querySelector(".collection-state")?.hidden === true && findCard().querySelector(".metrics-grid").textContent.includes("23"), "live metric recovery");
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
// 勾了免费（free）要写「免费」而不是「未设置价格」。
const longName = "长期免费验收 " + Date.now();
const madeLong = await fetch(base + "/api/v1/admin/nodes", {
  method: "POST",
  headers: { ...adminHeaders, "Content-Type": "application/json" },
  body: JSON.stringify({
    name: longName,
    region: "香港",
    public: true,
    billing: { currency: "CNY", free: true, reset_at: "2026-09-01T00:00:00Z", traffic_mode: "max", expires_at: "2226-07-04T00:00:00Z" },
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
    "勾了免费就是「免费」",
  );
  // 这台机器没填线路带宽：信息带里不许出现这一项（也不许留一个空占位）。
  assert(!long.querySelector(".line-pill"), "没填线路带宽的机器不显示这一项");
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
  // 卡片头恒两行：名字独占第一行、右边那排徽标整组落到第二行并靠左。名字的长短不再改变
  // 这个形态（实测同宽下：名字短的 24px、名字长的 58px，卡片头不齐），所以名字行自己
  // 换行、徽标组不许在内部再折一次。样式文本断言盯住这两条。
  assert(
    /\.card-top \{[^}]*flex-wrap: wrap/.test(styles),
    "卡片名字行要能换行（名字独占一行、徽标整组落到第二行）",
  );
  assert(
    /\.card-top \.node-name \{[^}]*white-space: nowrap/.test(styles) &&
      /\.card-top \.node-identity \{[^}]*flex: 1 1 100%/.test(styles) &&
      /\.card-top \.card-actions \{[^}]*justify-content: flex-start/.test(styles) &&
      /\.card-top \{[^}]*flex-wrap: wrap/.test(styles),
    "名字整段留在第一行、徽标组在第二行靠左；窄屏允许换行，不再顶出卡片边框",
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
    billing: { reset_at: "2026-09-01T00:00:00Z", traffic_mode: "sum" },
    line: { mbps: 1000, throttle_mbps: 1, throttle_hours: 1 },
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
  // 这一格只在 10 秒一轮的全量刷新里更新：每秒的轻刷只补速率与指标格（累计/额度那一带不动），
  // 所以窗口必须比一个刷新周期长——写 8 秒时，验收恰好落在两轮之间就会读到上一份数据，
  // 在 CI 上表现为随机红（同一提交重跑就绿）。给两个周期，别用「等它收敛」这类模糊话掩盖。
  const totals = () =>
    [...throttleCard().querySelectorAll(".card-stats .stat-totals .stat-item")].map((t) =>
      t.textContent.trim(),
    );
  const wantTotals = [`累计 ${bytes(throttleAfter.net_out)}`, `累计 ${bytes(throttleAfter.net_in)}`];
  await until(
    () => JSON.stringify(totals()) === JSON.stringify(wantTotals),
    `累计要给上下行两张（上行在上）、按网卡计数换算单位，实际「${totals().join(" / ")}」`,
    25000,
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
    billing: { reset_at: "2026-09-01T00:00:00Z", traffic_limit: 1000000, traffic_mode: "max" },
    line: { mbps: 4000, throttle_mbps: 50, throttle_on: "quota" },
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
  assert.equal(stored?.line.throttle_on, "quota", `创建接口要落库 line.throttle_on，实际 ${JSON.stringify(stored?.line)}`);
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
  assert.equal(view.line.throttle_on, "quota", "公开接口要给出降速触发条件");
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
unlockCard().querySelector("[data-detail]").click();
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
