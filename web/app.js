import { api, send, esc, toast, modal, guard } from "./api.js";
import { bytes, bandwidth, date, days, gib, mbps, percent, statusLabel, statusName, diagnosticHint, remainingValueLabel, remainingValueHint } from "./format.js";
import { detail } from "./detail.js";
import { applyTheme, themeButton } from "./theme.js";
import { applySkin, skinButton, skinLayout } from "./skins.js";
import { searchMatch } from "./search.js";
import { carrierMark } from "./carrier-logos.js";
import { distroBadge, regionFlag, offlineBadge, offlineTitle } from "./node-identity.js";
import { unlockBadge, unlockCatalog, unlockPanel } from "./unlock.js";
import { decorationVars, pickDecoration } from "./decorations.js";
import { brandHTML } from "./brand.js";
import "./ui-select.js";

const app = document.querySelector("#app");
const adminPage = location.pathname.startsWith("/admin");
let nodes = [],
  cardFamilies = new Map(),
  query = "",
  group = "",
  observations = new Map(),
  latencySamples = [],
  catalog = null,
  generation = 0;

// 展示方式：card = 每台主机一张卡（默认），list = 一行一台、字段横向对齐。
// 只存在浏览器本地，刷新与换页保持；访客没选过就用当前主题声明的 layout.default_view，
// 也没声明（或存不进去，隐私模式等）就是卡片视图。
const VIEW_KEY = "tza-view";
function storedView() {
  try {
    const v = localStorage.getItem(VIEW_KEY);
    return v === "list" || v === "card" ? v : null;
  } catch {
    return null;
  }
}
const skinView = () => (skinLayout().default_view === "list" ? "list" : "card");
let view = storedView() || skinView();

// 延迟分栏：domestic = 三网（电信 / 联通 / 移动），intl = 海外预设与自定义目标。
// 访客的选择全局生效——同一屏的卡片与列表一起翻，方便横向比较同一栏的延迟——并存在浏览器
// 本地，刷新与换页保持；没选过（或存不进去，隐私模式等）默认国内。
const LATENCY_SCOPE_KEY = "tza-latency-scope";
function storedLatencyScope() {
  try {
    return localStorage.getItem(LATENCY_SCOPE_KEY) === "intl" ? "intl" : "domestic";
  } catch {
    return "domestic";
  }
}
let latencyScope = storedLatencyScope();
function setLatencyScope(next) {
  if ((next !== "domestic" && next !== "intl") || next === latencyScope) return;
  latencyScope = next;
  try {
    localStorage.setItem(LATENCY_SCOPE_KEY, latencyScope);
  } catch {
    /* 隐私模式写不进去，忽略：这一轮的选择仍在内存里 */
  }
  renderCards();
}

// 滚动页面时不要顺手改掉已填好的值：数字、日期时间与下拉在被滚动时先失焦
// （失焦后浏览器不会再按滚轮改值），页面滚动不受影响。
const WHEEL_SENSITIVE =
  'input[type="number"],input[type="date"],input[type="time"],input[type="datetime-local"],input[type="month"],input[type="week"],select';
function noWheelStepping() {
  document.addEventListener(
    "wheel",
    (e) => {
      const el = e.target instanceof Element ? e.target.closest(WHEEL_SENSITIVE) : null;
      if (el) el.blur();
    },
    { passive: true },
  );
}

// 后台模块只在 /admin 才下载：公开页的访客用不到它们。依赖一次性声明成 modulepreload，
// 并行下载，不等 admin.js 解析完再一层层发现。
const ADMIN_MODULES = ["admin.js", "admin-state.js", "admin-nodes.js", "admin-tasks.js", "admin-alerts.js", "admin-themes.js", "admin-plugins.js", "admin-market.js", "admin-settings.js", "task-controls.js", "task-editor.js", "agent-setup.js", "regions.js", "region-picker.js"];
function loadAdmin() {
  for (const name of ADMIN_MODULES) {
    const link = document.createElement("link");
    link.rel = "modulepreload";
    link.href = "/" + name;
    document.head.append(link);
  }
  return import("./admin.js");
}

async function start() {
  applyTheme();
  applySkin();
  noWheelStepping();
  if (adminPage) {
    const { mountAdmin } = await loadAdmin();
    await mountAdmin(app);
    return;
  }
  renderShell();
  await refresh();
  // 数值类数据（负载、实时速率、用量）约 1 秒一笔，所以每秒轻刷一次——只换数字，不重建卡片；
  // 探测明细（每个目标的观测与最近样本）仍是 10 秒一轮，那一轮要打十几个请求。
  setInterval(() => {
    if (!document.hidden) liveTick().catch(() => {});
  }, 1000);
  setInterval(() => {
    if (!document.hidden) refresh().catch((e) => toast(e.message));
  }, 10000);
}

// 展示方式开关：卡片 / 列表。两个视图用同一份取数（见 cardInner），切换只换渲染函数。
function viewSwitch() {
  const button = (v, label) =>
    `<button type="button" data-view="${v}" class="${view === v ? "active" : ""}" aria-pressed="${view === v}">${label}</button>`;
  return `<div class="segmented view-switch" role="group" aria-label="展示方式">${button("card", "卡片")}${button("list", "列表")}</div>`;
}

function renderShell() {
  app.innerHTML = `<header data-part="public-header" class="public-header">${brandHTML()}<nav><span class="live-label"><i></i><span class="live-text">LIVE STATUS</span></span>${skinButton()}${themeButton()}<a href="/admin" class="button quiet admin-link" aria-label="管理控制台"><span class="admin-link-full">管理控制台</span><span class="admin-link-short">后台</span> ↗</a></nav></header><main class="public-main"><section class="hero"><div data-part="hero-meta" class="hero-meta"><span>最近同步</span><strong id="updated">连接中</strong><small>每秒刷新</small></div></section><section id="overview" data-part="overview" class="overview"></section><section data-part="toolbar" class="toolbar"><div class="filter-controls"><div class="search"><span>⌕</span><input id="search" aria-label="搜索节点" placeholder="搜索名称 / 地区 / 分组 / 商家 / 拼音"></div><select id="group-filter" aria-label="节点分组"><option value="">全部分组</option></select></div><div class="view-bar">${viewSwitch()}</div></section><div id="nodes" data-part="node-grid" class="node-grid"></div><footer><span>独立 · 轻量 · 可控</span><span>TZA PROBE · V2</span></footer></main>`;

  document.querySelector("#search").oninput = (e) => {
    query = e.target.value;
    renderCards();
  };
  document.querySelector("#group-filter").onchange = (e) => {
    group = e.target.value;
    renderCards();
  };
  for (const b of document.querySelectorAll("[data-view]"))
    b.onclick = () => setView(b.dataset.view);
  applyView();
}

// 切展示方式：记住选择、换容器的类，然后整块重绘（两个视图的行结构不同，逐张补丁没有意义）。
function setView(next) {
  if (next !== "card" && next !== "list") return;
  if (next === view) return;
  view = next;
  try {
    localStorage.setItem(VIEW_KEY, view);
  } catch {
    // 存不下就只影响下次打开的默认值，本轮照常切换
  }
  for (const b of document.querySelectorAll("[data-view]")) {
    const on = b.dataset.view === view;
    b.classList.toggle("active", on);
    b.setAttribute("aria-pressed", String(on));
  }
  applyView();
  renderCards();
}

function applyView() {
  const list = document.querySelector("#nodes");
  if (!list) return;
  list.classList.toggle("node-list", view === "list");
  list.onscroll = markListOverflow;
  markListOverflow();
}

// 列表视图在 1336px 以下整块横向滚动（styles.css 的媒体查询），右缘被切掉的那一格（计费）
// 原来没有任何提示，看着像排版坏了。右边还有内容时挂 .more-right，CSS 据此在右缘渐隐。
function markListOverflow() {
  const list = document.querySelector("#nodes");
  if (!list) return;
  const more = list.classList.contains("node-list") && list.scrollLeft + list.clientWidth < list.scrollWidth - 1;
  list.classList.toggle("more-right", more);
}
addEventListener("resize", markListOverflow);

async function refresh() {
  const ticket = ++generation;
  // 解锁目录与节点列表互不依赖，一起发，省掉首屏一次往返（目录取过一次就缓存）。
  const catalogTicket = unlockCatalog();
  const next = await api("/nodes");
  if (ticket !== generation) return;
  nodes = next;
  const groups = [...new Set(nodes.map((n) => n.group).filter(Boolean))];
  if (group && !groups.includes(group)) group = "";
  setHTML(document.querySelector("#group-filter"),
    `<option value="">全部分组</option>` +
      groups
        .map((g) => `<option ${g === group ? "selected" : ""}>${esc(g)}</option>`)
        .join(""));
  setText(document.querySelector("#updated"),
    new Date().toLocaleTimeString("zh-CN", { hour12: false }));

  setHTML(document.querySelector("#overview"), overviewInner());

  // 解锁目录是全站固定的一份（名称 / 分类 / 是否只报地区），先取到再画卡片：徽标上的分数
  // 来自节点自己的 unlock 字段，分类浮层还要目录才认得出每一项的名字与分类。
  catalog = await catalogTicket;

  renderCards();

  // 卡片上每个小格子是一次探测结果，所以除了逐目标的最新观测，还要取最近若干次原始结果。
  const sampleTicket = api("/latency-samples?limit=20")
    .then((data) => { if (ticket === generation) latencySamples = data.samples || []; })
    .catch(() => { if (ticket === generation) latencySamples = []; });
  // 所有节点的观测一次取回：以前每台节点一个请求，慢的是一轮轮往返而不是服务端。
  // 卡片只画回程标签，批量接口本身就不带跳表（那部分占了九成体积）。
  try {
    const all = await api("/observations");
    if (ticket === generation) {
      observations.clear();
      for (const n of nodes) if (all[n.id]) observations.set(n.id, all[n.id]);
    }
  } catch {
    if (ticket === generation) observations.clear();
  }
  await sampleTicket;
  if (ticket === generation) renderCards();
}

function meter(label, value, sub = "") {
  return `<div data-part="metric" class="metric"><div><span>${label}</span><strong>${value == null ? "—" : value.toFixed(1) + "%"}</strong></div><progress max="100" value="${value ?? 0}" aria-label="${label}"></progress><small>${esc(sub)}</small></div>`;
}

// 顶部四个数字与卡片上的「指标 / 实时速率」两块抽成函数：每秒的轻刷新只换这两处的 HTML，
// 整卡重建留给 10 秒一轮的探测明细（那一轮要打十几个请求，也会整块换 DOM）。
// 上传 / 下载的顺序全站统一为「上传在前」，跟卡片里「上行在上」一致（用户点名的顺序）。
function overviewInner() {
  const online = nodes.filter((n) => n.online),
    traffic = nodes.reduce((v, n) => v + n.traffic_used, 0),
    rateIn = online.reduce((v, n) => v + (n.metrics?.rate_in || 0), 0),
    rateOut = online.reduce((v, n) => v + (n.metrics?.rate_out || 0), 0),
    hasLiveMetrics = !online.length || online.some((n) => n.metrics);
  return `<div data-part="overview-tile" class="overview-tile"><div class="tile-header"><span>在线节点</span><i class="dot ${online.length ? "active" : ""}"></i></div><strong>${online.length}<small> / ${nodes.length}</small></strong></div><div data-part="overview-tile" class="overview-tile"><div class="tile-header"><span>实时上传</span><span class="tile-badge orange-badge">↑</span></div><strong>${hasLiveMetrics ? bytes(rateOut) : "—"}<small>/s</small></strong></div><div data-part="overview-tile" class="overview-tile"><div class="tile-header"><span>实时下载</span><span class="tile-badge teal-badge">↓</span></div><strong>${hasLiveMetrics ? bytes(rateIn) : "—"}<small>/s</small></strong></div><div data-part="overview-tile" class="overview-tile"><div class="tile-header"><span>本周期用量合计</span><span class="tile-badge purple-badge">⚡</span></div><strong>${bytes(traffic)}</strong></div>`;
}

// 小卡片只保留「窗口均值 / 阈值」；完整单位和规则放在 title 与无障碍标签中。
const compactBitrate = (bits) => mbps(bits).replace(/^([\d.]+) (Mbit|kbit|bit)\/s$/, (_, value, unit) => `${Number(value)}${{ Mbit: "M", kbit: "k", bit: "b" }[unit]}`);
function throttleNoteHTML(n) {
  if (!(n.line.throttle_mbps > 0)) return "";
  const limit = compactBitrate(n.line.throttle_mbps * 1e6);
  let label, value, description;
  if (n.line.throttle_on === "quota") {
    label = n.throttled ? "已限速" : "满额限";
    value = limit;
    description = `${n.throttled ? "已用满额度" : `用满 ${bytes(n.billing.traffic_limit)} 额度`} · 限速 ${n.line.throttle_mbps} Mbit/s`;
  } else {
    const hours = n.covered_hours > 0 ? Math.min(n.covered_hours, n.window_hours || 24) : n.window_hours || 24;
    label = n.covered_hours > 0 ? `${hours}h均` : n.last_seen ? `${hours}h统计中` : "待接入";
    value = `${n.covered_hours > 0 ? compactBitrate(n.avg_bps) + " " : ""}/ ${limit}`;
    description = `${n.covered_hours > 0 ? `近 ${hours} 小时均值 ${mbps(n.avg_bps)}` : n.last_seen ? `近 ${hours} 小时均值统计中` : "接入后开始统计均值"} · 阈值 ${n.line.throttle_mbps} Mbit/s`;
  }
  return `<small class="throttle-note${n.throttled ? " over" : ""}" title="${esc(description)}" aria-label="${esc(description)}"><span>${esc(label)}</span> <span class="traffic-value">${esc(value)}</span></small>`;
}

function metricsInner(n) {
  const m = n.metrics,
    hasMetrics = !!m,
    // CPU 与内存是瞬时值：离线后最后一次上报的数字不再代表现在，照写会像实时数据（实时速率
    // 离线时早就是「—」）。磁盘是存量，离线时仍按最后一次上报显示。
    live = hasMetrics && n.online;
  // CPU 那格的副标给核心数（Agent 报的 runtime.NumCPU），老 Agent 没报就退回原来的说明文字。
  const cpuSub = !hasMetrics ? (n.metrics_stale ? "指标不可用" : "等待采集") : !n.online ? "离线" : m.cpu_cores ? `${m.cpu_cores} 核` : "系统计算负载";
  // 内存与磁盘按 1024 换算（gib）：这两个量商家按 GiB 卖、`df -h` / `free -h` 也给 GiB；
  // 速率与流量仍按十进制（bytes），与商家的流量口径一致。
  const memSub = !hasMetrics ? (n.metrics_stale ? "指标不可用" : "未报告") : !n.online ? "离线" : `${gib(m.memory_used)} / ${gib(m.memory_total)}`;
  const diskSub = hasMetrics ? `${gib(m.disk_used)} / ${gib(m.disk_total)}` : n.metrics_stale ? "指标不可用" : "未报告";
  const mode = { sum: "双向", max: "取峰值", in: "仅下载", out: "仅上传" }[n.billing.traffic_mode];
  const trafficSub = n.billing.traffic_limit ? [bytes(n.billing.traffic_limit), mode].filter(Boolean).join(" · ") : "不限";
  return `${meter("CPU", live ? m.cpu : null, cpuSub)}${meter("内存", live ? percent(m.memory_used, m.memory_total) : null, memSub)}${meter("磁盘", hasMetrics ? percent(m.disk_used, m.disk_total) : null, diskSub)}<div data-part="metric" class="metric"><div><span>周期用量</span><strong>${bytes(n.traffic_used)}</strong></div><progress max="100" value="${percent(n.traffic_used, n.billing.traffic_limit)}" aria-label="周期流量额度"></progress><div class="traffic-meta"><small class="traffic-quota"><span>额度</span> <span class="traffic-value">${esc(trafficSub)}</span></small>${bandwidthLine(n)}${throttleNoteHTML(n)}</div></div>`;
}

// 实时速率：两个视图都是「一行一个方向」（小图标 + 数值靠右，标签收进 title）。
// 顺序是**上行在上、下行在下**（用户 2026-10-01 点名「上下行换下位置，上行在上面」）；
// 列表视图共用这份 HTML，所以两处一起换。卡片里原先两个大方块（图标 + 两行文字 + 内边距）单独占 59px。
function speedsInner(n) {
  const m = n.metrics,
    isLive = n.online && m;
  const rateIn = isLive ? `${bytes(m.rate_in)}<span class="unit">/s</span>` : '<span class="unit-none">—</span>';
  const rateOut = isLive ? `${bytes(m.rate_out)}<span class="unit">/s</span>` : '<span class="unit-none">—</span>';
  return `<div class="speed-line" title="实时上传"><span class="speed-icon orange-icon">↑</span><strong>${rateOut}</strong></div><div class="speed-line" title="实时下载"><span class="speed-icon teal-icon">↓</span><strong>${rateIn}</strong></div>`;
}

// 发行版只显示本地 Logo；名称和完整版本保留在 title / aria-label。架构独立显示。
function osTagsContent(n) {
  const m = n.metrics || n.agent_status;
  const arch = m?.arch ? esc(m.arch) : "";
  return `${distroBadge(n)}${arch ? `<span class="arch-tag">${arch}</span>` : ""}<span class="collection-state pill warn" title="${esc(diagnosticHint(n))}"${n.metrics_stale ? "" : " hidden"}>指标异常</span>`;
}
function osTagsInner(n) {
  return `<span class="os-box">${osTagsContent(n)}</span>`;
}

// 文本 / HTML 先比一遍再写：数值没变就不碰 DOM（每秒那层刷新因此只写真正在动的几个数字）。
function setHTML(el, html) {
  if (!el || el.__html === html) return;
  // 补丁而不是替换：元素本身不换，WebKit 才不会重挑滚动锚点（数据见 patchInto 上方注释）。
  const fresh = document.createElement("div");
  fresh.innerHTML = html;
  if (!patchInto(el, fresh)) el.innerHTML = html;
  el.__html = html;
}
function setText(el, text) {
  if (el && el.textContent !== text) el.textContent = text;
}

// 每秒一次的轻刷新：只动数值——顶部四个数字、每张卡片的指标与速率、在线状态。写入都走 setHTML 的
// 补丁路径（只改变化的文本与属性、不换元素），所以手指底下写也不会把位置弹走，不必再暂停刷新。
// 列表视图复用同一批类名（.node-dot / .os-tag / .metrics-grid / .speeds），所以这一层两个视图通用。
let liveBusy = false;
async function liveTick() {
  if (liveBusy) return; // 上一拍还没回来就跳过这一拍，别把请求叠起来
  liveBusy = true;
  try {
    // 每秒只取会动的那部分（在线、指标、用量、降速窗口），并进上一轮完整列表里的节点；
    // 配置、探测计划与解锁结论跟着 10 秒一轮的 /nodes 走。新增或消失的节点也等那一轮。
    const live = await api("/nodes/live");
    for (const update of live) {
      const n = nodes.find((x) => x.id === update.id);
      if (n) Object.assign(n, update);
    }
    setHTML(document.querySelector("#overview"), overviewInner());
    for (const el of document.querySelectorAll("#nodes .node-card")) {
      const n = nodes.find((x) => x.id === el.dataset.node);
      if (!n) continue;
      el.classList.toggle("is-online", !!n.online);
      el.classList.toggle("is-offline", !n.online);
      el.querySelector(".node-dot")?.classList.toggle("offline", !n.online);
      setHTML(el.querySelector(".os-box"), osTagsContent(n));
      const state = el.querySelector(".node-state");
      if (state) {
        state.hidden = !!n.online;
        setText(state, n.online ? "离线" : statusLabel(n));
        state.title = offlineTitle(n);
      }
      const badge = el.querySelector(".collection-state");
      if (badge) { badge.hidden = !n.metrics_stale; badge.title = diagnosticHint(n); }
      setHTML(el.querySelector(".metrics-grid"), metricsInner(n));
      setHTML(el.querySelector(".speeds"), speedsInner(n));
      setText(el.querySelector(".remaining-value"), "剩余价值 " + remainingValueLabel(n));
    }
    alignCardRows();
    setText(document.querySelector("#updated"), new Date().toLocaleTimeString("zh-CN", { hour12: false }));
    markLive(true);
  } catch (e) {
    markLive(false);
    throw e;
  } finally {
    liveBusy = false;
  }
}

// 连接状态：以前请求失败时 LIVE 绿点照亮、「最近同步」停在最后一次成功的时刻却没有任何提示，
// 看起来像数据一切正常。连续失败 STALE_AFTER 拍（约 3 秒，躲开单次抖动）就把绿点换成红点、
// 文字改成「连接中断」；「最近同步」保留最后一次成功的时刻，正好说明数据停在了哪一刻。
const STALE_AFTER = 3;
let liveFailures = 0;
function markLive(ok) {
  liveFailures = ok ? 0 : liveFailures + 1;
  const stale = liveFailures >= STALE_AFTER;
  const label = document.querySelector(".live-label");
  if (!label || label.classList.contains("stale") === stale) return;
  label.classList.toggle("stale", stale);
  setText(label.querySelector(".live-text"), stale ? "连接中断" : "LIVE STATUS");
  const meta = document.querySelector(".hero-meta");
  meta?.classList.toggle("stale", stale);
  setText(meta?.querySelector("small"), stale ? "连接中断 · 正在重试" : "每秒刷新");
}

// 换皮肤：新皮肤可能声明了别的板面段落（layout.card_sections）与默认视图，卡片按新清单重绘；
// 访客自己选过视图就不动。要排在下面 alignCardRows 的监听之前（同一事件按注册顺序跑）。
document.addEventListener("tza:skin", () => {
  // 公开页才有卡片；首屏的 /nodes 还没回来（列表还空着）就等 refresh 自己画，别先画一屏「尚无公开节点」。
  if (!document.querySelector("#nodes")?.children.length) return;
  const next = storedView() || skinView();
  if (next !== view) {
    view = next;
    for (const b of document.querySelectorAll("[data-view]")) {
      const on = b.dataset.view === view;
      b.classList.toggle("active", on);
      b.setAttribute("aria-pressed", String(on));
    }
    applyView();
  }
  renderCards();
});

// 同一排卡片的分段拉齐：卡片本身已等高（.node-grid 的 1fr 行），但段落各排各的——配了降速
// 规则的卡在「周期用量」下多一行说明、到期价格那列多一行线路，下面的速率和延迟就比邻卡低一截
// （用户标红框点名「没做到统一、没对齐」）。这里按视觉行分组，把指标、速率计费这几段补齐到
// 同排最高的那张：补的是段落底部的 padding（--row-fill），内容位置不动，下一段自然对齐。
// 自然高度 = 实测高度 − 当前补的量（直接读元素上的变量：10 秒一轮的卡片补丁会改写属性），
// 所以不必先清零再量；只在补量变化半像素以上时写，静置时不碰样式。
// 主题在 layout 里写了 align_rows: false（卡片不是一摞段落，比如上半张是整幅装饰图）就不拉齐。
const ROW_SECTIONS = [".node-sub", ".metrics-grid", ".card-stats"];
// 同步执行：在写完 DOM 的同一个任务里量完、补完，浏览器下一次绘制看到的就是对齐后的样子。
// 排到下一个任务（setTimeout / rAF）的话，中间那一帧画的是没补齐的布局，卡片会跳。
function alignCardRows() {
  const list = document.querySelector("#nodes");
  if (!list) return;
  const unaligned = skinLayout().align_rows === false;
  const rows = new Map();
  const fills = [];
  for (const card of list.querySelectorAll(".node-card")) {
    // 每张卡三段，缺的段落留 null，下标与 ROW_SECTIONS 一一对应。
    const sections = ROW_SECTIONS.map((sel) => {
      const el = card.querySelector(`.card-body ${sel}`);
      if (!el) return null;
      const now = parseFloat(el.style.getPropertyValue("--row-fill")) || 0;
      const x = { el, now, natural: el.getBoundingClientRect().height - now, fill: 0 };
      fills.push(x);
      return x;
    });
    if (list.classList.contains("node-list") || unaligned) continue;
    const top = Math.round(card.offsetTop);
    if (!rows.has(top)) rows.set(top, []);
    rows.get(top).push(sections);
  }
  for (const group of rows.values()) {
    if (group.length < 2) continue;
    ROW_SECTIONS.forEach((_, i) => {
      const items = group.map((sections) => sections[i]).filter(Boolean);
      const max = Math.max(...items.map((x) => x.natural));
      for (const x of items) x.fill = max - x.natural;
    });
  }
  for (const x of fills) {
    if (Math.abs(x.now - x.fill) < 0.5) continue;
    if (x.fill >= 0.5) x.el.style.setProperty("--row-fill", x.fill.toFixed(1) + "px");
    else x.el.style.removeProperty("--row-fill");
  }
}
document.addEventListener("tza:skin", alignCardRows);
window.addEventListener("resize", alignCardRows);
document.fonts?.ready.then(alignCardRows);

// 每张卡片上一次渲染出来的 HTML：下次比对用，内容没变就不碰 DOM。
const renderedHTML = new WeakMap();

// 只写变化、不换元素的 DOM 补丁。替换元素会让 WebKit 重新挑选滚动锚点，用户看到的是内容整块位移：
// 线上实测停在离底 40px 处，30 秒位移 9 次、每次 163px；把页面定时器全停掉则 0 次。
// 所以结构相同（标签、层级、兄弟数一致）时只改文本与属性，element 本身一个都不换。
const PATCH_ATTRS = (el) => {
  const names = new Set();
  for (const a of el.attributes || []) names.add(a.name);
  return names;
};
function patchInto(el, next) {
  const a = el.childNodes;
  const b = next.childNodes;
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) {
    const x = a[i];
    const y = b[i];
    if (x.nodeType !== y.nodeType) return false;
    if (x.nodeType === 3) {
      if (x.data !== y.data) x.data = y.data;
      continue;
    }
    if (x.nodeType !== 1) continue;
    if (x.tagName !== y.tagName || x.childNodes.length !== y.childNodes.length) return false;
    if (!patchInto(x, y)) return false;
    // style 只会是运行时写的（模板里没有内联样式）：同排拉齐的 --row-fill。补丁把它删掉、
    // 再由拉齐补回，中间只要画一帧，下面的速率与延迟就上下跳一次（10 秒一轮，实测 31px）。
    for (const name of PATCH_ATTRS(x)) if (name !== "style" && !y.hasAttribute(name)) x.removeAttribute(name);
    for (const attr of y.attributes || []) {
      if (x.getAttribute(attr.name) !== attr.value) x.setAttribute(attr.name, attr.value);
    }
  }
  return true;
}

// 解锁分类浮层是一个挂在 body 上的单例：卡片自己 overflow:hidden，塞在卡片里的浮层会被
// 裁掉，所以按徽标的盒子把它定位到卡片外面。桌面靠 mouseenter/leave 展开，手机点一下切换。
let unlockPopEl = null;
let unlockPopBadge = null;
let unlockPopTimer = 0;
function unlockPop() {
  if (!unlockPopEl) {
    unlockPopEl = document.createElement("div");
    unlockPopEl.id = "unlock-pop";
    unlockPopEl.className = "unlock-pop";
    unlockPopEl.hidden = true;
    // 鼠标从徽标移到浮层里时不能收起，否则光标一离开徽标面板就没了。
    unlockPopEl.addEventListener("mouseenter", () => clearTimeout(unlockPopTimer));
    unlockPopEl.addEventListener("mouseleave", hideUnlockPop);
    document.body.append(unlockPopEl);
    // 点到别处就收起：移动端没有 hover，靠这条和徽标自己的一次点击。
    document.addEventListener("click", (e) => {
      if (
        unlockPopBadge &&
        !unlockPopEl.contains(e.target) &&
        !unlockPopBadge.contains(e.target)
      )
        hideUnlockPop();
    });
  }
  return unlockPopEl;
}
function showUnlockPop(badge) {
  const n = nodes.find((x) => x.id === badge.dataset.unlock);
  if (!n?.unlock || !catalog) return;
  clearTimeout(unlockPopTimer);
  const el = unlockPop();
  setHTML(el, unlockPanel(n.unlock, catalog));
  el.hidden = false;
  // 右对齐徽标（卡片右侧），再夹进视口——窄屏上也不会跑出屏幕。
  const rect = badge.getBoundingClientRect();
  const width = el.offsetWidth || 288;
  const height = el.offsetHeight || 240;
  const viewport = document.documentElement.clientWidth || 1024;
  const viewportH = document.documentElement.clientHeight || 800;
  el.style.left =
    Math.min(Math.max(8, rect.right - width), Math.max(8, viewport - width - 8)) + "px";
  // 竖直方向同样要夹：max-height 只在真有空间时才有用，徽标靠下时按「下方 + 8」定位会把
  // 浮层推出屏幕底部，面板里最后几个分类就点不到了。下方放不下就翻到徽标上方，都放不下
  // 才夹进视口（这时由 max-height 的滚动接手）。
  const below = rect.bottom + 8;
  const above = rect.top - height - 8;
  let top = below;
  if (below + height > viewportH - 8) top = above >= 8 ? above : Math.max(8, viewportH - height - 8);
  el.style.top = top + "px";
  if (unlockPopBadge && unlockPopBadge !== badge) {
    unlockPopBadge.classList.remove("open");
    unlockPopBadge.setAttribute("aria-expanded", "false");
  }
  unlockPopBadge = badge;
  badge.classList.add("open");
  badge.setAttribute("aria-expanded", "true");
}
function hideUnlockPop() {
  clearTimeout(unlockPopTimer);
  if (unlockPopEl) unlockPopEl.hidden = true;
  if (unlockPopBadge) {
    unlockPopBadge.classList.remove("open");
    unlockPopBadge.setAttribute("aria-expanded", "false");
    unlockPopBadge = null;
  }
}
// 离开徽标后稍等一下再收：给光标一点时间移进浮层（进不去才真的收起）。
function scheduleHideUnlockPop() {
  clearTimeout(unlockPopTimer);
  unlockPopTimer = setTimeout(hideUnlockPop, 180);
}

function renderCards() {
  const filtered = nodes.filter(
    (n) =>
      (!group || n.group === group) &&
      searchMatch(query, n.name, n.region, n.group, n.vendor),
  );
  // 每 10 秒的明细刷新。卡片只在内容变了的时候逐张替换，集合变了才整块重绘——整块重绘会把整个
  // 滚动子树重建，是最容易被感知为「跳」的动作。滚动位置的保持交给浏览器自己的滚动锚定
  // （WebKit / Chromium 都已实现），只有在真贴着页面底部时才手动续贴。
  const scroller = document.scrollingElement || document.documentElement;
  // 真·贴底才续贴：8px 以内才算贴在底部（写 48px 时，停在离底 40px 的人每轮会被拽到最底）。
  const atBottom = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 8;
  const list = document.querySelector("#nodes");
  const wanted = filtered.map(cardInner);
  const shown = [...list.querySelectorAll(".node-card")];
  const sameSet = shown.length === filtered.length && shown.every((el, i) => el.dataset.node === filtered[i].id);
  if (sameSet) {
    shown.forEach((el, i) => {
      for (const [name, value] of Object.entries(shellAttrs(filtered[i]))) {
        if (!value) el.removeAttribute(name);
        else if (el.getAttribute(name) !== value) el.setAttribute(name, value);
      }
      if (renderedHTML.get(el) === wanted[i]) return;
      const fresh = document.createElement("div");
      fresh.innerHTML = wanted[i];
      // 结构一样就只写变化的文本/属性；结构真变了（行数增减、切了展示方式）才兜底替换这一张卡的内容。
      if (!patchInto(el, fresh)) el.innerHTML = wanted[i];
      renderedHTML.set(el, wanted[i]);
    });
  } else {
    list.innerHTML = wanted.length
      ? filtered.map(card).join("")
      : `<div class="empty"><span class="empty-symbol">◎</span><h2>${nodes.length ? "没有匹配的节点" : "尚无公开节点"}</h2><p>${nodes.length ? "换一个关键词或分组试试。" : "在管理控制台添加节点，并开启公开展示。"}</p></div>`;
    [...list.querySelectorAll(".node-card")].forEach((el, i) => renderedHTML.set(el, wanted[i]));
  }
  alignCardRows();
  if (atBottom) scroller.scrollTop = scroller.scrollHeight;
  markListOverflow();
  // 单元格子只展示那一格的内容（延迟 / 回程），整张卡片才展示综合信息。
  for (const b of document.querySelectorAll("[data-detail]"))
    b.onclick = guard((e) => {
      e.stopPropagation();
      detail(
        b.dataset.detail,
        b.dataset.focusKind
          ? { kind: b.dataset.focusKind, carrier: b.dataset.focusCarrier, region: b.dataset.focusRegion, family: b.dataset.focusFamily }
          : null,
      );
    });
  // 卷轴状态每次渲染后刷一遍：卡壳上的 data-rolled 决定了数据区是摊开还是卷起，
  // 局部补丁不会动它，但整块重绘换了新卡壳，所以在这里统一补上（含 aria-expanded）。
  applyFolded();
  applyDecorations();
  // 单个延迟 / 回程格子是带 aria-label 的按钮：点它只看那一格（该运营商 + 该地址族）的聚焦视图。
  // 这与「点卡片空白处 / 装饰图空白处不弹详情」不冲突——2026-10-10 撤掉整卡 onclick 时把这些格子的
  // 点击一起弄哑了（用户随后点名「怎么点回程和延迟也不弹出来了」），这里单独接回来：格子本身仍不是
  // data-detail 按钮，弹的是只看那一格的聚焦视图，不是含主机历史的综合详情。
  for (const cell of document.querySelectorAll("[data-focus-kind]"))
    cell.onclick = guard((e) => {
      e.stopPropagation();
      const owner = cell.closest("[data-node]");
      if (owner)
        detail(owner.dataset.node, {
          kind: cell.dataset.focusKind,
          carrier: cell.dataset.focusCarrier,
          region: cell.dataset.focusRegion,
          family: cell.dataset.focusFamily,
        });
    });
  // 卡片空白处不再打开综合信息：只有内容里那枚「详细」按钮进详情页（用户 2026-10-10 点名的
  // 三条：点卡片空白区、点数据行、点装饰图空白处都不许弹详情）。所以这里不挂整卡 onclick，
  // 数据行（回程 / 延迟那一排）也不再是 data-detail 按钮——但每一格自己的聚焦视图照旧（见上）。
  for (const b of document.querySelectorAll("[data-card-family]"))
    b.onclick = () => { cardFamilies.set(b.dataset.cardNode, b.dataset.cardFamily); renderCards(); };
  // 延迟的国内 / 国际切换：点任何一张卡（或列表任何一格）上的按钮，整个面板一起翻。
  for (const b of document.querySelectorAll("[data-latency-scope]"))
    b.onclick = () => setLatencyScope(b.dataset.latencyScope);
  // 解锁徽标：桌面悬停展开，手机上点一下展开 / 收起（都是同一个单例浮层）。
  for (const b of document.querySelectorAll("[data-unlock]")) {
    b.onmouseenter = () => showUnlockPop(b);
    b.onmouseleave = scheduleHideUnlockPop;
    b.onclick = (e) => {
      e.stopPropagation();
      if (b.classList.contains("open")) hideUnlockPop();
      else showUnlockPop(b);
    };
  }
}

// 卡片收起：卡片头那颗「绿点 + 名字」按钮切换卡壳上的 data-rolled，状态按节点 id 存在本地。
// 收起后画成什么样由主题决定（样式表按 [data-rolled="true"] 选择）；默认样式不改变卡片。
// 收起状态挂在**卡壳**上，不是重绘出来的内容：每秒轻刷新与 10 秒全量刷新都走补丁路径（只改
// 文本/属性、不换元素），卡壳上的属性不会被擦掉；每次渲染后再统一 applyFolded() 刷一遍，刷新页面也还收着。
const FOLD_KEY = "tza-folded-cards";
const FOLD_ICON =
  '<svg viewBox="0 0 16 16" aria-hidden="true" focusable="false"><path d="M4 10.2 8 6.2l4 4" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>';
const foldedCards = new Set(
  (() => {
    try {
      const v = JSON.parse(localStorage.getItem(FOLD_KEY) || "[]");
      return Array.isArray(v) ? v.filter((x) => typeof x === "string") : [];
    } catch {
      return [];
    }
  })(),
);
function saveFolded() {
  try {
    localStorage.setItem(FOLD_KEY, JSON.stringify([...foldedCards]));
  } catch {
    /* 隐私模式写不进去，忽略：这一轮的状态仍在内存里 */
  }
}
// 卡壳上的状态 + 名字那颗按钮的 aria-expanded：折叠开关就是卡片头那颗「绿点 + 名字」按钮。
function applyFolded() {
  for (const card of document.querySelectorAll(".node-card")) {
    const toggles = card.querySelectorAll("[data-fold]");
    const rolled = toggles.length > 0 && foldedCards.has(card.dataset.node);
    if (rolled) card.dataset.rolled = "true";
    else delete card.dataset.rolled;
    for (const btn of toggles)
      btn.setAttribute("aria-expanded", String(!rolled));
  }
}

// 卡片装饰的变量：挑中项的图片与数值写成卡壳上的 --deco-<名字>（CSSOM 写入，页面 CSP 不收行内 style），
// 换了装饰项或主题就删掉上一轮多出来的。data-deco 属性在卡壳模板里（shellAttrs），这里只补变量。
function applyDecorations() {
  for (const card of document.querySelectorAll("#nodes .node-card")) {
    const n = nodes.find((x) => x.id === card.dataset.node);
    const want = decorationVars(n ? pickDecoration(n) : null);
    // 倒着按下标取（jsdom 的 CSSStyleDeclaration 不可迭代；边删边走也不跳项）。
    for (let i = card.style.length - 1; i >= 0; i--) {
      const name = card.style.item(i);
      if (name.startsWith("--deco-") && !(name in want)) card.style.removeProperty(name);
    }
    for (const [name, value] of Object.entries(want))
      if (card.style.getPropertyValue(name) !== value) card.style.setProperty(name, value);
  }
}
function toggleFolded(key) {
  if (!key) return;
  foldedCards.has(key) ? foldedCards.delete(key) : foldedCards.add(key);
  saveFolded();
  applyFolded();
}
// 委托在 document 上：卡片内容会被 patchInto 局部替换，绑在按钮上的 onclick 会随之失效，
// 委托一次就够（也省掉每次渲染后重新绑一遍的那趟遍历）。
document.addEventListener("click", (e) => {
  const btn = e.target?.closest?.("[data-fold]");
  if (!btn) return;
  e.stopPropagation();
  toggleFolded(btn.dataset.fold);
});

// 指针跟随：指针在卡上移动时，把指针相对卡心的位置写成两个**无单位**变量（-1…1）挂到卡壳上，并给卡片
// 加 is-tilt。主题可以用它们做倾斜、视差（比如让装饰层比看板多走几像素），默认样式不用它们。
// 只在这张卡上写、离场就删，所以不 tilt 的卡片一点合成开销都没有；系统开了「减少动态效果」就不做。
// jsdom（scripts/ui-smoke.mjs 里 eval 这份脚本）没有 matchMedia，取不到就当没开「减少动态效果」。
const TILT_OFF = typeof matchMedia === "function" ? matchMedia("(prefers-reduced-motion: reduce)") : null;
function tiltCard(card, e) {
  const r = card.getBoundingClientRect();
  if (!r.width || !r.height) return;
  const nx = Math.max(-1, Math.min(1, ((e.clientX - r.left) / r.width - 0.5) * 2));
  const ny = Math.max(-1, Math.min(1, ((e.clientY - r.top) / r.height - 0.5) * 2));
  card.style.setProperty("--tilt-nx", nx.toFixed(3));
  card.style.setProperty("--tilt-ny", ny.toFixed(3));
}
function untiltCard(card) {
  card.style.removeProperty("--tilt-nx");
  card.style.removeProperty("--tilt-ny");
  card.classList.remove("is-tilt");
}
document.addEventListener(
  "pointermove",
  (e) => {
    if (TILT_OFF?.matches || e.pointerType === "touch") return;
    const card = e.target?.closest?.(".node-card");
    for (const other of document.querySelectorAll(".node-card.is-tilt"))
      if (other !== card) untiltCard(other);
    if (!card) return;
    tiltCard(card, e);
    card.classList.add("is-tilt");
  },
  { passive: true },
);
document.addEventListener(
  "pointerout",
  (e) => {
    const card = e.target?.closest?.(".node-card");
    if (card && !card.contains(e.relatedTarget)) untiltCard(card);
  },
  { passive: true },
);

// 折叠只由节点名（卡片头那个「绿点+名字」按钮）触发，见 [data-fold] 的处理。

// 卡壳与内容分开：整块重绘用 card()，逐张替换只能喂 cardInner()——直接把整张卡赋给 innerHTML，
// 卡片会把自己嵌进自己里面（外层 article 里再套一个同名 article，页面高度和位置都会跟着错）。
const cardShell = (n, inner) => {
  // 挑中的装饰项挂在卡壳上（data-deco / data-deco-pack），背景层上也有一份 data-deco：主题既能按装饰项
  // 给整张卡上色（`.node-card[data-deco="…"]`），也能直接选背景层。两处都由 pickDecoration() 出，不会漂。
  const a = shellAttrs(n);
  const attrs = Object.entries(a)
    .filter(([, v]) => v)
    .map(([k, v]) => ` ${k}="${esc(v)}"`)
    .join("");
  return `<article data-part="node-card" class="node-card ${n.online ? "is-online" : "is-offline"}" data-node="${n.id}"${attrs}>${inner}</article>`;
};

// 卡壳上随节点设置变的那几个属性。10 秒一轮只按内容补丁（patchInto 不碰卡壳本身），
// 所以 renderCards 每轮也按这里对一遍——后台给节点换了装饰，卡片不用整块重绘就跟上。
function shellAttrs(n) {
  const deco = pickDecoration(n);
  // data-region / data-group / data-vendor 给主题包按地区、分组、商家选装饰图（稳定接口，见 docs/THEMES.md）。
  return {
    "data-deco": deco?.key || "",
    "data-deco-pack": deco?.pkg.id || "",
    "data-region": n.region || "",
    "data-group": n.group || "",
    "data-vendor": n.vendor || "",
  };
}

function card(n) {
  return cardShell(n, cardInner(n));
}

// 一台主机的全部取数：协议族、延迟分格、回程、计费。卡片视图与列表视图共用这一份，
// 两个渲染函数只决定怎么摆，所以切换展示方式不会改变任何一个数字。
function nodeFacts(n) {
  const cardFamily = cardFamilies.get(n.id) || "ipv4";
  const allObs = observations.get(n.id) || [];
  // The Agent reports which address families this host actually has. Older
  // Agents omit the field, so fall back to the old observation-based guess.
  const reported = Array.isArray(n.ip_families) ? new Set(n.ip_families) : null;
  const hasIPv4 = reported
    ? reported.has("ipv4")
    : allObs.some((o) => o.task.family === "ipv4");
  const hasIPv6 = reported
    ? reported.has("ipv6")
    : allObs.some((o) => o.task.family === "ipv6");
  const effectiveFamily =
    hasIPv4 && !hasIPv6
      ? "ipv4"
      : hasIPv6 && !hasIPv4
        ? "ipv6"
        : cardFamily === "ipv6" && hasIPv6
          ? "ipv6"
          : "ipv4";

  // 最新观测用于回程；延迟和丢包使用下方状态条的历史样本。
  const obs = allObs.filter(
    (o) => o.task.family === effectiveFamily,
  );
  // 三网延迟的地区取节点的延迟地区，其次回程地区，都未设则不限地区。
  // 目录外的自定义目标（以及没有运营商的旧任务）归入“其他”，见下面的海外格。
  const carriers = ["电信", "联通", "移动"];
  const bucketOf = (carrier) =>
    carriers.includes(carrier) ? carrier : "其他";
  const latencyRegion = n.display.latency_region || n.display.route_region || "";
  const inRegion = (region, want) => !want || region === want;
  // 一格的资格来自服务端给的 probe_targets（这台机器真有那些探测项，取自任务定义），
  // 不是从 jobs 推的：新绑的任务要等 Agent 认领才有行，从 jobs 推会让卡片先画空再长出来。
  // 哪组没有任务就不给那格——以前固定画满电信 / 联通 / 移动 / 其他，没挂任务的机器
  // 整天是一块「未检测 + 灰色空条」的骨架，看着像没加载完。后台的「公开卡片显示延迟」
  // 只在这个集合里再筛一道。
  const plan = n.probe_targets || [];
  const scoped = plan.filter((t) => t.family === effectiveFamily);
  // 三网一家一格（按卡片的延迟地区筛）；三网之外的目标（常用海外预设、自定义地址）按各自的
  // 地区——预设就是服务名——一个目标一格，顺序跟任务里的目标顺序走。以前它们全并成一格「其他」
  // 取平均，勾了 Google、Telegram、DNS 一堆，卡片上却只剩一行（用户点名）。这些格子都写了
  // 名字，所以不再受延迟地区限制：节点设了延迟地区时，海外预设照样显示。
  const showCarrier = (c) => !n.display.latency_carriers?.length || n.display.latency_carriers.includes(c);
  const otherRegions = [
    ...new Set(
      scoped
        .filter((t) => t.kind === "tcp" && bucketOf(t.carrier) === "其他")
        .map((t) => t.region || ""),
    ),
  ];
  const latencyBuckets = [
    ...carriers
      .filter(
        (c) =>
          showCarrier(c) &&
          scoped.some((t) => t.kind === "tcp" && inRegion(t.region, latencyRegion) && t.carrier === c),
      )
      .map((carrier) => ({ carrier, region: null })),
    ...(showCarrier("其他") ? otherRegions.map((region) => ({ carrier: "其他", region })) : []),
  ];
  // 一格收哪些结果：三网按运营商 + 延迟地区，海外按运营商「其他」+ 目标自己的地区。
  const inBucket = (b, carrier, region) =>
    (carriers.includes(carrier) ? carrier : "其他") === b.carrier &&
    (b.region == null ? inRegion(region, latencyRegion) : (region || "") === b.region);
  // 回程只有三网三格（自定义目标不参与回程），同一套资格判断。
  // 后台的「公开卡片显示回程」同样只在这个集合里再筛一道。
  const routeBuckets = carriers.filter(
    (c) =>
      scoped.some(
        (t) =>
          t.kind === "route" &&
          inRegion(t.region, n.display.route_region) &&
          t.carrier === c,
      ) && (!n.display.route_carriers?.length || n.display.route_carriers.includes(c)),
  );
  const latencies = latencyBuckets
    .map((b) => {
      const { carrier } = b;
      // 数字与状态条共用窗口内最近 BAR_COUNT 次；失败计入丢包，不当作 0 ms。
      const history = latencySamples
        .filter(
          (s) =>
            s.node_id === n.id &&
            s.family === effectiveFamily &&
            inBucket(b, s.carrier, s.region),
        )
        .slice(-BAR_COUNT);
      const ok = history.filter((s) => s.latency != null);
      const value = ok.length
        ? ok.reduce((v, s) => v + s.latency, 0) / ok.length
        : null;
      const sent = history.reduce((v, s) => v + s.sent, 0),
        received = history.reduce((v, s) => v + s.received, 0);
      const loss = sent ? Math.max(0, (sent - received) / sent * 100) : null;
      return {
        carrier,
        // 海外格的名字（服务名 / 地区）；三网格是 null，靠标志认。
        region: b.region,
        name: b.region == null ? null : b.region || "其他",
        value,
        text: value == null ? null : (value < 10 ? value.toFixed(1) : value.toFixed(0)) + " ms",
        // 一次都没成功时区分“没测过”和“测了但超时”
        missText: history.length ? "超时" : "未检测",
        loss,
        lossText: loss == null ? "—" : loss.toFixed(1) + "%",
        // 卡片和列表的数字、色带都取这一份样本。
        history,
        title: history.length
          ? `${date(history[0].at)} ~ ${date(history.at(-1).at)} · 最近 ${history.length} 次平均 · 成功 ${received}/${sent} 次`
          : "当前时间窗口内尚未检测",
      };
    });

  const routes = routeBuckets.map((carrier) => {
    const match = obs
      .filter(
        (o) =>
          o.task.kind === "route" &&
          inRegion(o.task.region, n.display.route_region) &&
          o.task.carrier === carrier,
      )
      .sort((a, b) => b.at - a.at)[0];
    const known =
      match && !match.stale && match.result.status === "ok" && n.online;
    const region = match?.task.region || n.display.route_region || "未设回程地区";
    const label = known ? match.result.route : "未知";
    const title = match ? [region, label, date(match.at), statusName[match.result.status] || "未知"].join(" · ") : "尚未检测";
    return { carrier, known, label, title };
  });

  // 探测段的三种空不是一回事：这台机器一个探测项都没有（none）、有但不在卡片当前这一族
  // （family）、有、这一族也有但都不在卡片显示的地区里（region）。都写成「未检测」只会
  // 让人以为在加载。有格子可画时 probeState 为 shown。
  const hasProbe = latencies.length > 0 || routes.length > 0;
  const probeState = hasProbe
    ? "shown"
    : !plan.some((t) => t.kind === "tcp" || t.kind === "route")
      ? "none"
      : scoped.some((t) => t.kind === "tcp" || t.kind === "route")
        ? "region"
        : "family";
  // 延迟分国内 / 国际两栏：没写名字的三网格在国内栏，写了目标名的海外格在国际栏。
  // 两边都有格子时才给切换（只有一边不摆一个点不动的 tab），此时有效分栏跟访客的选择走；
  // 只有一边时有效分栏就是那一边，访客的选择留着——这台机器哪天两边都有了再生效。
  const domesticCount = latencies.filter((l) => l.name == null).length;
  const intlCount = latencies.length - domesticCount;
  return {
    hasIPv4,
    hasIPv6,
    effectiveFamily,
    familyLabel: effectiveFamily === "ipv4" ? "IPv4" : "IPv6",
    latencyRegion,
    // 全部格子都在这里（两个分栏共用一份取数）；当前分栏画哪些由 scopedLatencies() 挑。
    latencies,
    scope: domesticCount && intlCount ? latencyScope : intlCount ? "intl" : "domestic",
    hasBothScopes: domesticCount > 0 && intlCount > 0,
    routes,
    hasProbe,
    probeState,
    expiry: days(n.billing.expires_at),
  };
}

// IPv4 / IPv6 开关：两个协议都在才给按钮，只有一个就给静态标签。
// 地址族开关：机器有几族就给几段——双栈两段（IPv4 / IPv6），单栈只给那一段，
// 不摆一个点不动的灰段。外面套一层 .family-slot：列表里这层是定宽的，所以单栈那行
// 后面的 linux / 架构标签与双栈行对齐（以前单栈是一颗 40px 窄标签，整列都对不齐）。
function familySwitch(n, f) {
  if (!f.hasIPv4 && !f.hasIPv6) return `<span class="family-tag">无探测</span>`;
  const seg = (fam, label) =>
    `<button data-card-family="${fam}" data-card-node="${n.id}" class="${f.effectiveFamily === fam ? "active" : ""}">${label}</button>`;
  return `<span class="family-slot"><div class="segmented card-family" aria-label="${esc(n.name)} IP 协议">${f.hasIPv4 ? seg("ipv4", "IPv4") : ""}${f.hasIPv6 ? seg("ipv6", "IPv6") : ""}</div></span>`;
}

// 长期机器的判定线：到期日在十年以外就写「长期」——用户会把长期（按量付费）机器的年份填到很
// 后面（例如 2226 年），照实写会变成「剩余 72965 天」，没有意义。真实日期留在 title 里。
const LONG_TERM_DAYS = 3650;

// 计费两块：到期与价格。列表视图竖排在计费格；卡片视图把这两样跟实时速率一起压进卡底那一带
// 的一行（见 statsBand），所以这里拆成两个片段函数供两边复用，文本完全一样。
// 价格：勾了免费（free）就写「免费」；没勾又是 0 价才是「未设置价格」。
function expiryPill(n, expiry) {
  return expiry == null
    ? `<span class="expiry-pill">未设到期</span>`
    : expiry < 0
      ? `<span class="expiry-pill expired">已过期 ${Math.abs(expiry)} 天</span>`
      : expiry > LONG_TERM_DAYS
        ? `<span class="expiry-pill" title="到期 ${esc(String(n.billing.expires_at || "").slice(0, 10))}">长期</span>`
        : `<span class="expiry-pill${expiry <= 7 ? " warning" : ""}">剩余 ${expiry} 天</span>`;
}

function priceLabel(n) {
  const price = n.billing.free
    ? "免费"
    : n.billing.price
      ? `${esc(n.billing.currency || "CNY")} ${n.billing.price.toFixed(2)} / ${esc(n.billing.price_period || "月")}`
      : "未设置价格";
  return `<span class="price-label">${price}</span>`;
}

// 线路规格归入周期用量，与额度、均值使用相同的左右两列。
// 没填带宽就不显示这一行，避免重复占位。
function bandwidthLine(n) {
  const text = bandwidth(n.line.mbps);
  return text
    ? `<small class="traffic-line" title="线路带宽（商家标称的端口 / 峰值速率）"><span>线路</span> <span class="traffic-value">${esc(text)}</span></small>`
    : "";
}

function billingInner(n, expiry) {
  return `${expiryPill(n, expiry)}${priceLabel(n)}<span class="remaining-value" title="${esc(remainingValueHint)}">剩余价值 ${esc(remainingValueLabel(n))}</span>`;
}

// 卡片底部的信息带：三列两行——左列实时速率（↓/↑）、中列本次开机网卡累计（↓/↑），
// 右列到期与价格。取代原来两个大方块 + 一条独立页脚（合计 105px），也比「速率一列、
// 计费一列」的两列版更紧凑：三列各自两行，一眼能对上方向。
// 累计流量和速率用同一套写法：数值 12px 深色，单位（GB / KB）10px 灰色——与速率的「/s」一致，
// 不然同一行里「7.3 KB/s」的单位被弱化、「7.3 GB」的却是深色加粗，看着就不是一套。
function withUnit(value) {
  const [num, unit] = String(value).split(" ");
  return unit ? `${num} <span class="unit">${unit}</span>` : num;
}

function statsBand(n, f) {
  const m = n.metrics;
  const totals = m
    ? `<div class="stat-totals" title="本次开机网卡累计（↑ 上传 / ↓ 下载）；换网卡或重装会重置"><span class="stat-item">累计 <b>${withUnit(bytes(m.net_out))}</b></span><span class="stat-item">累计 <b>${withUnit(bytes(m.net_in))}</b></span></div>`
    : "";
  return `<div data-part="card-stats" class="card-stats"><div class="speeds">${speedsInner(n)}</div>${totals}<div class="stat-facts">${billingInner(n, f.expiry)}</div></div>`;
}

// 没有探测任务（按设计不挂三网任务，例如国际线路的机器）：两段都不画，只留一行说明。
// 不摆空格子也不摆灰色空条——那正是「看着像没加载完」的来源；卡片的高度由同行最高的
// 那张拉齐（栅格默认拉伸）+ 计费行 margin-top:auto 贴底保证，所以不需要占位元素。
const PROBE_NOTES = {
  none: "仅主机指标",
  family: "该协议暂无探测任务",
  region: "该地区暂无探测任务",
};
const probeNote = (state) =>
  `<div class="probe-none">${PROBE_NOTES[state] || PROBE_NOTES.none}</div>`;

// 没有可用地址族时的那一行：Agent 从没上报过，就还不知道它有哪些地址——写「未检测到公网 IP」
// 会让人以为网络配置出了问题。报过、但报的是空列表，才是真没有公网地址。
function noFamilyNote(n) {
  if (n.metrics_stale && !n.metrics_at) return `<p class="hint network-unavailable">等待有效指标，上报后确认可用的 IP 协议。</p>`;
  return n.last_seen
    ? `<p class="hint network-unavailable">未检测到可用的公网 IP 地址，已跳过网络探测。</p>`
    : `<p class="hint network-unavailable">等待 Agent 接入，接入后开始网络探测。</p>`;
}

function networkSections(n, f) {
  // 一个探测项都没有的机器（按设计不挂任务）：不管它报没报地址族，都只写这一行。
  if (f.probeState === "none") return probeNote("none");
  if (!f.hasIPv4 && !f.hasIPv6) return noFamilyNote(n);
  if (!f.hasProbe) return probeNote(f.probeState);
  // 延迟与回程在这里各占一段：延迟是一家运营商一块（文字在上、延迟与丢包两条色带并排在下），
  // 标题行右端按分栏给地区注记与国内 / 国际切换；回程仍是三个并排的胶囊。
  return `${f.latencies.length ? `${latencyHead(f)}<div class="latency-bands${scopedLatencies(f).some((l) => l.name) ? " named" : ""}">${latencyBands(n, f)}</div>` : ""}${f.routes.length ? `<div class="route-heading"><span>三网回程 <b>${f.familyLabel}</b></span>${n.display.route_region ? `<span class="rep-label">${esc(n.display.route_region)}</span>` : ""}</div><div class="route-grid">${routeCells(n, f)}</div>` : ""}`;
}

// 回程格子里的运营商身份：用各家的官方图形标志（电信 / 联通 / 移动，见 web/carrier-logos.js）。
// 三格等宽后那 20px 的运营商名正好是「多骨干值一行读完」差的那几 px，所以格子里不写名字——
// 名字没丢：aria-label、title、data-focus-carrier 里都在，点选/筛选/详情照旧按 carrier 走。
function routeCells(n, f) {
  // 三格等宽、一格内容 89px：标志 11 + 间距 4 + 回程值（11px）74.4 = 89.4px，装得下最长的那条。
  // flex-wrap 留着兜底——万一以后出现更长的三段骨干值，整段落到第二行，不会断在箭头上。
  return f.routes
    .map((r) => {
      const label = esc(r.label).replace(/ → /g, " →&nbsp;");
      return `<button class="route-label ${r.known ? "known" : "unknown"}" data-focus-kind="route" data-focus-carrier="${esc(r.carrier)}" data-focus-family="${f.effectiveFamily}" title="${esc(r.title)}" aria-label="${esc(`${r.carrier} ${r.label}`)}">${carrierMark(r.carrier)}<strong class="${r.known ? "" : "muted"}">${label}</strong></button>`;
    })
    .join("");
}

// 列表视图的行内文本：一家运营商一行，延迟、丢包与最近 BAR_COUNT 次的色带横向排开。
// 回程标签在这里不截断（卡片那格为保持格子高度会截到两行、完整值放在 title 里），
// 列表的要求是文字全给出来：长标签换行，不省略。
// 行首与回程行一样放官方标志（用户点名：前面的「电信 / 联通 / 移动」也换成 logo——这样
// 同一行右边的回程行、卡片里的延迟块、列表里的延迟行三处口径一致）；名字仍在 aria-label /
// title / data-focus-carrier 里，点选与筛选照旧按 carrier 走。
// 海外格（l.name）多一个名字槽：同一格里只要有一行带名字，三网行也留同宽的空槽，数值仍排成一列。
function latencyLines(n, f) {
  const shown = scopedLatencies(f);
  const named = shown.some((l) => l.name);
  return shown
    .map(
      (l) =>
        `<button class="net-line ${l.value == null ? "unknown" : "known"}" data-focus-kind="latency" data-focus-carrier="${esc(l.carrier)}"${focusRegion(l)} data-focus-family="${f.effectiveFamily}" title="${esc(l.title)}" aria-label="${esc(`${l.name || l.carrier} ${l.value == null ? l.missText : l.text} 丢包 ${l.lossText}`)}"><span class="carrier-name">${carrierMark(l.carrier, l.name)}</span>${named ? `<span class="net-name" title="${esc(l.name || "")}">${esc(l.name || "")}</span>` : ""}<strong class="${l.value == null ? "muted" : ""}">${l.value == null ? l.missText : l.text}</strong><span class="net-loss">丢包 ${l.lossText}</span><span class="quality-bars" title="最近 ${BAR_COUNT} 次延迟">${sampleBars(l.history, (s) => s.latency, 200, BAR_COUNT)}</span></button>`,
    )
    .join("");
}

// 卡片视图的延迟块：一家运营商一块——上面一行「延迟 / 丢包」的文字，下面两条并排的小色带，
// 每条画最近 BAR_COUNT 次（参考图的形状就是这个：文字在上、色块在下，延迟与丢包并排）。
// 列宽用同一套三列栅格（运营商槽 11px + 两条等宽），所以「延迟」的数值正对着上面那条色带。
// 行首和列表的延迟行一样是官方标志，名字留在 aria-label / data-focus-carrier 里。
//
// 格数两个视图共用一个值：列表原来画 20 格、卡片画 10 格，同一份数据在两个视图里格数不一样，
// 用户点名统一到卡片的 10 格（列表那条吃满格子剩余宽度，10 格比 20 格更接近卡片里格子的大小）。
const BAR_COUNT = 10;

// 当前分栏要画的格子：国内栏是三网（没写名字的格），国际栏是海外预设与自定义（写了目标名的格）。
const scopedLatencies = (f) => f.latencies.filter((l) => (l.name == null) === (f.scope !== "intl"));

// 延迟段的小标题：左边「TCP 延迟 + 协议」，右边地区注记与国内 / 国际切换（两个视图共用这一段）。
// 地区注记只管三网那几格，所以只在国内栏显示；国际栏每一格都写着目标自己的名字，再挂一个地区
// 反而让人以为是海外目标的地区。
function latencyHead(f) {
  const label =
    f.scope === "domestic" && f.latencyRegion ? `<span class="rep-label">${esc(f.latencyRegion)}</span>` : "";
  const tabs = f.hasBothScopes ? latencyScopeTabs() : "";
  return `<div class="route-heading"><span>TCP 延迟 <b>${f.familyLabel}</b></span>${label || tabs ? `<span class="heading-side">${label}${tabs}</span>` : ""}</div>`;
}

// 国内 / 国际切换：选择全局生效并存在本地（见文件顶部的 LATENCY_SCOPE_KEY），点击在 renderCards
// 里按 data-latency-scope 绑定。
const latencyScopeTabs = () =>
  `<div class="segmented latency-scope" role="group" aria-label="延迟分栏">${["domestic", "intl"]
    .map(
      (s) =>
        `<button type="button" data-latency-scope="${s}" class="${latencyScope === s ? "active" : ""}" aria-pressed="${latencyScope === s}">${s === "domestic" ? "国内" : "国际"}</button>`,
    )
    .join("")}</div>`;

// 海外格点开的聚焦视图只看那一个目标：带上它的地区（三网格不带，照旧按运营商聚焦）。
const focusRegion = (l) => (l.region == null ? "" : ` data-focus-region="${esc(l.region)}"`);
const lossPercent = (s) => (s.sent ? Math.max(0, (s.sent - s.received) / s.sent * 100) : null);

function latencyBands(n, f) {
  return scopedLatencies(f)
    .map(
      (l) =>
        `<button class="net-band ${l.value == null ? "unknown" : "known"}" data-focus-kind="latency" data-focus-carrier="${esc(l.carrier)}"${focusRegion(l)} data-focus-family="${f.effectiveFamily}" title="${esc(l.title)}" aria-label="${esc(`${l.name || l.carrier} 延迟 ${l.value == null ? l.missText : l.text} 丢包 ${l.lossText}`)}"><span class="band-head"><span class="carrier-name">${carrierMark(l.carrier, l.name)}</span><span class="band-metric">${l.name ? `<i class="band-name" title="${esc(l.name)}">${esc(l.name)}</i>` : "<i>延迟</i>"}<strong class="${l.value == null ? "muted" : ""}">${l.value == null ? l.missText : l.text}</strong></span><span class="band-metric"><i>丢包</i><strong class="${l.loss == null ? "muted" : ""}">${l.lossText}</strong></span></span><span class="band-bars"><span class="quality-bars" title="最近 ${BAR_COUNT} 次延迟">${sampleBars(l.history, (s) => s.latency, 200, BAR_COUNT)}</span><span class="quality-bars loss-bars" title="最近 ${BAR_COUNT} 次丢包">${sampleBars(l.history, lossPercent, 20, BAR_COUNT)}</span></span></button>`,
    )
    .join("");
}

// 回程标签在这里也不写运营商名：与卡片那格一致，标志占列表行里那 11px 的运营商槽
//（`.carrier-name` 两行共用，同一列仍然对齐），名字仍在 aria-label / title /
// data-focus-carrier 里，点选与筛选照旧按 carrier 走。
function routeLines(n, f) {
  return f.routes
    .map(
      (r) =>
        `<button class="net-line route-line ${r.known ? "known" : "unknown"}" data-focus-kind="route" data-focus-carrier="${esc(r.carrier)}" data-focus-family="${f.effectiveFamily}" title="${esc(r.title)}" aria-label="${esc(`${r.carrier} ${r.label}`)}"><span class="carrier-name">${carrierMark(r.carrier)}</span><strong class="${r.known ? "" : "muted"}">${esc(r.label)}</strong></button>`,
    )
    .join("");
}

// 管理员登录后，公开页也会列出私有节点（接口按会话放行，方便预览）。不标出来的话，管理员看到
// 「私有」的机器出现在公开页上，会以为泄露了；访客那边接口根本不返回这些节点，不会看到这枚标签。
function privateTag(n) {
  return n.public === false
    ? '<span class="private-tag" title="这台是私有节点，只有登录后台的管理员能在公开页看到">仅管理员可见</span>'
    : "";
}

// 商家名牌：节点填了商家才有。默认皮肤里是地区 · 分组后面的一枚小牌；主题可以按 .vendor-plate
// 重新画（也可以改用卡片装饰槽里的 card-plate）。卡片与列表共用。
function vendorPlate(n) {
  return n.vendor ? `<span data-part="vendor-plate" class="vendor-plate" title="商家">${esc(n.vendor)}</span>` : "";
}

function gridInner(n) {
  const f = nodeFacts(n);
  // 卡片上没有可用地址族时不再挂「无探测」标签：卡底那行（「仅主机指标」或「未检测到可用的
  // 公网 IP 地址」）已经说明了同一件事，两处同时出现只是重复。详情页与列表仍保留固定标签。
  // 卡片的三个装饰槽（docs/THEMES.md「卡片装饰槽」）：任何主题包都能用 CSS 给它们挂背景图、角标、名牌，
  // 默认 display:none（styles.css）。DOM 顺序就是叠放顺序：
  //   ① 背景层 card-backdrop（垫在看板下面；里面一层 card-backdrop-layer 给第二张图用）
  //   ② HTML 看板 .card-body
  //   ③ 前景层 card-overlay（压在看板之上）
  //   ④ 名牌 card-plate（只在节点填了商家时有，内容是商家名）
  // 选哪张图由卡壳与背景层上的 data-deco 决定（主题清单里的装饰项，见 decorations.js），也可以按
  // 卡壳上的 data-region / data-group / data-vendor 选。挑中的项带名字时，背景层当作一张有说明的图。
  // data-bleed = 通铺到卡片边框的装饰层：允许越过卡片内边距（被 overflow: hidden 裁掉是设计意图），
  // 但越过边框 Box 就还是越界——scripts/card-overflow-check.mjs 按这个标记区分。
  const deco = pickDecoration(n);
  const fkey = n.id;
  // 当前只有二次元的立绘卡定义了卷起布局；其他主题显示普通名称，避免无效按钮。
  const foldable = document.documentElement.dataset.skin === "anime" && !!deco;
  const nameTag = foldable ? "button" : "div";
  const foldAttrs = foldable ? ` type="button" data-fold="${fkey}" aria-expanded="true" aria-controls="body-${n.id}"` : "";
  const figureA11y = deco ? `data-deco="${esc(deco.key)}" role="img" aria-label="${esc(deco.d.name)}"` : 'aria-hidden="true"';
  const figure = `<div data-part="card-backdrop" class="card-backdrop" data-bleed ${figureA11y}><span data-part="card-backdrop-layer" class="card-backdrop-layer" data-bleed aria-hidden="true"></span></div>`;
  const hands = `<div data-part="card-overlay" class="card-overlay" data-bleed aria-hidden="true"></div>`;
  const plate = n.vendor ? `<div data-part="card-plate" class="card-plate" aria-hidden="true">${esc(n.vendor)}</div>` : "";
  // 支持卷起的立绘卡使用名字按钮；普通主题没有按钮语义、箭头或折叠状态。
  const cardTop =
    `<div data-part="card-top" class="card-top"><${nameTag} class="fold-name"${foldAttrs}>` +
    `<span class="node-dot ${n.online ? "" : "offline"}"></span>` +
    `<span class="fold-name-text">${regionFlag(n.region)}${esc(n.name)}</span>` +
    (foldable ? `<span class="fold-caret" aria-hidden="true">${FOLD_ICON}</span>` : "") +
    `</${nameTag}><div class="card-actions">${offlineBadge(n)}${privateTag(n)}${unlockBadge(n)}${osTagsInner(n)}<button class="quiet detail-open" type="button" data-detail="${n.id}">详细</button></div></div>`;
  // 头部两行：第一行只有状态点 + 名字，右侧是发行版与架构胶囊；地区 / 分组与地址族开关落到第二行。
  // 以前地址族开关、系统、架构三样都挤在第一行，卡片窄时名字被挤成三行（手机上一张卡的头部 104px）。
  // 下面紧接着的一条信息带（实时速率 + 到期 + 价格）取代了原来的两个速率方块与独立页脚。
  // 「详细」是内容里唯一能进详情页的控件（卡片空白、数据行都不再导航）。
  // 板面各段按主题声明的 layout.card_sections 排序与取舍（没声明就是默认的四段全显示）。
  const sections = {
    "node-sub": () => `<div data-part="node-sub" class="node-sub"><p>${esc(n.region || "未设置地区")}<span> · </span>${esc(n.group || "默认分组")}${vendorPlate(n)}</p>${f.hasIPv4 || f.hasIPv6 ? familySwitch(n, f) : ""}</div>`,
    "metrics-grid": () => `<div data-part="metrics-grid" class="metrics-grid">${metricsInner(n)}</div>`,
    "card-stats": () => statsBand(n, f),
    network: () => networkSections(n, f),
  };
  const body = cardSections().map((name) => sections[name]()).join("");
  return `${figure}<div data-part="card-body" class="card-body">${cardTop}<div id="body-${n.id}">${body}</div></div>${hands}${plate}`;
}

// 卡片视图的板面段落：主题在 theme.json 的 layout.card_sections 里声明顺序与取舍，认不出的名字跳过。
const CARD_SECTIONS = ["node-sub", "metrics-grid", "card-stats", "network"];
function cardSections() {
  const declared = skinLayout().card_sections;
  return Array.isArray(declared) ? declared.filter((name, i) => CARD_SECTIONS.includes(name) && declared.indexOf(name) === i) : CARD_SECTIONS;
}

// 列表视图：一行一台，六格横向对齐（主机 / 指标 / 网速 / 延迟 / 回程 / 计费）。
// 状态不占一列——绿点直接贴在名字前面，与卡片视图同一个 .node-identity 结构。
function listInner(n) {
  const f = nodeFacts(n);
  const detected = f.hasIPv4 || f.hasIPv6;
  // 标题右侧只在真有地区时才给标签：以前空着会写上「全部地区 / 自动匹配地区」，
  // 这两句是占位值不是信息，用户点名去掉。
  const head = (label, region) =>
    `<div class="route-heading"><span>${label} <b>${f.familyLabel}</b></span>${region ? `<span class="rep-label">${esc(region)}</span>` : ""}</div>`;
  // 没有任务的机器在这两格里不画空格子 / 空行（与卡片视图同一套判断），只在延迟格给一行说明。
  // 延迟格的小标题与卡片视图共用 latencyHead：右端同样是地区注记与国内 / 国际切换。
  const netCell =
    f.probeState === "none"
      ? probeNote("none")
      : !detected
        ? noFamilyNote(n)
        : f.latencies.length
          ? latencyHead(f) + latencyLines(n, f)
          : f.hasProbe
            ? ""
            : probeNote(f.probeState);
  const routeCell =
    detected && f.routes.length
      ? head("三网回程", n.display.route_region) + routeLines(n, f)
      : "";
  // 名字不再是导航按钮（用户 2026-10-10：点数据行 / 点空白处都不许进详情页）。列表视图这一格
  // 里补一枚「详细」——不然列表里就没有任何进详情的入口了，卡片视图那边也是同一枚按钮。
  // 按钮放在名字下面那排徽标的右端，不跟名字抢同一行：主机格只有 190px，同一行时名字只剩 ~108px，
  // 「HK-BGP-01 · 香港直连」这种常见长度也要折行。
  return `<div class="row-identity"><div class="node-identity"><span class="node-dot ${n.online ? "" : "offline"}"></span><div><span class="node-name">${regionFlag(n.region)}${esc(n.name)}</span>${offlineBadge(n)}<p>${esc(n.region || "未设置地区")}<span> · </span>${esc(n.group || "默认分组")}${vendorPlate(n)}</p></div></div><div class="card-actions">${privateTag(n)}${familySwitch(n, f)}${osTagsInner(n)}<button class="quiet detail-open" type="button" data-detail="${n.id}">详细</button></div></div><div data-part="metrics-grid" class="metrics-grid">${metricsInner(n)}</div><div class="speeds-cell"><div class="route-heading"><span>实时网速</span></div><div class="speeds">${speedsInner(n)}</div></div><div class="net-cell">${netCell}</div><div class="route-cell">${routeCell}</div><div class="billing-cell">${billingInner(n, f.expiry)}</div>`;
}

// 展示方式只在这里分叉：两个视图取数完全相同，liveTick 那一层找的也是同一批类名。
function cardInner(n) {
  return view === "list" ? listInner(n) : gridInner(n);
}

// 一行 N 格，每格代表一次探测结果，按那次的数值落到 20 档色带上
// （绿 → 青 → 黄 → 橙 → 红）。没有足够的探测时左边的格子留空，最新的一次在最右。
// 颜色来自 styles.css 的 .quality-bar.qN：页面 CSP 是 style-src 'self'，行内
// style 会被浏览器直接忽略，所以只能用 class。
function sampleBars(samples, pick, ceiling, count = 20) {
  const recent = samples.slice(-count);
  const cells = new Array(count).fill(null);
  recent.forEach((s, i) => { cells[count - recent.length + i] = s; });
  return cells
    .map((s) => {
      if (!s) return '<i class="quality-bar idle"></i>';
      const value = pick(s);
      // 失败的那一次没有数值：给一个空心红格，和"没有数据"的灰格区分开。
      if (value == null) return `<i class="quality-bar fail" title="${esc(date(s.at))} · 失败"></i>`;
      const step = Math.max(1, Math.min(20, Math.round((value / ceiling) * 20)));
      const text = ceiling === 20 ? value.toFixed(1) + "%" : (value < 10 ? value.toFixed(1) : value.toFixed(0)) + " ms";
      return `<i class="quality-bar q${step}" title="${esc(date(s.at))} · ${esc(text)}"></i>`;
    })
    .join("");
}

start().catch((e) => {
  app.innerHTML = `<main class="empty"><h1>暂时无法连接</h1><p>${esc(e.message)}</p><a class="button" href="${adminPage ? "/admin" : "/"}">重新加载</a></main>`;
});
