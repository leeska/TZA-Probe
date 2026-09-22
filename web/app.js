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
  watchScrolling();
  await refresh();
  // 数值类数据（负载、实时速率、用量）约 1 秒一笔，所以每秒轻刷一次——只换数字，不重建卡片；
  // 探测明细（每个目标的观测与最近样本）仍是 10 秒一轮，那一轮要打十几个请求。
  // 用户正在滑动时两拍都让开，等停下来再补。
  setInterval(() => {
    if (!document.hidden) liveTick().catch(() => {});
  }, 1000);
  setInterval(() => {
    if (document.hidden) return;
    // iOS 的惯性可以比手指多滑一两秒，所以不是等一次就算：还在滑就 400ms 后再看，停下才动 DOM。
    const run = () => {
      if (scrolling()) {
        setTimeout(run, 400);
        return;
      }
      if (!document.hidden) refresh().catch((e) => toast(e.message));
    };
    run();
  }, 10000);
}

function renderShell() {
  app.innerHTML = `<header class="public-header">${brand}<nav><span class="live-label"><i></i><span class="live-text">LIVE STATUS</span></span>${themeButton()}<a href="/admin" class="button quiet">管理控制台 ↗</a></nav></header><main class="public-main"><section class="hero"><div><div class="eyebrow">YOUR INFRASTRUCTURE, AT A GLANCE</div><h1>每一台主机，<span>尽在眼前。</span></h1><p>实时状态、网络线路与用量，在一个清晰的视图里。</p></div><div class="hero-meta"><span>最近同步</span><strong id="updated">连接中</strong><small>每秒刷新</small></div></section><section id="overview" class="overview"></section><section class="toolbar"><div class="filter-controls"><div class="search"><span>⌕</span><input id="search" aria-label="搜索节点" placeholder="搜索名称、地区（支持拼音与缩写）"></div><select id="group-filter" aria-label="节点分组"><option value="">全部分组</option></select></div></section><div id="nodes" class="node-grid"></div><footer><span>独立 · 轻量 · 可控</span><span>TZA PROBE · V2</span></footer></main>`;

  document.querySelector("#search").oninput = (e) => {
    query = e.target.value;
    renderCards();
  };
  document.querySelector("#group-filter").onchange = (e) => {
    group = e.target.value;
    renderCards();
  };
}

async function refresh() {
  const ticket = ++generation;
  const next = await api("/nodes");
  if (ticket !== generation) return;
  // 同 liveTick：取数途中开始滑动就推迟——但等待期间不重复取数，还在滑就继续等。
  if (scrolling()) {
    const retry = () => {
      if (document.hidden) return;
      if (scrolling()) {
        setTimeout(retry, 400);
        return;
      }
      refresh().catch((e) => toast(e.message));
    };
    setTimeout(retry, 400);
    return;
  }
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

function speedsInner(n) {
  const m = n.metrics,
    isLive = n.online && m;
  const rateIn = isLive ? `${bytes(m.rate_in)}<span class="unit">/s</span>` : '<span class="unit-none">—</span>';
  const rateOut = isLive ? `${bytes(m.rate_out)}<span class="unit">/s</span>` : '<span class="unit-none">—</span>';
  return `<div class="speed-box"><span class="speed-icon teal-icon">↓</span><div><small>实时下载</small><strong>${rateIn}</strong></div></div><div class="speed-box"><span class="speed-icon orange-icon">↑</span><div><small>实时上传</small><strong>${rateOut}</strong></div></div>`;
}

// 滚动期间不改 DOM：WebKit（iOS 上的 Chrome / Safari）在滚动中改 DOM 会把位置弹回去。
let lastScrollAt = 0;
function watchScrolling() {
  const mark = () => {
    lastScrollAt = Date.now();
  };
  // scroll 不冒泡，用捕获阶段听 document，元素级滚动也能触发。
  document.addEventListener("scroll", mark, { passive: true, capture: true });
  window.addEventListener("touchmove", mark, { passive: true });
}
const scrolling = () => Date.now() - lastScrollAt < 1200;

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

// 每秒一次的轻刷新：只动数值——顶部四个数字、每张卡片的指标与速率、在线状态。不重建卡片列表，
// 也就不动滚动位置（重建那一轮走 renderCards，里面另有滚动锚定）。
let liveBusy = false;
async function liveTick() {
  if (liveBusy || scrolling()) return; // 上一拍没回来、或用户正在滑动，都跳过这一拍
  liveBusy = true;
  try {
    const next = await api("/nodes");
    // 取数期间用户可能刚起步滑动：写入前再查一次，别把手指下的内容动了（这一步是「还是跳」的漏洞）。
    if (scrolling()) return;
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
      // 结构一样就只写变化的文本/属性；结构真变了（行数增减）才兜底替换这一张卡的内容。
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

function cardInner(n) {
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
  const m = n.metrics,
    hasMetrics = !!m,
    isLive = n.online && m,
    expiry = days(n.expires_at);

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
      const latencyBars = sampleBars(history, (s) => s.latency, 200);
      const lossBars = sampleBars(
        history,
        (s) => (s.sent ? Math.max(0, (s.sent - s.received) / s.sent * 100) : null),
        20,
      );
      const recent = matches.map((o) => o.at).sort((a, b) => b - a)[0];
      const title = matches.length
        ? `${date(recent)} · 成功 ${received}/${sent} 次`
        : "尚未检测";
      return `<button class="route-label latency-label ${value == null ? "unknown" : "known"}" data-detail="${n.id}" data-focus-kind="latency" data-focus-carrier="${esc(carrier)}" data-focus-family="${effectiveFamily}" title="${esc(title)}"><span class="carrier-name">${carrier}</span><span class="quality-row latency-row"><span>延迟</span><strong class="${value == null ? "muted" : ""}">${value == null ? (matches.length ? "超时" : "未检测") : (value < 10 ? value.toFixed(1) : value.toFixed(0)) + " ms"}</strong></span><span class="quality-bars">${latencyBars}</span><span class="quality-row loss-row"><span>丢包</span><strong class="${loss == null ? "muted" : ""}">${loss == null ? "—" : loss.toFixed(1) + "%"}</strong></span><span class="quality-bars loss-bars">${lossBars}</span></button>`;
    })
    .join("");

  const routes = carriers
    .map((carrier) => {
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
      return `<button class="route-label ${known ? "known" : "unknown"}" data-detail="${n.id}" data-focus-kind="route" data-focus-carrier="${esc(carrier)}" data-focus-family="${effectiveFamily}" title="${esc(title)}"><span>${carrier}</span><strong class="${known ? "" : "muted"}">${esc(label)}</strong></button>`;
    })
    .join("");

  const familyLabel = effectiveFamily === "ipv4" ? "IPv4" : "IPv6";
  const networkSections = hasIPv4 || hasIPv6
    ? `<div class="route-heading"><span>TCP 延迟 <b>${familyLabel}</b></span><span class="rep-label">${esc(latencyRegion || "全部地区")}</span></div><div class="route-grid latency-grid">${latencies}</div><div class="route-heading"><span>三网回程 <b>${familyLabel}</b></span><span class="rep-label">${esc(n.representative || "自动匹配地区")}</span></div><div class="route-grid">${routes}</div>`
    : `<p class="hint network-unavailable">未检测到可用的公网 IP 地址，已跳过网络探测。</p>`;

  return `<div class="card-top"><div class="node-identity"><span class="node-dot ${n.online ? "" : "offline"}"></span><div><button class="node-name" data-detail="${n.id}">${esc(n.name)}</button><p>${esc(n.region || "未设置地区")}<span> · </span>${esc(n.group || "默认分组")}</p></div></div><div class="card-actions">${hasIPv4 && hasIPv6 ? `<div class="segmented card-family" aria-label="${esc(n.name)} IP 协议"><button data-card-family="ipv4" data-card-node="${n.id}" class="${effectiveFamily === "ipv4" ? "active" : ""}">IPv4</button><button data-card-family="ipv6" data-card-node="${n.id}" class="${effectiveFamily === "ipv6" ? "active" : ""}">IPv6</button></div>` : `<span class="family-tag">${hasIPv6 ? "IPv6" : hasIPv4 ? "IPv4" : "无探测"}</span>`}<span class="os-tag">${esc(m?.os || (n.online ? "在线" : "等待接入"))}</span></div></div>${`<div class="metrics-grid">${metricsInner(n)}</div><div class="speeds">${speedsInner(n)}</div>`}${networkSections}<div class="card-bottom"><span class="expiry-pill ${expiry != null && expiry <= 7 ? "warning" : ""}">${expiry == null ? "未设到期" : expiry < 0 ? "已过期 " + Math.abs(expiry) + " 天" : "剩余 " + expiry + " 天"}</span><span class="price-label">${n.price ? esc(n.currency || "CNY") + " " + n.price.toFixed(2) + " / " + esc(n.price_period || "月") : "未设置价格"}</span></div>`;
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
