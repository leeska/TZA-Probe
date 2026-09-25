import { api, send, esc, toast, modal, guard } from "./api.js";
import { bytes, date, days, percent, statusName } from "./format.js";
import { detail } from "./detail.js";
import { mountAdmin } from "./admin.js";
import { applyTheme, themeButton } from "./theme.js";
import { searchMatch } from "./search.js";
import "./ui-select.js";

const app = document.querySelector("#app");
const adminPage = location.pathname.startsWith("/admin");
let nodes = [],
  cardFamilies = new Map(),
  query = "",
  group = "",
  observations = new Map(),
  latencySamples = [],
  generation = 0;

// 展示方式：card = 每台主机一张卡（默认），list = 一行一台、字段横向对齐。
// 只存在浏览器本地，刷新与换页保持；存不进去（隐私模式等）就退回卡片视图。
const VIEW_KEY = "tza-view";
function storedView() {
  try {
    return localStorage.getItem(VIEW_KEY) === "list" ? "list" : "card";
  } catch {
    return "card";
  }
}
let view = storedView();

export const brand = `<a class="brand" href="/" aria-label="TZA Probe 首页"><span class="brand-mark">t<span>z</span>a</span><span>PROBE<small>基础设施观测</small></span></a>`;

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

async function start() {
  applyTheme();
  noWheelStepping();
  if (adminPage) {
    await mountAdmin(app, brand);
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
  app.innerHTML = `<header class="public-header">${brand}<nav><span class="live-label"><i></i><span class="live-text">LIVE STATUS</span></span>${themeButton()}<a href="/admin" class="button quiet">管理控制台 ↗</a></nav></header><main class="public-main"><section class="hero"><div><div class="eyebrow">YOUR INFRASTRUCTURE, AT A GLANCE</div><h1>每一台主机，<span>尽在眼前。</span></h1><p>实时状态、网络线路与用量，在一个清晰的视图里。</p></div><div class="hero-meta"><span>最近同步</span><strong id="updated">连接中</strong><small>每秒刷新</small></div></section><section id="overview" class="overview"></section><section class="toolbar"><div class="filter-controls"><div class="search"><span>⌕</span><input id="search" aria-label="搜索节点" placeholder="搜索名称、地区（支持拼音与缩写）"></div><select id="group-filter" aria-label="节点分组"><option value="">全部分组</option></select></div><div class="view-bar">${viewSwitch()}</div></section><div id="nodes" class="node-grid"></div><footer><span>独立 · 轻量 · 可控</span><span>TZA PROBE · V2</span></footer></main>`;

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
  document.querySelector("#nodes")?.classList.toggle("node-list", view === "list");
}

async function refresh() {
  const ticket = ++generation;
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

  renderCards();

  // 卡片上每个小格子是一次探测结果，所以除了逐目标的最新观测，还要取最近若干次原始结果。
  const sampleTicket = api("/latency-samples?limit=20")
    .then((data) => { if (ticket === generation) latencySamples = data.samples || []; })
    .catch(() => { if (ticket === generation) latencySamples = []; });
  let index = 0;
  await Promise.all(
    Array.from({ length: Math.min(4, nodes.length) }, async () => {
      while (index < nodes.length) {
        const n = nodes[index++];
        try {
          const data = await api(`/nodes/${n.id}/observations`);
          if (ticket === generation) observations.set(n.id, data);
        } catch {
          observations.delete(n.id);
        }
      }
    }),
  );
  await sampleTicket;
  if (ticket === generation) renderCards();
}

function meter(label, value, sub = "") {
  return `<div class="metric"><div><span>${label}</span><strong>${value == null ? "—" : value.toFixed(1) + "%"}</strong></div><progress max="100" value="${value ?? 0}" aria-label="${label}"></progress><small>${esc(sub)}</small></div>`;
}

// 顶部四个数字与卡片上的「指标 / 实时速率」两块抽成函数：每秒的轻刷新只换这两处的 HTML，
// 整卡重建留给 10 秒一轮的探测明细（那一轮要打十几个请求，也会整块换 DOM）。
function overviewInner() {
  const online = nodes.filter((n) => n.online),
    traffic = nodes.reduce((v, n) => v + n.traffic_used, 0),
    rateIn = online.reduce((v, n) => v + (n.metrics?.rate_in || 0), 0),
    rateOut = online.reduce((v, n) => v + (n.metrics?.rate_out || 0), 0);
  return `<div class="overview-tile"><div class="tile-header"><span>在线节点</span><i class="dot ${online.length ? "active" : ""}"></i></div><strong>${online.length}<small> / ${nodes.length}</small></strong></div><div class="overview-tile"><div class="tile-header"><span>实时下载</span><span class="tile-badge teal-badge">↓</span></div><strong>${bytes(rateIn)}<small>/s</small></strong></div><div class="overview-tile"><div class="tile-header"><span>实时上传</span><span class="tile-badge orange-badge">↑</span></div><strong>${bytes(rateOut)}<small>/s</small></strong></div><div class="overview-tile"><div class="tile-header"><span>本周期用量合计</span><span class="tile-badge purple-badge">⚡</span></div><strong>${bytes(traffic)}</strong></div>`;
}

function metricsInner(n) {
  const m = n.metrics,
    hasMetrics = !!m;
  const cpuSub = hasMetrics ? "系统计算负载" : "等待采集";
  const memSub = hasMetrics ? `${bytes(m.memory_used)} / ${bytes(m.memory_total)}` : "未报告";
  const diskSub = hasMetrics ? `${bytes(m.disk_used)} / ${bytes(m.disk_total)}` : "未报告";
  const trafficSub = n.traffic_limit ? `${bytes(n.traffic_limit)} 额度 · ${esc({ sum: "双向", max: "取峰值", in: "仅下载", out: "仅上传" }[n.traffic_mode])}` : "无限制额度";
  return `${meter("CPU", hasMetrics ? m.cpu : null, cpuSub)}${meter("内存", hasMetrics ? percent(m.memory_used, m.memory_total) : null, memSub)}${meter("磁盘", hasMetrics ? percent(m.disk_used, m.disk_total) : null, diskSub)}<div class="metric"><div><span>周期用量</span><strong>${bytes(n.traffic_used)}</strong></div><progress max="100" value="${percent(n.traffic_used, n.traffic_limit)}" aria-label="周期流量额度"></progress><small>${trafficSub}</small></div>`;
}

// 实时速率：卡片里是两个带标签的方框（图标 + 「实时下载」+ 数值）；
// 列表里那格只有一百来像素宽，改成一行一条（图标 + 数值靠右，标签放进 title），
// 两个视图的 HTML 都由这里出，liveTick 那一层跟着 view 走就不会串。
function speedsInner(n) {
  const m = n.metrics,
    isLive = n.online && m;
  const rateIn = isLive ? `${bytes(m.rate_in)}<span class="unit">/s</span>` : '<span class="unit-none">—</span>';
  const rateOut = isLive ? `${bytes(m.rate_out)}<span class="unit">/s</span>` : '<span class="unit-none">—</span>';
  if (view === "list")
    return `<div class="speed-line" title="实时下载"><span class="speed-icon teal-icon">↓</span><strong>${rateIn}</strong></div><div class="speed-line" title="实时上传"><span class="speed-icon orange-icon">↑</span><strong>${rateOut}</strong></div>`;
  return `<div class="speed-box"><span class="speed-icon teal-icon">↓</span><div><small>实时下载</small><strong>${rateIn}</strong></div></div><div class="speed-box"><span class="speed-icon orange-icon">↑</span><div><small>实时上传</small><strong>${rateOut}</strong></div></div>`;
}

// 系统标签：操作系统 + 架构（Agent 报的 runtime.GOOS / GOARCH，老 Agent 没有 arch 就不给这个标签）。
// 架构单独一个标签，因为它要在列表里换到 linux 下面那一行；写进 os-tag 里会被每秒刷新的
// setText(.os-tag) 整段覆盖掉。
function osTagsInner(n) {
  const m = n.metrics;
  const os = esc(m?.os || (n.online ? "在线" : "等待接入"));
  const arch = m?.arch ? esc(m.arch) : "";
  // 两个标签套一层 .os-box：卡片里是一行两个，列表里那层改成竖排，架构就落在 linux 下面
  // （直接给 arch-tag 撑满一行的话，那颗胶囊的底色会横贯整格）。
  return `<span class="os-box"><span class="os-tag">${os}</span>${arch ? `<span class="arch-tag">${arch}</span>` : ""}</span>`;
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
    const next = await api("/nodes");
    nodes = next;
    setHTML(document.querySelector("#overview"), overviewInner());
    for (const el of document.querySelectorAll("#nodes .node-card")) {
      const n = nodes.find((x) => x.id === el.dataset.node);
      if (!n) continue;
      el.classList.toggle("is-online", !!n.online);
      el.classList.toggle("is-offline", !n.online);
      el.querySelector(".node-dot")?.classList.toggle("offline", !n.online);
      setText(el.querySelector(".os-tag"), n.metrics?.os || (n.online ? "在线" : "等待接入"));
      setHTML(el.querySelector(".metrics-grid"), metricsInner(n));
      setHTML(el.querySelector(".speeds"), speedsInner(n));
    }
    setText(document.querySelector("#updated"), new Date().toLocaleTimeString("zh-CN", { hour12: false }));
  } finally {
    liveBusy = false;
  }
}

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
    for (const name of PATCH_ATTRS(x)) if (!y.hasAttribute(name)) x.removeAttribute(name);
    for (const attr of y.attributes || []) {
      if (x.getAttribute(attr.name) !== attr.value) x.setAttribute(attr.name, attr.value);
    }
  }
  return true;
}

function renderCards() {
  const filtered = nodes.filter(
    (n) =>
      (!group || n.group === group) &&
      searchMatch(query, n.name, n.region),
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
      : `<div class="empty"><span class="empty-symbol">◎</span><h2>${nodes.length ? "没有匹配的节点" : "尚无公开节点"}</h2><p>${nodes.length ? "换一个关键词或分组试试。" : "在管理控制台添加节点，并开办公开展示。"}</p></div>`;
    [...list.querySelectorAll(".node-card")].forEach((el, i) => renderedHTML.set(el, wanted[i]));
  }
  if (atBottom) scroller.scrollTop = scroller.scrollHeight;
  // 单元格子只展示那一格的内容（延迟 / 回程），整张卡片才展示综合信息。
  for (const b of document.querySelectorAll("[data-detail]"))
    b.onclick = guard((e) => {
      e.stopPropagation();
      detail(
        b.dataset.detail,
        b.dataset.focusKind
          ? { kind: b.dataset.focusKind, carrier: b.dataset.focusCarrier, family: b.dataset.focusFamily }
          : null,
      );
    });
  for (const c of document.querySelectorAll(".node-card"))
    c.onclick = guard((e) => {
      // 卡片里已有自己行为的控件（协议开关、格子、节点名）不触发整卡的综合信息
      if (e.target.closest("button, a, select, input, [data-detail]")) return;
      detail(c.dataset.node);
    });
  for (const b of document.querySelectorAll("[data-card-family]"))
    b.onclick = () => { cardFamilies.set(b.dataset.cardNode, b.dataset.cardFamily); renderCards(); };
}

// 卡壳与内容分开：整块重绘用 card()，逐张替换只能喂 cardInner()——直接把整张卡赋给 innerHTML，
// 卡片会把自己嵌进自己里面（外层 article 里再套一个同名 article，页面高度和位置都会跟着错）。
const cardShell = (n, inner) =>
  `<article class="node-card ${n.online ? "is-online" : "is-offline"}" data-node="${n.id}">${inner}</article>`;

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
    : allObs.some((o) => o.task.family === "ipv4" && o.task.enabled);
  const hasIPv6 = reported
    ? reported.has("ipv6")
    : allObs.some((o) => o.task.family === "ipv6" && o.task.enabled);
  const effectiveFamily =
    hasIPv4 && !hasIPv6
      ? "ipv4"
      : hasIPv6 && !hasIPv4
        ? "ipv6"
        : cardFamily === "ipv6" && hasIPv6
          ? "ipv6"
          : "ipv4";

  const obs = allObs.filter(
    (o) => o.task.family === effectiveFamily && o.task.enabled,
  );
  // 延迟按运营商分格显示，地区取节点的延迟地区，其次代表地区，都未设则不限地区。
  // 目录外的自定义目标（以及没有运营商的旧任务）归入“其他”。
  const carriers = ["电信", "联通", "移动"];
  const latencyRegion = n.latency_region || n.representative || "";
  const tcp = obs.filter(
    (o) =>
      n.online &&
      o.task.kind === "tcp" &&
      !o.stale &&
      o.result.status !== "pending" &&
      (!latencyRegion || o.task.region === latencyRegion),
  );
  const visibleCarriers = n.latency_carriers?.length ? n.latency_carriers : [...carriers, "其他"];
  const latencies = visibleCarriers
    .map((carrier) => {
      const matches = tcp.filter(
        (o) =>
          (carriers.includes(o.task.carrier) ? o.task.carrier : "其他") ===
          carrier,
      );
      const ok = matches.filter((o) => o.result.latency != null);
      const value = ok.length
        ? ok.reduce((v, o) => v + o.result.latency, 0) / ok.length
        : null;
      const sent = matches.reduce((v, o) => v + o.result.sent, 0),
        received = matches.reduce((v, o) => v + o.result.received, 0);
      const loss = sent ? Math.max(0, (sent - received) / sent * 100) : null;
      // 每个格子是一次探测：20 格 = 最近 20 次，最新在右。失败的那次用空心红格表示。
      const history = latencySamples
        .filter(
          (s) =>
            s.node_id === n.id &&
            s.family === effectiveFamily &&
            (!latencyRegion || s.region === latencyRegion) &&
            (carriers.includes(s.carrier) ? s.carrier : "其他") === carrier,
        )
        .slice(-20);
      const recent = matches.map((o) => o.at).sort((a, b) => b - a)[0];
      return {
        carrier,
        value,
        text: value == null ? null : (value < 10 ? value.toFixed(1) : value.toFixed(0)) + " ms",
        // 一次都没成功时区分“没测过”和“测了但超时”
        missText: matches.length ? "超时" : "未检测",
        loss,
        lossText: loss == null ? "—" : loss.toFixed(1) + "%",
        latencyBars: sampleBars(history, (s) => s.latency, 200),
        lossBars: sampleBars(
          history,
          (s) => (s.sent ? Math.max(0, (s.sent - s.received) / s.sent * 100) : null),
          20,
        ),
        title: matches.length
          ? `${date(recent)} · 成功 ${received}/${sent} 次`
          : "尚未检测",
      };
    });

  const routes = carriers.map((carrier) => {
    const match = obs
      .filter(
        (o) =>
          o.task.kind === "route" &&
          o.task.family === effectiveFamily &&
          (!n.representative || o.task.region === n.representative) &&
          o.task.carrier === carrier,
      )
      .sort((a, b) => b.at - a.at)[0];
    const known =
      match && !match.stale && match.result.status === "ok" && n.online;
    const region = match?.task.region || n.representative || "未设代表地区";
    const label = known ? match.result.route : "未知";
    const title = match ? [region, label, date(match.at), statusName[match.result.status] || "未知"].join(" · ") : "尚未检测";
    return { carrier, known, label, title };
  });

  return {
    hasIPv4,
    hasIPv6,
    effectiveFamily,
    familyLabel: effectiveFamily === "ipv4" ? "IPv4" : "IPv6",
    latencyRegion,
    latencies,
    routes,
    expiry: days(n.expires_at),
  };
}

// IPv4 / IPv6 开关：两个协议都在才给按钮，只有一个就给静态标签。
// 地址族开关：只要上报了地址族就一律给这个小开关（IPv4 / IPv6 两段），缺哪一族就把那一段置灰。
// 以前单栈机器只有一个 .family-tag、双栈机器是两段开关，同一列里每行的开关宽度不一样（40px vs 94px），
// 后面那颗 linux / 架构标签的起始位置跟着每行都不同，列表看着就不齐。
function familySwitch(n, f) {
  if (!f.hasIPv4 && !f.hasIPv6) return `<span class="family-tag">无探测</span>`;
  const seg = (fam, label, has) =>
    has
      ? `<button data-card-family="${fam}" data-card-node="${n.id}" class="${f.effectiveFamily === fam ? "active" : ""}">${label}</button>`
      : `<button data-card-family="${fam}" data-card-node="${n.id}" disabled aria-disabled="true" title="这台机器没有 ${label} 地址">${label}</button>`;
  return `<div class="segmented card-family" aria-label="${esc(n.name)} IP 协议">${seg("ipv4", "IPv4", f.hasIPv4)}${seg("ipv6", "IPv6", f.hasIPv6)}</div>`;
}

// 计费两块：到期与价格。卡片里横排在卡底，列表里竖排在计费格，文本完全一样。
function billingInner(n, expiry) {
  return `<span class="expiry-pill ${expiry != null && expiry <= 7 ? "warning" : ""}">${expiry == null ? "未设到期" : expiry < 0 ? "已过期 " + Math.abs(expiry) + " 天" : "剩余 " + expiry + " 天"}</span><span class="price-label">${n.price ? esc(n.currency || "CNY") + " " + n.price.toFixed(2) + " / " + esc(n.price_period || "月") : "未设置价格"}</span>`;
}

function networkSections(n, f) {
  return f.hasIPv4 || f.hasIPv6
    ? `<div class="route-heading"><span>TCP 延迟 <b>${f.familyLabel}</b></span>${f.latencyRegion ? `<span class="rep-label">${esc(f.latencyRegion)}</span>` : ""}</div><div class="route-grid latency-grid">${latencyCells(n, f)}</div><div class="route-heading"><span>三网回程 <b>${f.familyLabel}</b></span>${n.representative ? `<span class="rep-label">${esc(n.representative)}</span>` : ""}</div><div class="route-grid">${routeCells(n, f)}</div>`
    : `<p class="hint network-unavailable">未检测到可用的公网 IP 地址，已跳过网络探测。</p>`;
}

// 卡片视图的延迟格：一格一个运营商，延迟 / 丢包各一行数值加色带。
function latencyCells(n, f) {
  return f.latencies
    .map(
      (l) =>
        `<button class="route-label latency-label ${l.value == null ? "unknown" : "known"}" data-detail="${n.id}" data-focus-kind="latency" data-focus-carrier="${esc(l.carrier)}" data-focus-family="${f.effectiveFamily}" title="${esc(l.title)}"><span class="carrier-name">${l.carrier}</span><span class="quality-row latency-row"><span>延迟</span><strong class="${l.value == null ? "muted" : ""}">${l.value == null ? l.missText : l.text}</strong></span><span class="quality-bars">${l.latencyBars}</span><span class="quality-row loss-row"><span>丢包</span><strong class="${l.loss == null ? "muted" : ""}">${l.lossText}</strong></span><span class="quality-bars loss-bars">${l.lossBars}</span></button>`,
    )
    .join("");
}

function routeCells(n, f) {
  return f.routes
    .map(
      (r) =>
        `<button class="route-label ${r.known ? "known" : "unknown"}" data-detail="${n.id}" data-focus-kind="route" data-focus-carrier="${esc(r.carrier)}" data-focus-family="${f.effectiveFamily}" title="${esc(r.title)}"><span>${r.carrier}</span><strong class="${r.known ? "" : "muted"}">${esc(r.label)}</strong></button>`,
    )
    .join("");
}

// 列表视图的行内文本：一家运营商一行，延迟、丢包与最近 20 次的色带横向排开。
// 回程标签在这里不截断（卡片那格为保持格子高度会截到两行、完整值放在 title 里），
// 列表的要求是文字全给出来：长标签换行，不省略。
function latencyLines(n, f) {
  return f.latencies
    .map(
      (l) =>
        `<button class="net-line ${l.value == null ? "unknown" : "known"}" data-detail="${n.id}" data-focus-kind="latency" data-focus-carrier="${esc(l.carrier)}" data-focus-family="${f.effectiveFamily}" title="${esc(l.title)}"><span class="carrier-name">${l.carrier}</span><strong class="${l.value == null ? "muted" : ""}">${l.value == null ? l.missText : l.text}</strong><span class="net-loss">丢包 ${l.lossText}</span><span class="quality-bars">${l.latencyBars}</span></button>`,
    )
    .join("");
}

function routeLines(n, f) {
  return f.routes
    .map(
      (r) =>
        `<button class="net-line route-line ${r.known ? "known" : "unknown"}" data-detail="${n.id}" data-focus-kind="route" data-focus-carrier="${esc(r.carrier)}" data-focus-family="${f.effectiveFamily}" title="${esc(r.title)}"><span class="carrier-name">${r.carrier}</span><strong class="${r.known ? "" : "muted"}">${esc(r.label)}</strong></button>`,
    )
    .join("");
}

function gridInner(n) {
  const f = nodeFacts(n);
  return `<div class="card-top"><div class="node-identity"><span class="node-dot ${n.online ? "" : "offline"}"></span><div><button class="node-name" data-detail="${n.id}">${esc(n.name)}</button><p>${esc(n.region || "未设置地区")}<span> · </span>${esc(n.group || "默认分组")}</p></div></div><div class="card-actions">${familySwitch(n, f)}${osTagsInner(n)}</div></div><div class="metrics-grid">${metricsInner(n)}</div><div class="speeds">${speedsInner(n)}</div>${networkSections(n, f)}<div class="card-bottom">${billingInner(n, f.expiry)}</div>`;
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
  return `<div class="row-identity"><div class="node-identity"><span class="node-dot ${n.online ? "" : "offline"}"></span><div><button class="node-name" data-detail="${n.id}">${esc(n.name)}</button><p>${esc(n.region || "未设置地区")}<span> · </span>${esc(n.group || "默认分组")}</p></div></div><div class="card-actions">${familySwitch(n, f)}${osTagsInner(n)}</div></div><div class="metrics-grid">${metricsInner(n)}</div><div class="speeds-cell"><div class="route-heading"><span>实时网速</span></div><div class="speeds">${speedsInner(n)}</div></div><div class="net-cell">${detected ? head("TCP 延迟", f.latencyRegion) + latencyLines(n, f) : `<p class="hint network-unavailable">未检测到可用的公网 IP 地址，已跳过网络探测。</p>`}</div><div class="route-cell">${detected ? head("三网回程", n.representative) + routeLines(n, f) : ""}</div><div class="billing-cell">${billingInner(n, f.expiry)}</div>`;
}

// 展示方式只在这里分叉：两个视图取数完全相同，liveTick 那一层找的也是同一批类名。
function cardInner(n) {
  return view === "list" ? listInner(n) : gridInner(n);
}

// 一行 20 格，每格代表一次探测结果，按那次的数值落到 20 档色带上
// （绿 → 青 → 黄 → 橙 → 红）。没有足够的探测时左边的格子留空，最新的一次在最右。
// 颜色来自 styles.css 的 .quality-bar.qN：页面 CSP 是 style-src 'self'，行内
// style 会被浏览器直接忽略，所以只能用 class。
function sampleBars(samples, pick, ceiling) {
  const recent = samples.slice(-20);
  const cells = new Array(20).fill(null);
  recent.forEach((s, i) => { cells[20 - recent.length + i] = s; });
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
