import { api, send, esc, toast, modal, guard } from "./api.js";
import { bytes, date, days, percent, statusName } from "./format.js";
import { detail } from "./detail.js";
import { mountAdmin } from "./admin.js";
import { applyTheme, themeButton } from "./theme.js";
import "./ui-select.js";

const app = document.querySelector("#app");
const adminPage = location.pathname.startsWith("/admin");
let nodes = [],
  cardFamilies = new Map(),
  query = "",
  group = "",
  observations = new Map(),
  generation = 0;

export const brand = `<a class="brand" href="/" aria-label="TZA Probe 首页"><span class="brand-mark">t<span>z</span>a</span><span>PROBE<small>基础设施观测</small></span></a>`;

async function start() {
  applyTheme();
  if (adminPage) {
    await mountAdmin(app, brand);
    return;
  }
  renderShell();
  await refresh();
  setInterval(() => {
    if (!document.hidden) refresh().catch((e) => toast(e.message));
  }, 10000);
}

function renderShell() {
  app.innerHTML = `<header class="public-header">${brand}<nav><span class="live-label"><i></i><span class="live-text">LIVE STATUS</span></span>${themeButton()}<a href="/admin" class="button quiet">管理控制台 ↗</a></nav></header><main class="public-main"><section class="hero"><div><div class="eyebrow">YOUR INFRASTRUCTURE, AT A GLANCE</div><h1>每一台主机，<span>尽在眼前。</span></h1><p>实时状态、网络线路与用量，在一个清晰的视图里。</p></div><div class="hero-meta"><span>最近同步</span><strong id="updated">连接中</strong><small>每 10 秒刷新</small></div></section><section id="overview" class="overview"></section><section class="toolbar"><div class="filter-controls"><div class="search"><span>⌕</span><input id="search" aria-label="搜索节点" placeholder="搜索名称、地区…"></div><select id="group-filter" aria-label="节点分组"><option value="">全部分组</option></select></div></section><div id="nodes" class="node-grid"></div><footer><span>独立 · 轻量 · 可控</span><span>TZA PROBE · V2</span></footer></main>`;

  document.querySelector("#search").oninput = (e) => {
    query = e.target.value.toLowerCase();
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
  nodes = next;
  const groups = [...new Set(nodes.map((n) => n.group).filter(Boolean))];
  if (group && !groups.includes(group)) group = "";
  document.querySelector("#group-filter").innerHTML =
    `<option value="">全部分组</option>` +
    groups
      .map((g) => `<option ${g === group ? "selected" : ""}>${esc(g)}</option>`)
      .join("");
  document.querySelector("#updated").textContent =
    new Date().toLocaleTimeString("zh-CN", { hour12: false });
  const online = nodes.filter((n) => n.online),
    traffic = nodes.reduce((v, n) => v + n.traffic_used, 0),
    rateIn = online.reduce((v, n) => v + (n.metrics?.rate_in || 0), 0),
    rateOut = online.reduce((v, n) => v + (n.metrics?.rate_out || 0), 0);

  document.querySelector("#overview").innerHTML =
    `<div class="overview-tile"><div class="tile-header"><span>在线节点</span><i class="dot ${online.length ? "active" : ""}"></i></div><strong>${online.length}<small> / ${nodes.length}</small></strong></div><div class="overview-tile"><div class="tile-header"><span>实时下载</span><span class="tile-badge teal-badge">↓</span></div><strong>${bytes(rateIn)}<small>/s</small></strong></div><div class="overview-tile"><div class="tile-header"><span>实时上传</span><span class="tile-badge orange-badge">↑</span></div><strong>${bytes(rateOut)}<small>/s</small></strong></div><div class="overview-tile"><div class="tile-header"><span>本周期用量合计</span><span class="tile-badge purple-badge">⚡</span></div><strong>${bytes(traffic)}</strong></div>`;

  renderCards();

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
  if (ticket === generation) renderCards();
}

function meter(label, value, sub = "") {
  return `<div class="metric"><div><span>${label}</span><strong>${value == null ? "—" : value.toFixed(1) + "%"}</strong></div><progress max="100" value="${value ?? 0}" aria-label="${label}"></progress><small>${esc(sub)}</small></div>`;
}

function renderCards() {
  const filtered = nodes.filter(
    (n) =>
      (!group || n.group === group) &&
      `${n.name} ${n.region}`.toLowerCase().includes(query),
  );
  document.querySelector("#nodes").innerHTML = filtered.length
    ? filtered.map(card).join("")
    : `<div class="empty"><span class="empty-symbol">◎</span><h2>${nodes.length ? "没有匹配的节点" : "尚无公开节点"}</h2><p>${nodes.length ? "换一个关键词或分组试试。" : "在管理控制台添加节点，并开启公开展示。"}</p></div>`;
  for (const b of document.querySelectorAll("[data-detail]"))
    b.onclick = guard(() => detail(b.dataset.detail));
  for (const b of document.querySelectorAll("[data-card-family]"))
    b.onclick = () => { cardFamilies.set(b.dataset.cardNode, b.dataset.cardFamily); renderCards(); };
}

function card(n) {
  const cardFamily = cardFamilies.get(n.id) || "ipv4";
  const m = n.metrics,
    hasMetrics = !!m,
    isLive = n.online && m,
    expiry = days(n.expires_at);

  const obs = (observations.get(n.id) || []).filter(
    (o) => o.task.family === cardFamily && o.task.enabled,
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
      const latencyBars = qualityBars(value, 200);
      const lossBars = qualityBars(loss, 20, true);
      const recent = matches.map((o) => o.at).sort((a, b) => b - a)[0];
      const title = matches.length
        ? `${date(recent)} · 成功 ${received}/${sent} 次`
        : "尚未检测";
      return `<button class="route-label latency-label ${value == null ? "unknown" : "known"}" data-detail="${n.id}" title="${esc(title)}"><span class="carrier-name">${carrier}</span><span class="quality-row"><span>延迟</span><strong class="${value == null ? "muted" : ""}">${value == null ? (matches.length ? "超时" : "未检测") : (value < 10 ? value.toFixed(1) : value.toFixed(0)) + " ms"}</strong></span><span class="quality-bars">${latencyBars}</span><span class="quality-row"><span>丢包</span><strong class="${loss == null ? "muted" : ""}">${loss == null ? "—" : loss.toFixed(1) + "%"}</strong></span><span class="quality-bars loss-bars">${lossBars}</span></button>`;
    })
    .join("");

  const routes = carriers
    .map((carrier) => {
      const match = obs
        .filter(
          (o) =>
            o.task.kind === "route" &&
            o.task.family === cardFamily &&
            (!n.representative || o.task.region === n.representative) &&
            o.task.carrier === carrier,
        )
        .sort((a, b) => b.at - a.at)[0];
      const known =
        match && !match.stale && match.result.status === "ok" && n.online;
      const region = match?.task.region || n.representative || "未设代表地区";
      return `<button class="route-label ${known ? "known" : "unknown"}" data-detail="${n.id}" title="${esc(match ? region + " · " + date(match.at) + " · " + (statusName[match.result.status] || "未知") : "尚未检测")}"><span>${carrier}</span><strong class="${known ? "" : "muted"}">${esc(known ? match.result.route : "未知")}</strong></button>`;
    })
    .join("");

  const cpuSub = hasMetrics ? "系统计算负载" : "等待采集";
  const memSub = hasMetrics ? `${bytes(m.memory_used)} / ${bytes(m.memory_total)}` : "未报告";
  const diskSub = hasMetrics ? `${bytes(m.disk_used)} / ${bytes(m.disk_total)}` : "未报告";
  const trafficSub = n.traffic_limit ? `${bytes(n.traffic_limit)} 额度 · ${esc({ sum: "双向", max: "取峰值", in: "仅下载", out: "仅上传" }[n.traffic_mode])}` : "无限制额度";

  const rateInStr = isLive ? `${bytes(m.rate_in)}<span class="unit">/s</span>` : '<span class="unit-none">—</span>';
  const rateOutStr = isLive ? `${bytes(m.rate_out)}<span class="unit">/s</span>` : '<span class="unit-none">—</span>';

  return `<article class="node-card ${n.online ? "is-online" : "is-offline"}><div class="card-top"><div class="node-identity"><span class="node-dot ${n.online ? "" : "offline"}"></span><div><button class="node-name" data-detail="${n.id}">${esc(n.name)}</button><p>${esc(n.region || "未设置地区")}<span> · </span>${esc(n.group || "默认分组")}</p></div></div><div class="card-actions"><div class="segmented card-family" aria-label="${esc(n.name)} IP 协议"><button data-card-family="ipv4" data-card-node="${n.id}" class="${cardFamily === "ipv4" ? "active" : ""}">IPv4</button><button data-card-family="ipv6" data-card-node="${n.id}" class="${cardFamily === "ipv6" ? "active" : ""}">IPv6</button></div><span class="os-tag">${esc(m?.os || (n.online ? "在线" : "等待接入"))}</span></div></div><div class="metrics-grid">${meter("CPU", hasMetrics ? m.cpu : null, cpuSub)}${meter("内存", hasMetrics ? percent(m.memory_used, m.memory_total) : null, memSub)}${meter("磁盘", hasMetrics ? percent(m.disk_used, m.disk_total) : null, diskSub)}<div class="metric"><div><span>周期用量</span><strong>${bytes(n.traffic_used)}</strong></div><progress max="100" value="${percent(n.traffic_used, n.traffic_limit)}" aria-label="周期流量额度"></progress><small>${trafficSub}</small></div></div><div class="speeds"><div class="speed-box"><span class="speed-icon teal-icon">↓</span><div><small>实时下载</small><strong>${rateInStr}</strong></div></div><div class="speed-box"><span class="speed-icon orange-icon">↑</span><div><small>实时上传</small><strong>${rateOutStr}</strong></div></div></div><div class="route-heading"><span>TCP 延迟 <b>${family === "ipv4" ? "IPv4" : "IPv6"}</b></span><span class="rep-label">${esc(latencyRegion || "全部地区")}</span></div><div class="route-grid latency-grid">${latencies}</div><div class="route-heading"><span>三网回程 <b>${family === "ipv4" ? "IPv4" : "IPv6"}</b></span><span class="rep-label">${esc(n.representative || "自动匹配地区")}</span></div><div class="route-grid">${routes}</div><div class="card-bottom"><span class="expiry-pill ${expiry != null && expiry <= 7 ? "warning" : ""}">${expiry == null ? "未设到期" : expiry < 0 ? "已过期 " + Math.abs(expiry) + " 天" : "剩余 " + expiry + " 天"}</span><span class="price-label">${n.price ? esc(n.currency || "CNY") + " " + n.price.toFixed(2) + " / " + esc(n.price_period || "月") : "未设置价格"}</span></div></article>`;
}

function qualityBars(value, ceiling, inverse = false) {
  const count = value == null ? 0 : Math.max(0, Math.min(20, Math.round((value / ceiling) * 20)));
  return Array.from({ length: 20 }, (_, i) => {
    const active = inverse ? i >= 20 - count : i < count;
    const level = inverse ? (value > 5 ? "bad" : value > 1 ? "warn" : "good") : (value > ceiling * .75 ? "bad" : value > ceiling * .4 ? "warn" : "good");
    return `<i class="quality-bar ${active ? level : "idle"}"></i>`;
  }).join("");
}

start().catch((e) => {
  app.innerHTML = `<main class="empty"><h1>暂时无法连接</h1><p>${esc(e.message)}</p><a class="button" href="${adminPage ? "/admin" : "/"}">重新加载</a></main>`;
});
