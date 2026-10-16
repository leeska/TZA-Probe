import { api, esc, modal, guard } from "./api.js";
import { bytes, bandwidth, date, mbps, osLabel, statusLabel, statusName, reasonName } from "./format.js";
import { chart, wireCharts, colorClass, axisRow, formatValue, niceCeiling, stamp } from "./charts.js";
import { unlockCatalog, unlockPanel } from "./unlock.js";
// 详情页要发三个请求，任何一个失败或长时间无响应，弹窗都会停在“读取节点详情…”：
// 每个请求都带截止时间，失败时把原因和重试按钮写进弹窗。
const requestTimeout = 15000;
function withDeadline(send) {
  let timer;
  const timeout = new Promise((_, reject) => {
    timer = setTimeout(
      () =>
        reject(
          Object.assign(new Error(`${requestTimeout / 1000} 秒内没有响应`), {
            name: "TimeoutError",
          }),
        ),
      requestTimeout,
    );
  });
  const pending = send();
  // 超时后原请求仍会返回，别让它变成未处理的拒绝。
  pending.catch(() => {});
  return Promise.race([pending, timeout]).finally(() => clearTimeout(timer));
}
function failureReason(error) {
  return (error && error.message) || "未知错误";
}
function showFailure(dialog, id, focus, error) {
  dialog.querySelector(".loading")?.remove();
  const box = document.createElement("div");
  box.className = "detail-error";
  const text = document.createElement("p");
  text.textContent = `读取节点详情失败：${failureReason(error)}`;
  const retry = document.createElement("button");
  retry.type = "button";
  retry.textContent = "重试";
  retry.onclick = () => {
    box.remove();
    detail(id, focus);
  };
  box.append(text, retry);
  dialog.append(box);
}
// 一个探测目标的显示名：三网写「上海 · 电信」；三网之外（运营商「其他」）的目标地区就是服务名
//（Google、Telegram DC1），后面再挂「· 其他」只是噪音，只写地区。
function targetName(t) {
  const carrier = t.carrier || "自定义";
  if (t.region && (carrier === "其他" || carrier === "自定义")) return t.region;
  return [t.region || t.name, carrier].filter(Boolean).join(" · ") || "网络目标";
}

// focus 为空时展示整台机器的综合信息；带 {kind, carrier} 时只展示那一格的详情：
// 延迟格看该运营商的 TCP 延迟，回程格看该运营商的回程路径。
export async function detail(id, focus = null) {
  // 明细请求不必等节点列表回来再发：和列表同时出发，弹窗打开时数据多半已经在路上了。
  const loads = Promise.all([
    api(`/nodes/${id}/history?hours=24&slim=1`),
    api(`/nodes/${id}/observations`),
    api(`/nodes/${id}/network-history?hours=24`),
    // 解锁目录（名称 / 分类）全站一份，取一次就缓存，用来画「解锁检测」那一块。
    unlockCatalog(),
  ]);
  // 节点不在列表里时下面直接返回，这批请求的失败不该变成未处理的拒绝。
  loads.catch(() => {});
  const list = await api("/nodes");
  const n = list.find((x) => x.id === id);
  if (!n) {
    // 节点可能刚被删掉或改了可见性，此时列表里已经没有它：说清楚，别留一个空弹窗。
    const open = document.querySelector(`#dialog[data-node-id="${id}"]`);
    if (open && open.open) showFailure(open, id, focus, new Error("节点已不在列表里，可能已被删除或不再公开"));
    return;
  }
  const d = modal(n.name, `<div class="loading">读取节点详情…</div>`);
  d.dataset.nodeId = id;
  let history, obs, network, catalog;
  try {
    [history, obs, network, catalog] = await withDeadline(() => loads);
  } catch (error) {
    if (!d.open || d.dataset.nodeId !== id) return;
    console.error("节点详情加载失败", `/api/v1/nodes/${id}`, error);
    showFailure(d, id, focus, error);
    return;
  }
  if (!d.open || d.dataset.nodeId !== id) return;
  const reported = Array.isArray(n.ip_families) ? new Set(n.ip_families) : null;
  const hasIPv4 = reported
    ? reported.has("ipv4")
    : obs.some((o) => o.task.family === "ipv4");
  const hasIPv6 = reported
    ? reported.has("ipv6")
    : obs.some((o) => o.task.family === "ipv6");
  // 与公开面板同一套：有几族给几段（单栈就一段，不摆点不动的灰段）。
  const familyControl =
    !hasIPv4 && !hasIPv6
      ? `<span class="family-tag">无探测</span>`
      : `<div class="segmented" role="group" aria-label="IP 协议">${hasIPv4 ? `<button data-detail-family="ipv4" class="active">IPv4</button>` : ""}${hasIPv6 ? `<button data-detail-family="ipv6" class="${hasIPv4 ? "" : "active"}">IPv6</button>` : ""}</div>`;
  // 点击卡片上的某一格时只展示那一格的详情：延迟格看该运营商 TCP 延迟，
  // 回程格看该运营商回程路径；点整张卡片才给综合信息。
  const focused = !!focus && (focus.kind === "latency" || focus.kind === "route");
  const focusCarrier = focused ? focus.carrier || "" : "";
  const focusKind = focused ? focus.kind : "";
  // 卡片上的海外格一个目标一格，点开时带着它的地区（服务名），只看那一个目标。
  const focusRegion = focused && focus.region != null ? focus.region : null;
  const focusName = focusRegion != null ? focusRegion || "其他" : focusCarrier && focusCarrier !== "其他" ? focusCarrier : "";
  const focusTitle = focused
    ? `${focusName ? focusName + " · " : ""}${focusKind === "route" ? "三网回程" : "TCP 延迟"}`
    : "";
  // 一个探测任务都没有的机器（按设计不挂任务）：不给空图表，也不写「请在后台创建并绑定任务」，
  // 只留一行说明——与公开卡片同一句话。判据是节点上的探测项（probe_targets），不是这次拿回来
  // 的观测：新绑的任务要等 Agent 认领才有行。
  const probePlan = (n.probe_targets || []).filter(
    (t) => t.kind === "tcp" || t.kind === "route",
  );
  // 回程标签的口径说明只在真有探测时给（没任务或没有公网地址的机器，这段说明无所指）。
  const routeHint =
    probePlan.length && (hasIPv4 || hasIPv6)
      ? `<p class="hint">回程标签按实际经过的骨干网命名（163 / CN2GIA / CN2GT / 4837 / 9929 / 10099 / CMIN2 / CMI / CTG），不代表商家承诺的线路。地址已脱敏，超时或过期的结果显示为未知；TCP 连接失败率不等于 ICMP 丢包率。</p><details class="more"><summary>标签判定规则</summary><p class="hint">ASN 优先、跳地址前缀兜底。CN2 跳之后仍走 59.43.* 到目标省份记为 GIA、连续两跳及以上 202.97.*（163 骨干）才记为 GT（单个 202.97 跳是途中偶发回包的骨干路由器，不算落地），一条电信线路只给一个标签、联通轨迹里出现过 9929 就不再另记 4837、163 只在它是轨迹里第一条线路名时才记（后面再出现的 163 是落地接入，如 10099 → 9929）。</p></details>`
      : "";
  const networkPanel = !probePlan.length
    ? `<p class="hint probe-none">仅主机指标</p>`
    : !hasIPv4 && !hasIPv6
      ? `<p class="hint">${n.last_seen ? "未检测到可用的公网 IP 地址，已跳过网络探测。" : "等待 Agent 接入，接入后开始网络探测。"}</p>`
      : focused
        ? focusKind === "route"
          ? `<div id="route-details"></div>`
          : `<div id="network-overview"></div>`
        : `<div id="network-overview"></div><div id="route-details"></div>`;
  // 达量降速：线路真配了规则才给这一格（与公开卡片同一判断）。按窗口平均带宽的写窗口
  // 均值并如实报出本机已上报多少小时；按周期额度用满后限速的写那条额度与用满后的速率。
  const trafficWord = { sum: "双向", max: "较大方向", in: "下载", out: "上传" }[n.billing.traffic_mode] || "双向";
  const throttleTile =
    n.line.throttle_mbps > 0
      ? n.line.throttle_on === "quota"
        ? `<div class="detail-wide"><span>用满额度后的限速</span><strong>${n.line.throttle_mbps} Mbit/s</strong><small>额度 ${bytes(n.billing.traffic_limit)}${n.line.mbps ? `（${trafficWord}）· 峰值 ${n.line.mbps} Mbit/s` : ""} · 本周期已用 ${bytes(n.traffic_used)}${n.throttled ? " · 已达额度" : ""}</small></div>`
        : `<div class="detail-wide"><span>近 ${Math.min(n.covered_hours || n.window_hours || 24, n.window_hours || 24)} 小时平均带宽</span><strong>${n.covered_hours > 0 ? mbps(n.avg_bps) : n.last_seen ? "统计中" : "未接入"}</strong><small>降速阈值 ${n.line.throttle_mbps} Mbit/s${n.line.mbps ? ` · 线路带宽 ${n.line.mbps} Mbit/s` : ""}${n.throttled ? " · 已达阈值" : ""}${n.covered_hours > 0 && n.covered_hours < (n.window_hours || 24) ? ` · 本机已上报 ${n.covered_hours} 小时` : ""} · 按${trafficWord}统计</small></div>`
      : "";
  // 线路带宽：后台填了就在详情里单独给一格（前台看得到线路规格）。已经按降速规则在
  // throttleTile 里写过这一项（「峰值 X Mbit/s」/「线路带宽 X Mbit/s」）就不重复一遍。
  const bandwidthTile =
    n.line.mbps > 0 && !throttleTile
      ? `<div><span>线路带宽</span><strong>${bandwidth(n.line.mbps)}</strong><small>商家标称的端口 / 峰值速率</small></div>`
      : "";
  // 解锁检测：这台机器跑过检测才有这一块（没跑过就不画，不留占位）。数据取节点自带的
  // unlock 字段（/nodes 已经带上）。
  const unlockSection =
    !focused && n.unlock?.total
      ? `<div class="unlock-block">${unlockPanel(n.unlock, catalog, { tested: true })}</div>`
      : "";
  const content = `<div class="detail-meta"><span class="pill ${n.online ? "good" : ""}">${statusLabel(n)}</span><span>最近上报 ${date(n.last_seen)}</span></div>${focused ? "" : `<div data-part="detail-stats" class="detail-stats"><div><span>本周期用量</span><strong>${bytes(n.traffic_used)}</strong><small>${date(n.cycle_start)} — ${date(n.cycle_end)}（本地时间）</small></div><div><span>本次开机网卡累计</span><strong>↑ ${bytes(n.metrics?.net_out)} / ↓ ${bytes(n.metrics?.net_in)}</strong><small>运行 ${Math.floor((n.metrics?.uptime || 0) / 86400)} 天 · ${esc(osLabel(n, "—"))} ${esc(n.metrics?.arch || "")}</small></div>${throttleTile}${bandwidthTile}</div><div class="section-head"><h3>主机历史</h3>${historyRange()}</div><div id="charts">${hostCharts(history, 24, n)}</div>${unlockSection}`}<div class="section-head"><h3>${focused ? esc(focusTitle) : "网络与三网回程"}</h3><div class="heading-actions">${familyControl}${focused && focusKind !== "route" ? historyRange(168) : ""}</div></div>${networkPanel}${routeHint}`;
  const head = d.querySelector(".modal-head");
  d.replaceChildren(head);
  d.insertAdjacentHTML("beforeend", content);
  wireCharts(d);
  // 卡片把没有运营商的旧任务归入“其他”，这里用同样的规则匹配。
  const carrierOf = (value) => (["电信", "联通", "移动"].includes(value) ? value : "其他");
  const inFocus = (carrier, region) =>
    carrierOf(carrier) === focusCarrier && (focusRegion == null || (region || "") === focusRegion);
  let networkSeries = focused ? network.filter((x) => inFocus(x.carrier, x.region)) : network;
  const scopedObs = focused ? obs.filter((o) => inFocus(o.task.carrier, o.task.region)) : obs;
  const wantedFamily = focus && focus.family;
  let selectedFamily = wantedFamily && (wantedFamily === "ipv4" ? hasIPv4 : hasIPv6) ? wantedFamily : hasIPv4 ? "ipv4" : "ipv6";
  let selectedHours = 24;
  let overviewMetric = "latency";
  function renderNetwork() {
    const overview = d.querySelector("#network-overview");
    if (!overview) return;
    // 每个目标一条线，悬停就能读出任意时刻的延迟或丢包；单目标的「查看趋势」折叠图已删掉
    // ——同一份数据重复一遍，只会让人分不清哪条是延迟、哪条是回程。
    const series = networkSeries.filter((s) => s.family === selectedFamily);
    // 这一族根本没有延迟探测项时，别写「样本不足，采样后自动显示」——那等于许诺一张永远不会来的图。
    if (!series.length && !probePlan.some((t) => t.kind === "tcp" && t.family === selectedFamily)) {
      overview.innerHTML = '<div class="chart-empty">此协议没有延迟探测任务</div>';
      return;
    }
    // 网络历史最多 7 天：下拉选到 30 天时，曲线和标题都按实际能查到的窗口说。
    overview.innerHTML = networkOverview(series, overviewMetric, Math.min(selectedHours, 168));
    d.querySelectorAll("[data-network-metric]").forEach((b) => (b.onclick = () => {
      overviewMetric = b.dataset.networkMetric;
      renderNetwork();
    }));
    wireCharts(d);
  }
  function renderRoutes(f) {
    selectedFamily = f;
    renderNetwork();
    const details = d.querySelector("#route-details");
    if (!details) return;
    // 延迟数据由上面的多折线图呈现，这里只留三网回程路径。
    const matches = scopedObs.filter((o) => o.task.family === f && o.task.kind === "route");
    details.innerHTML = matches.length
      ? `<section class="trace-group"><h4>三网回程<span>${matches.length} 个目标</span></h4>${matches
          .map(
            (o) =>
              `<details class="trace"><summary><span><b>${esc(targetName(o.task))}</b><small>${esc(o.task.host)}:${o.task.port} · ${date(o.at)}</small></span><span class="trace-status">${esc(!o.stale && o.result.status === "ok" ? o.result.route : "未知")}<small>${o.stale ? "结果已过期" : esc(reasonName[o.result.reason] || statusName[o.result.status] || "未知")}</small></span></summary><div class="table-scroll"><table><thead><tr><th>跳数</th><th>地址（脱敏）</th><th>ASN</th><th>延迟</th></tr></thead><tbody>${(o.result.hops || []).map((h) => `<tr><td>${h.ttl}</td><td>${esc(h.address)}</td><td>${esc(h.asn || "未知")}</td><td>${h.rtt ? h.rtt.toFixed(1) + " ms" : "—"}</td></tr>`).join("") || '<tr><td colspan="4">暂无路径数据</td></tr>'}</tbody></table></div></details>`,
          )
          .join("")}</section>`
      : probePlan.some((t) => t.kind === "route" && t.family === f)
        ? '<div class="chart-empty">此协议还没有回程结果</div>'
        : '<div class="chart-empty">此协议没有回程探测任务</div>';
  }
  d.querySelectorAll("[data-detail-family]").forEach(
    (x) => x.classList.toggle("active", x.dataset.detailFamily === selectedFamily),
  );
  renderRoutes(selectedFamily);
  d.querySelectorAll("[data-detail-family]").forEach(
    (b) =>
      (b.onclick = () => {
        d.querySelectorAll("[data-detail-family]").forEach((x) =>
          x.classList.toggle("active", x === b),
        );
        renderRoutes(b.dataset.detailFamily);
      }),
  );
  // 时间范围的数据按请求缓存在这次打开的弹窗里：主机历史按所选小时数，网络历史最多 7 天
  // （30 天与 7 天是同一份）。碰到下拉框就把各档预取下来，选的时候多半已经在手里，
  // 来回切换也不再发请求。缓存一分钟后作废，弹窗开着放久了不会一直画旧数据。
  const rangeCache = new Map();
  const cacheTTL = 60000;
  const fetchOnce = (url) => {
    const hit = rangeCache.get(url);
    if (hit && Date.now() - hit.at < cacheTTL) return hit;
    const entry = { at: Date.now(), settled: false };
    entry.promise = api(url).finally(() => (entry.settled = true));
    // 失败的不留在缓存里，下次选同一档会重新请求；预取的失败也不该变成未处理的拒绝。
    entry.promise.catch(() => rangeCache.get(url) === entry && rangeCache.delete(url));
    rangeCache.set(url, entry);
    return entry;
  };
  const hostURL = (hours) => `/nodes/${id}/history?hours=${hours}&slim=1`;
  const netURL = (hours) => `/nodes/${id}/network-history?hours=${Math.min(hours, 168)}`;
  // 首屏的 24 小时已经取过了。
  for (const [url, value] of [[hostURL(24), history], [netURL(24), network]]) {
    rangeCache.set(url, { at: Date.now(), settled: true, promise: Promise.resolve(value) });
  }
  const rangeSelect = d.querySelector("#history-range");
  const prefetch = () => {
    // 最慢、最常选的 7 天排在最前面，免得浏览器的并发上限让它排在小请求后面。
    const options = [...rangeSelect.options].map((o) => Number(o.value)).sort((a, b) => b - a);
    for (const hours of options) {
      if (focused) fetchOnce(netURL(hours));
      else {
        fetchOnce(netURL(hours));
        fetchOnce(hostURL(hours));
      }
    }
  };
  // 鼠标移上去、键盘聚焦、手指按下：哪个先发生都算「要选了」。原生 select 会被 ui-select.js
  // 包进自定义触发器（异步、且自身隐藏），所以在下拉框所在的标题栏上委托、认触发器外壳。
  // 不挂在 #dialog 上：所有弹窗共用它，每打开一次详情就会多叠一份监听。
  const nearRange = (e) => {
    const shell = e.target.closest?.(".select-shell");
    if (e.target === rangeSelect || shell?.contains(rangeSelect)) prefetch();
  };
  const rangeHead = rangeSelect?.closest(".section-head");
  for (const type of ["pointerover", "focusin", "pointerdown"]) rangeHead?.addEventListener(type, nearRange);
  // 正在等的那几块图调暗并标成忙；只有最后一次选择能撤掉，先发的慢请求回来不会提前撤。
  // 调暗有短暂延迟（见 styles.css），预取命中时不闪一下。
  let rangeTicket = 0;
  const busyTargets = () => [d.querySelector("#charts"), d.querySelector("#network-overview")].filter(Boolean);
  rangeSelect?.addEventListener("change", guard(async (e) => {
    const hours = Number(e.target.value);
    const ticket = ++rangeTicket;
    const host = focused ? null : fetchOnce(hostURL(hours));
    const net = fetchOnce(netURL(hours));
    const waiting = !net.settled || (host && !host.settled);
    if (waiting) {
      for (const el of busyTargets()) {
        el.classList.add("range-loading");
        el.setAttribute("aria-busy", "true");
      }
    }
    try {
      const [samples, series] = await withDeadline(() => Promise.all([host ? host.promise : null, net.promise]));
      if (!d.open || d.dataset.nodeId !== id || ticket !== rangeTicket) return;
      selectedHours = hours;
      networkSeries = focused ? series.filter((x) => inFocus(x.carrier, x.region)) : series;
      // 聚焦视图没有主机历史，只有网络曲线——那里没有 #charts，别把整段交互打断。
      const hosts = d.querySelector("#charts");
      if (hosts && samples) hosts.innerHTML = hostCharts(samples, hours, n);
      renderNetwork();
    } finally {
      if (ticket === rangeTicket) {
        for (const el of busyTargets()) {
          el.classList.remove("range-loading");
          el.removeAttribute("aria-busy");
        }
      }
    }
  }));
}

function targetLabel(series) {
  return targetName(series);
}

// 时间范围下拉：主机历史支持 30 天，网络历史（/network-history）最多只有 7 天，
// 聚焦视图因此只给到 7 天——选了 30 天却只画 7 天是骗人的。
// 7 天也是探测结果（回程、延迟色带）的保留上限，所以这就是「全部」。
function spanLabel(hours) {
  return hours >= 24 && hours % 24 === 0 ? hours / 24 + " 天" : hours + " 小时";
}

function historyRange(maxHours = 720) {
  return `<select id="history-range" aria-label="历史时间范围">${[[1, "最近 1 小时"], [6, "最近 6 小时"], [24, "最近 24 小时"], [72, "最近 3 天"], [168, "最近 7 天"], [720, "最近 30 天"]]
    .filter(([hours]) => hours <= maxHours)
    .map(([hours, text]) => `<option value="${hours}"${hours === 24 ? " selected" : ""}>${text}</option>`)
    .join("")}</select>`;
}

function hostCharts(samples, hours, n) {
  // 一笔样本都凑不够（新接入、从未上报，或这段时间一直离线）时四张图都会是同一句「样本不足」，
  // 合成一条提示，不再摆四个空框。提示按节点状态说：在线的节点正在上报，只是这个时间范围的
  // 聚合格（24 小时一格 6 分钟）还没攒够两格，不能再说「Agent 上报后显示」。
  if (samples.length < 2) {
    const text = n?.online
      ? "正在采样，攒够两个数据点后显示曲线；可先切到「最近 1 小时」查看"
      : n?.last_seen
        ? "这段时间没有主机历史（节点离线）"
        : "节点还没接入，Agent 上报后自动显示 CPU、内存与网速曲线";
    return `<div class="chart-empty">${text}</div>`;
  }
  // 速率两张按全站统一的「上传在前」排列。
  return chart(samples, "cpu", "CPU 使用率", "teal", hours) +
    chart(samples, "memory", "内存使用率", "orange", hours) +
    chart(samples, "rate_out", "上传速率", "orange", hours) +
    chart(samples, "rate_in", "下载速率", "teal", hours);
}

function networkOverview(series, metric, hours) {
  const usable = series.map((s) => ({ ...s, samples: s.samples.filter((p) => p[metric] != null) })).filter((s) => s.samples.length > 1);
  if (!usable.length) return '<div class="chart-empty">网络历史样本不足，采样后自动显示折线图</div>';
  const all = usable.flatMap((s) => s.samples), first = Math.min(...all.map((p) => p.at)), last = Math.max(...all.map((p) => p.at));
  const maxValue = metric === "failure" ? 100 : niceCeiling(Math.max(100, ...all.map((p) => p.latency || 0)));
  // 纵坐标：顶部是量程（丢包固定 100%，延迟取历史峰值，最低 100 ms），底部是 0。
  const tick = (v) => metric === "failure" ? v.toFixed(0) + "%" : (v >= 100 ? v.toFixed(0) : v.toFixed(1)) + " ms";
  const yAxis = `<div class="chart-y" aria-hidden="true"><span>${esc(tick(maxValue))}</span><span>${esc(tick(maxValue / 2))}</span><span>${esc(tick(0))}</span></div>`;
  // 多折线必须靠 class 上色：标记里的 stroke="…" 属性优先级低于样式表里的
  // `.chart polyline`，会被主题色统一覆盖成同一条蓝线。legend 用同一组 class 保持一致。
  const unit = metric === "failure" ? "%" : "ms";
  // 图例和读数框只写「地区 · 运营商」：IPv6 目标地址太长，一行放不下会把读数框撑到
  // 图表外面；地址放在 title 里，下面「三网回程」表本来也会列出完整目标。
  // 同一个目标出现在两个任务里（各自一条线）时名字会重复，图例分不清：重名的后面补上任务名。
  const nameCount = new Map();
  for (const s of usable) nameCount.set(targetName(s), (nameCount.get(targetName(s)) || 0) + 1);
  const seriesName = (s) => {
    const base = targetName(s);
    return nameCount.get(base) > 1 && s.name ? `${base}（${s.name}）` : base;
  };
  const seriesFull = (s) => [s.region, s.carrier, s.host].filter(Boolean).join(" · ");
  const hover = usable.map((s, i) => ({
    label: seriesName(s),
    full: seriesFull(s),
    unit,
    color: colorClass(i),
    points: s.samples.map((p) => [p.at, p[metric] == null ? null : p[metric]]),
  }));
  const lines = usable.map((s, i) => {
    const points = s.samples.map((p) => `${(((p.at - first) / Math.max(1, last - first)) * 680).toFixed(1)},${(110 - ((p[metric] || 0) / maxValue) * 102).toFixed(1)}`).join(" ");
    return `<polyline class="overview-line ${colorClass(i)}" points="${points}" />`;
  }).join("");
  // 图例右侧常驻最新一次采样值，悬停时换成那一列的值——读数因此永远在图外，不会盖住曲线。
  const latestOf = (s) => {
    const values = s.samples.map((p) => p[metric]).filter((value) => value != null && Number.isFinite(value));
    return values.length ? values[values.length - 1] : null;
  };
  // 读数放在图下方常驻的框里（同时兼作图例）：浮层在图上无论怎么摆都会压住曲线，手机上四条线
  // 铺满绘图区时尤其明显，所以干脆留在图外。框顶一行是时间（平时最近一次采样，悬停换成那一刻），
  // 下面是每个目标的值。
  const latestAt = usable.reduce((at, s) => {
    const last = s.samples && s.samples.length ? s.samples[s.samples.length - 1] : null;
    return last && last.at > at ? last.at : at;
  }, 0);
  const readout = `<div class="chart-readout"><b data-readout-time>${esc(stamp(latestAt).full)}</b>${usable
    .map(
      (s, i) =>
        `<span title="${esc(seriesFull(s))}"><i class="${colorClass(i)}"></i>${esc(seriesName(s))}<em data-legend-value="${i}">${esc(formatValue(latestOf(s), unit))}</em></span>`,
    )
    .join("")}</div>`;
  return `<figure class="network-overview chart" data-series="${esc(JSON.stringify(hover))}" data-range="${first},${last}" data-ceiling="${maxValue}"><div class="overview-head"><figcaption><strong>${metric === "failure" ? "丢包率" : "延迟"}趋势</strong><span>最近 ${spanLabel(hours)} · ${usable.length} 个目标</span></figcaption><div class="segmented"><button class="${metric === "latency" ? "active" : ""}" data-network-metric="latency">延迟</button><button class="${metric === "failure" ? "active" : ""}" data-network-metric="failure">丢包</button></div></div><div class="chart-plot">${yAxis}<div class="chart-body"><svg viewBox="0 0 680 120" preserveAspectRatio="none" role="img" aria-label="网络${metric === "failure" ? "丢包率" : "延迟"}历史曲线，纵轴 0 到 ${esc(tick(maxValue))}"><path class="gridline" d="M0 8H680 M0 59H680 M0 110H680" />${lines}</svg>${axisRow(first, last)}</div></div>${readout}</figure>`;
}
