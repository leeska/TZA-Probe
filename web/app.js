import { api, send, esc, toast, modal, guard } from "./api.js";
import { bytes, date, days, gib, mbps, osLabel, percent, statusName } from "./format.js";
import { detail } from "./detail.js";
import { mountAdmin } from "./admin.js";
import { applyTheme, themeButton } from "./theme.js";
import { applySkin, skinButton } from "./skins.js";
import { searchMatch } from "./search.js";
import { carrierMark } from "./carrier-logos.js";
import { unlockBadge, unlockCatalog, unlockPanel } from "./unlock.js";
import { castId } from "./anime-cast.js";
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
  applySkin();
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
  app.innerHTML = `<header class="public-header">${brand}<nav><span class="live-label"><i></i><span class="live-text">LIVE STATUS</span></span>${skinButton()}${themeButton()}<a href="/admin" class="button quiet">管理控制台 ↗</a></nav></header><main class="public-main"><section class="hero"><div><div class="eyebrow">YOUR INFRASTRUCTURE, AT A GLANCE</div><h1>每一台主机，<span>尽在眼前。</span></h1><p>实时状态、网络线路与用量，在一个清晰的视图里。</p></div><div class="hero-meta"><span>最近同步</span><strong id="updated">连接中</strong><small>每秒刷新</small></div></section><section id="overview" class="overview"></section><section class="toolbar"><div class="filter-controls"><div class="search"><span>⌕</span><input id="search" aria-label="搜索节点" placeholder="搜索名称、地区（支持拼音与缩写）"></div><select id="group-filter" aria-label="节点分组"><option value="">全部分组</option></select></div><div class="view-bar">${viewSwitch()}</div></section><div id="nodes" class="node-grid"></div><footer><span>独立 · 轻量 · 可控</span><span>TZA PROBE · V2</span></footer></main>`;

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

  // 解锁目录是全站固定的一份（名称 / 分类 / 是否只报地区），先取到再画卡片：徽标上的分数
  // 来自节点自己的 unlock 字段，分类浮层还要目录才认得出每一项的名字与分类。
  catalog = await unlockCatalog();

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
          // slim：卡片只画回程标签，不需要跳表（那部分占了九成体积）。
          const data = await api(`/nodes/${n.id}/observations?slim=1`);
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
  // CPU 那格的副标给核心数（Agent 报的 runtime.NumCPU），老 Agent 没报就退回原来的说明文字。
  const cpuSub = hasMetrics ? (m.cpu_cores ? `${m.cpu_cores} 核` : "系统计算负载") : "等待采集";
  // 内存与磁盘按 1024 换算（gib）：这两个量商家按 GiB 卖、`df -h` / `free -h` 也给 GiB；
  // 速率与流量仍按十进制（bytes），与商家的流量口径一致。
  const memSub = hasMetrics ? `${gib(m.memory_used)} / ${gib(m.memory_total)}` : "未报告";
  const diskSub = hasMetrics ? `${gib(m.disk_used)} / ${gib(m.disk_total)}` : "未报告";
  const trafficSub = n.traffic_limit ? `${bytes(n.traffic_limit)} 额度 · ${esc({ sum: "双向", max: "取峰值", in: "仅下载", out: "仅上传" }[n.traffic_mode])}` : "无限制额度";
  // 达量降速：线路自带的两条规则都可能——按窗口平均带宽（德国商家）或按周期额度用满后
  // 限速（DMIT T1 这类不计量但限速）。只有真配了规则的线路才写这一行，没配的机器不显示
  // 占位文案；窗口口径按本机实际上报的小时数写，覆盖不满就说还在统计，不假装有一整个窗口。
  const throttleNote =
    n.throttle_mbps > 0
      ? `<small class="throttle-note${n.throttled ? " over" : ""}">${
          n.throttle_on === "quota"
            ? `${n.throttled ? "已用满额度" : `用满 ${bytes(n.traffic_limit)} 额度`} · 限速 ${n.throttle_mbps} Mbit/s`
            : `${
                n.covered_hours > 0
                  ? `近 ${Math.min(n.covered_hours, n.window_hours || 24)} 小时均值 ${mbps(n.avg_bps)}`
                  : "近 " + (n.window_hours || 24) + " 小时均值统计中"
              } · 阈值 ${n.throttle_mbps} Mbit/s`
        }</small>`
      : "";
  return `${meter("CPU", hasMetrics ? m.cpu : null, cpuSub)}${meter("内存", hasMetrics ? percent(m.memory_used, m.memory_total) : null, memSub)}${meter("磁盘", hasMetrics ? percent(m.disk_used, m.disk_total) : null, diskSub)}<div class="metric"><div><span>周期用量</span><strong>${bytes(n.traffic_used)}</strong></div><progress max="100" value="${percent(n.traffic_used, n.traffic_limit)}" aria-label="周期流量额度"></progress><small>${trafficSub}</small>${throttleNote}</div>`;
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

// 系统标签：优先显示 Agent 报的发行版（见 format.js 的 osLabel），老 Agent 没有这个字段
// 就退回 runtime.GOOS（linux）。架构单独一个标签，因为它要在列表里换到发行版下面那一行；
// 写进 os-tag 里会被每秒刷新的 setText(.os-tag) 整段覆盖掉。
function osTagsInner(n) {
  const m = n.metrics;
  const os = esc(osLabel(n));
  const arch = m?.arch ? esc(m.arch) : "";
  // 两个标签套一层 .os-box：卡片里是一行两个，列表里那层改成竖排，架构就落在发行版下面
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
      setText(el.querySelector(".os-tag"), osLabel(n));
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
  // 卷轴状态每次渲染后刷一遍：卡壳上的 data-rolled 决定了数据区是摊开还是卷起，
  // 局部补丁不会动它，但整块重绘换了新卡壳，所以在这里统一补上（含 aria-expanded）。
  applyFolded();
  for (const c of document.querySelectorAll(".node-card"))
    c.onclick = guard((e) => {
      // 卡片里已有自己行为的控件（协议开关、格子、节点名）不触发整卡的综合信息
      if (e.target.closest("button, a, select, input, [data-detail]")) return;
      detail(c.dataset.node);
    });
  for (const b of document.querySelectorAll("[data-card-family]"))
    b.onclick = () => { cardFamilies.set(b.dataset.cardNode, b.dataset.cardFamily); renderCards(); };
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

// 二次元皮肤的「卷轴」：把卡片卷起来——数据收进卷轴，露出她举牌时挡住的下半身。
// 收起状态挂在**卡壳**上（`.node-card[data-rolled="true"]`），不是重绘出来的内容：
// 每秒轻刷新与 10 秒全量刷新都走补丁路径（只改文本/属性、不换元素），卡壳上的属性不会被它们擦掉；
// 每次渲染后再统一 applyFolded() 刷一遍，落库的是角色 id（节点名小写），刷新页面也还卷着。
const FOLD_KEY = "tza-anime-rolled";
const FOLD_ICON =
  '<svg viewBox="0 0 16 16" aria-hidden="true" focusable="false"><path d="M4 10.2 8 6.2l4 4" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>';
const foldedCasts = new Set(
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
    localStorage.setItem(FOLD_KEY, JSON.stringify([...foldedCasts]));
  } catch {
    /* 隐私模式写不进去，忽略：这一轮的状态仍在内存里 */
  }
}
// 卡壳上的状态 + 两个控制件的 aria-expanded：卷起后由卷轴本身展开，展开时由立绘右上角的胶囊卷起。
function applyFolded() {
  for (const card of document.querySelectorAll(".node-card[data-cast]")) {
    const rolled = foldedCasts.has(card.dataset.cast);
    if (rolled) card.dataset.rolled = "true";
    else delete card.dataset.rolled;
    for (const btn of card.querySelectorAll("[data-fold]"))
      btn.setAttribute("aria-expanded", String(!rolled));
  }
}
function toggleFolded(cast) {
  if (!cast) return;
  foldedCasts.has(cast) ? foldedCasts.delete(cast) : foldedCasts.add(cast);
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

// 裸眼 3D：指针在卡上移动时，把指针相对卡心的位置写成两个**无单位**变量（-1…1）挂到卡壳上，
// anime.css 用一个量同时算「卡片转几度」和「立绘 / 卷轴多走几像素」——层间差速就是立体感的来源。
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
    const card = e.target?.closest?.(".node-card[data-cast]");
    for (const other of document.querySelectorAll(".node-card[data-cast].is-tilt"))
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
    const card = e.target?.closest?.(".node-card[data-cast]");
    if (card && !card.contains(e.relatedTarget)) untiltCard(card);
  },
  { passive: true },
);

// 卡壳与内容分开：整块重绘用 card()，逐张替换只能喂 cardInner()——直接把整张卡赋给 innerHTML，
// 卡片会把自己嵌进自己里面（外层 article 里再套一个同名 article，页面高度和位置都会跟着错）。
const cardShell = (n, inner) => {
  // 二次元皮肤：卡壳上也挂一份角色 id。立绘层 `.anime-stage` 是卡片**内部**的元素，
  // 卡片自己读不到它的 data-cast，而皮肤样式表要按角色给整张牌面上色
  // （`.node-card[data-cast="…"]`），所以这里再挂一份——两处都由 castId() 出，不会漂。
  const cast = castId(n);
  return `<article class="node-card ${n.online ? "is-online" : "is-offline"}" data-node="${n.id}"${cast ? ` data-cast="${cast}"` : ""}>${inner}</article>`;
};

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

  // 观测（结果）只用来填数值：这一族、已启用的任务才有最新结果。
  const obs = allObs.filter(
    (o) => o.task.family === effectiveFamily && o.task.enabled,
  );
  // 延迟按运营商分格显示，地区取节点的延迟地区，其次代表地区，都未设则不限地区。
  // 目录外的自定义目标（以及没有运营商的旧任务）归入“其他”。
  const carriers = ["电信", "联通", "移动"];
  const carrierOf = (o) =>
    carriers.includes(o.task.carrier) ? o.task.carrier : "其他";
  const bucketOf = (carrier) =>
    carriers.includes(carrier) ? carrier : "其他";
  const latencyRegion = n.latency_region || n.representative || "";
  const inRegion = (region, want) => !want || region === want;
  // 一格的资格来自服务端给的 probe_targets（这台机器真有那些探测项，取自任务定义），
  // 不是从 jobs 推的：新绑的任务要等 Agent 认领才有行，从 jobs 推会让卡片先画空再长出来。
  // 哪组没有任务就不给那格——以前固定画满电信 / 联通 / 移动 / 其他，没挂任务的机器
  // 整天是一块「未检测 + 灰色空条」的骨架，看着像没加载完。后台的「公开卡片显示延迟」
  // 只在这个集合里再筛一道。
  const plan = n.probe_targets || [];
  const scoped = plan.filter((t) => t.family === effectiveFamily);
  const latencyBuckets = [...carriers, "其他"].filter(
    (c) =>
      scoped.some(
        (t) =>
          t.kind === "tcp" &&
          inRegion(t.region, latencyRegion) &&
          bucketOf(t.carrier) === c,
      ) && (!n.latency_carriers?.length || n.latency_carriers.includes(c)),
  );
  // 回程只有三网三格（自定义目标不参与回程），同一套资格判断。
  const routeBuckets = carriers.filter((c) =>
    scoped.some(
      (t) =>
        t.kind === "route" &&
        inRegion(t.region, n.representative) &&
        t.carrier === c,
    ),
  );
  const tcp = obs.filter(
    (o) =>
      n.online &&
      o.task.kind === "tcp" &&
      !o.stale &&
      o.result.status !== "pending" &&
      inRegion(o.task.region, latencyRegion),
  );
  const latencies = latencyBuckets
    .map((carrier) => {
      const matches = tcp.filter((o) => carrierOf(o) === carrier);
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
        // 原始样本交给渲染函数：列表视图画最近 20 次的延迟色带，卡片视图画「延迟 + 丢包」两
        // 条各 10 格的小色带，两边取的都是这一份。
        history,
        title: matches.length
          ? `${date(recent)} · 成功 ${received}/${sent} 次`
          : "尚未检测",
      };
    });

  const routes = routeBuckets.map((carrier) => {
    const match = obs
      .filter(
        (o) =>
          o.task.kind === "route" &&
          inRegion(o.task.region, n.representative) &&
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
  return {
    hasIPv4,
    hasIPv6,
    effectiveFamily,
    familyLabel: effectiveFamily === "ipv4" ? "IPv4" : "IPv6",
    latencyRegion,
    latencies,
    routes,
    hasProbe,
    probeState,
    expiry: days(n.expires_at),
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
// 价格：0 价且周期写着「免费」就写「免费」，不再说成「未设置价格」。
function expiryPill(n, expiry) {
  return expiry == null
    ? `<span class="expiry-pill">未设到期</span>`
    : expiry < 0
      ? `<span class="expiry-pill warning">已过期 ${Math.abs(expiry)} 天</span>`
      : expiry > LONG_TERM_DAYS
        ? `<span class="expiry-pill" title="到期 ${esc(String(n.expires_at || "").slice(0, 10))}">长期</span>`
        : `<span class="expiry-pill${expiry <= 7 ? " warning" : ""}">剩余 ${expiry} 天</span>`;
}

function priceLabel(n) {
  const price = n.price
    ? `${esc(n.currency || "CNY")} ${n.price.toFixed(2)} / ${esc(n.price_period || "月")}`
    : /免费/.test(n.price_period || "")
      ? "免费"
      : "未设置价格";
  return `<span class="price-label">${price}</span>`;
}

function billingInner(n, expiry) {
  return `${expiryPill(n, expiry)}${priceLabel(n)}`;
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
  return `<div class="card-stats"><div class="speeds">${speedsInner(n)}</div>${totals}<div class="stat-facts">${expiryPill(n, f.expiry)}${priceLabel(n)}</div></div>`;
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

function networkSections(n, f) {
  // 一个探测项都没有的机器（按设计不挂任务）：不管它报没报地址族，都只写这一行。
  if (f.probeState === "none") return probeNote("none");
  if (!f.hasIPv4 && !f.hasIPv6)
    return `<p class="hint network-unavailable">未检测到可用的公网 IP 地址，已跳过网络探测。</p>`;
  if (!f.hasProbe) return probeNote(f.probeState);
  // 延迟与回程在这里各占一段：延迟是一家运营商一块（文字在上、延迟与丢包两条色带并排在下），
  // 回程仍是三个并排的胶囊。
  return `${f.latencies.length ? `<div class="route-heading"><span>TCP 延迟 <b>${f.familyLabel}</b></span>${f.latencyRegion ? `<span class="rep-label">${esc(f.latencyRegion)}</span>` : ""}</div><div class="latency-bands">${latencyBands(n, f)}</div>` : ""}${f.routes.length ? `<div class="route-heading"><span>三网回程 <b>${f.familyLabel}</b></span>${n.representative ? `<span class="rep-label">${esc(n.representative)}</span>` : ""}</div><div class="route-grid">${routeCells(n, f)}</div>` : ""}`;
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
      return `<button class="route-label ${r.known ? "known" : "unknown"}" data-detail="${n.id}" data-focus-kind="route" data-focus-carrier="${esc(r.carrier)}" data-focus-family="${f.effectiveFamily}" title="${esc(r.title)}" aria-label="${esc(`${r.carrier} ${r.label}`)}">${carrierMark(r.carrier)}<strong class="${r.known ? "" : "muted"}">${label}</strong></button>`;
    })
    .join("");
}

// 列表视图的行内文本：一家运营商一行，延迟、丢包与最近 BAR_COUNT 次的色带横向排开。
// 回程标签在这里不截断（卡片那格为保持格子高度会截到两行、完整值放在 title 里），
// 列表的要求是文字全给出来：长标签换行，不省略。
// 行首与回程行一样放官方标志（用户点名：前面的「电信 / 联通 / 移动」也换成 logo——这样
// 同一行右边的回程行、卡片里的延迟块、列表里的延迟行三处口径一致）；名字仍在 aria-label /
// title / data-focus-carrier 里，点选与筛选照旧按 carrier 走。
function latencyLines(n, f) {
  return f.latencies
    .map(
      (l) =>
        `<button class="net-line ${l.value == null ? "unknown" : "known"}" data-detail="${n.id}" data-focus-kind="latency" data-focus-carrier="${esc(l.carrier)}" data-focus-family="${f.effectiveFamily}" title="${esc(l.title)}" aria-label="${esc(`${l.carrier} ${l.value == null ? l.missText : l.text} 丢包 ${l.lossText}`)}"><span class="carrier-name">${carrierMark(l.carrier)}</span><strong class="${l.value == null ? "muted" : ""}">${l.value == null ? l.missText : l.text}</strong><span class="net-loss">丢包 ${l.lossText}</span><span class="quality-bars" title="最近 ${BAR_COUNT} 次延迟">${sampleBars(l.history, (s) => s.latency, 200, BAR_COUNT)}</span></button>`,
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
const lossPercent = (s) => (s.sent ? Math.max(0, (s.sent - s.received) / s.sent * 100) : null);

function latencyBands(n, f) {
  return f.latencies
    .map(
      (l) =>
        `<button class="net-band ${l.value == null ? "unknown" : "known"}" data-detail="${n.id}" data-focus-kind="latency" data-focus-carrier="${esc(l.carrier)}" data-focus-family="${f.effectiveFamily}" title="${esc(l.title)}" aria-label="${esc(`${l.carrier} 延迟 ${l.value == null ? l.missText : l.text} 丢包 ${l.lossText}`)}"><span class="band-head"><span class="carrier-name">${carrierMark(l.carrier)}</span><span class="band-metric"><i>延迟</i><strong class="${l.value == null ? "muted" : ""}">${l.value == null ? l.missText : l.text}</strong></span><span class="band-metric"><i>丢包</i><strong class="${l.loss == null ? "muted" : ""}">${l.lossText}</strong></span></span><span class="band-bars"><span class="quality-bars" title="最近 ${BAR_COUNT} 次延迟">${sampleBars(l.history, (s) => s.latency, 200, BAR_COUNT)}</span><span class="quality-bars loss-bars" title="最近 ${BAR_COUNT} 次丢包">${sampleBars(l.history, lossPercent, 20, BAR_COUNT)}</span></span></button>`,
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
        `<button class="net-line route-line ${r.known ? "known" : "unknown"}" data-detail="${n.id}" data-focus-kind="route" data-focus-carrier="${esc(r.carrier)}" data-focus-family="${f.effectiveFamily}" title="${esc(r.title)}" aria-label="${esc(`${r.carrier} ${r.label}`)}"><span class="carrier-name">${carrierMark(r.carrier)}</span><strong class="${r.known ? "" : "muted"}">${esc(r.label)}</strong></button>`,
    )
    .join("");
}

function gridInner(n) {
  const f = nodeFacts(n);
  // 二次元皮肤（data-skin="anime"）的线路机立绘：只有名单里的节点才渲染这一层，
  // 其余皮肤下 .anime-stage 是 display:none（见 styles.css）。图片走样式表的 [data-cast] 规则。
  // data-bleed = 通铺到卡片边框的装饰层：允许越过卡片内边距（被 overflow: hidden 裁掉是设计意图），
  // 但越过边框 Box 就还是越界——scripts/card-overflow-check.mjs 按这两个标记区分。
  const cast = castId(n);
  // 折叠两件（只有名单里的节点才渲染）：立绘右上角的「卷起」胶囊、收起后印在她手里那块牌子上的名字。
  // 牌子是**画进立绘里的贴图**（2026-10-09 起），所以这里只剩一层字，不再有 UI 画的纸带。
  // 可见性全在 anime.css 里按卡壳上的 data-rolled 切，这里只负责摆出来。
  const fold = cast
    ? `<button class="anime-fold-btn" type="button" data-fold="${cast}" data-bleed aria-label="卷起卡片" aria-expanded="true">${FOLD_ICON}<span>卷起</span></button>` +
      `<button class="anime-roll" type="button" data-fold="${cast}" data-bleed aria-label="展开卡片" aria-expanded="true">` +
      `<span class="anime-roll-label" data-bleed><span class="node-dot anime-roll-dot ${n.online ? "" : "offline"}"></span><span class="anime-roll-name">${esc(n.name)}</span></span>` +
      `</button>`
    : "";
  // 头部两行：第一行只有状态点 + 名字，右侧是发行版与架构胶囊；地区 / 分组与地址族开关落到第二行。
  // 以前地址族开关、系统、架构三样都挤在第一行，卡片窄时名字被挤成三行（手机上一张卡的头部 104px）。
  // 下面紧接着的一条信息带（实时速率 + 到期 + 价格）取代了原来的两个速率方块与独立页脚。
  return `${cast ? `<div class="anime-stage" data-cast="${cast}" data-bleed role="img" aria-label="线路机角色立绘"><span class="anime-blink" data-bleed aria-hidden="true"></span></div>${fold}` : ""}<div class="card-top"><div class="node-identity"><span class="node-dot ${n.online ? "" : "offline"}"></span><button class="node-name" data-detail="${n.id}">${esc(n.name)}</button></div><div class="card-actions">${unlockBadge(n)}${osTagsInner(n)}</div></div><div class="node-sub"><p>${esc(n.region || "未设置地区")}<span> · </span>${esc(n.group || "默认分组")}</p>${familySwitch(n, f)}</div><div class="metrics-grid">${metricsInner(n)}</div>${statsBand(n, f)}${networkSections(n, f)}`;
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
  const netCell =
    f.probeState === "none"
      ? probeNote("none")
      : !detected
        ? `<p class="hint network-unavailable">未检测到可用的公网 IP 地址，已跳过网络探测。</p>`
        : f.latencies.length
          ? head("TCP 延迟", f.latencyRegion) + latencyLines(n, f)
          : f.hasProbe
            ? ""
            : probeNote(f.probeState);
  const routeCell =
    detected && f.routes.length
      ? head("三网回程", n.representative) + routeLines(n, f)
      : "";
  return `<div class="row-identity"><div class="node-identity"><span class="node-dot ${n.online ? "" : "offline"}"></span><div><button class="node-name" data-detail="${n.id}">${esc(n.name)}</button><p>${esc(n.region || "未设置地区")}<span> · </span>${esc(n.group || "默认分组")}</p></div></div><div class="card-actions">${familySwitch(n, f)}${osTagsInner(n)}</div></div><div class="metrics-grid">${metricsInner(n)}</div><div class="speeds-cell"><div class="route-heading"><span>实时网速</span></div><div class="speeds">${speedsInner(n)}</div></div><div class="net-cell">${netCell}</div><div class="route-cell">${routeCell}</div><div class="billing-cell">${billingInner(n, f.expiry)}</div>`;
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
