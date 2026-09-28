import { api, esc, modal, guard } from "./api.js";
import { bytes, date, mbps, statusName, reasonName } from "./format.js";
import { chart, wireCharts, colorClass, axisRow, formatValue, stamp } from "./charts.js";
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
// focus 为空时展示整台机器的综合信息；带 {kind, carrier} 时只展示那一格的详情：
// 延迟格看该运营商的 TCP 延迟，回程格看该运营商的回程路径。
export async function detail(id, focus = null) {
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
  let history, obs, network;
  try {
    [history, obs, network] = await withDeadline(() =>
      Promise.all([
        api(`/nodes/${id}/history?hours=24`),
        api(`/nodes/${id}/observations`),
        api(`/nodes/${id}/network-history?hours=24`),
      ]),
    );
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
    : obs.some((o) => o.task.family === "ipv4" && o.task.enabled);
  const hasIPv6 = reported
    ? reported.has("ipv6")
    : obs.some((o) => o.task.family === "ipv6" && o.task.enabled);
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
  const focusTitle = focused
    ? `${focusCarrier && focusCarrier !== "其他" ? focusCarrier + " · " : ""}${focusKind === "route" ? "三网回程" : "TCP 延迟"}`
    : "";
  // 一个探测任务都没有的机器（按设计不挂任务）：不给空图表，也不写「请在后台创建并绑定任务」，
  // 只留一行说明——与公开卡片同一句话。判据是节点上的探测项（probe_targets），不是这次拿回来
  // 的观测：新绑的任务要等 Agent 认领才有行。
  const probePlan = (n.probe_targets || []).filter(
    (t) => t.kind === "tcp" || t.kind === "route",
  );
  const networkPanel = !probePlan.length
    ? `<p class="hint probe-none">仅主机指标</p>`
    : !hasIPv4 && !hasIPv6
      ? `<p class="hint">未检测到可用的公网 IP 地址，已跳过网络探测。</p>`
      : focused
        ? focusKind === "route"
          ? `<div id="route-details"></div>`
          : `<div id="network-overview"></div>`
        : `<div id="network-overview"></div><div id="route-details"></div>`;
  // 达量降速：线路真配了规则才给这一格（与公开卡片同一判断）。按窗口平均带宽的写窗口
  // 均值并如实报出本机已上报多少小时；按周期额度用满后限速的写那条额度与用满后的速率。
  const trafficWord = { sum: "双向", max: "较大方向", in: "下载", out: "上传" }[n.traffic_mode] || "双向";
  const throttleTile =
    n.throttle_mbps > 0
      ? n.throttle_on === "quota"
        ? `<div class="detail-wide"><span>用满额度后的限速</span><strong>${n.throttle_mbps} Mbit/s</strong><small>额度 ${bytes(n.traffic_limit)}${n.line_mbps ? `（${trafficWord}）· 峰值 ${n.line_mbps} Mbit/s` : ""} · 本周期已用 ${bytes(n.traffic_used)}${n.throttled ? " · 已达额度" : ""}</small></div>`
        : `<div class="detail-wide"><span>近 ${Math.min(n.covered_hours || n.window_hours || 24, n.window_hours || 24)} 小时平均带宽</span><strong>${n.covered_hours > 0 ? mbps(n.avg_bps) : "统计中"}</strong><small>降速阈值 ${n.throttle_mbps} Mbit/s${n.line_mbps ? ` · 线路带宽 ${n.line_mbps} Mbit/s` : ""}${n.throttled ? " · 已达阈值" : ""}${n.covered_hours > 0 && n.covered_hours < (n.window_hours || 24) ? ` · 本机已上报 ${n.covered_hours} 小时` : ""} · 按${trafficWord}统计</small></div>`
      : "";
  const content = `<div class="detail-meta"><span class="pill ${n.online ? "good" : ""}">${n.online ? "在线" : "离线"}</span><span>最近上报 ${date(n.last_seen)}</span></div>${focused ? "" : `<div class="detail-stats"><div><span>本周期流量</span><strong>${bytes(n.traffic_used)}</strong><small>${date(n.cycle_start)} — ${date(n.cycle_end)} UTC 边界</small></div><div><span>本次开机网卡累计</span><strong>↓ ${bytes(n.metrics?.net_in)} / ↑ ${bytes(n.metrics?.net_out)}</strong><small>运行 ${Math.floor((n.metrics?.uptime || 0) / 86400)} 天 · ${esc(n.metrics?.os || "—")} ${esc(n.metrics?.arch || "")}</small></div>${throttleTile}</div><div class="section-head"><h3>主机历史</h3>${historyRange()}</div><div id="charts">${hostCharts(history, 24)}</div>`}<div class="section-head"><h3>${focused ? esc(focusTitle) : "网络与三网回程"}</h3><div class="heading-actions">${familyControl}${focused && focusKind !== "route" ? historyRange(168) : ""}</div></div>${networkPanel}<p class="hint">回程标签按实际观测到的骨干命名（163 / CN2GIA / CN2GT / 4837 / 9929 / 10099 / CMIN2 / CMI / CTG）；ASN 优先、跳地址前缀兜底，CN2 跳之后仍走 59.43.* 到目标省份记为 GIA、连续两跳及以上 202.97.*（163 骨干）才记为 GT（单个 202.97 跳是途中偶发回包的骨干路由器，不算落地），一条电信线路只给一个标签、联通轨迹里出现过 9929 就不再另记 4837、163 只在它是轨迹里第一条线路名时才记（后面再出现的 163 是落地接入，如 10099 → 9929），仍不代表商业线路承诺。地址已脱敏；超时与过期结果显示未知。TCP 连接失败率不等同于 ICMP 丢包率。</p>`;
  const head = d.querySelector(".modal-head");
  d.replaceChildren(head);
  d.insertAdjacentHTML("beforeend", content);
  wireCharts(d);
  // 卡片把没有运营商的旧任务归入“其他”，这里用同样的规则匹配。
  const carrierOf = (value) => (["电信", "联通", "移动"].includes(value) ? value : "其他");
  let networkSeries = focused ? network.filter((x) => carrierOf(x.carrier) === focusCarrier) : network;
  const scopedObs = focused ? obs.filter((o) => carrierOf(o.task.carrier) === focusCarrier) : obs;
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
              `<details class="trace"><summary><span><b>${esc(o.task.region || o.task.name)} · ${esc(o.task.carrier || "自定义")}</b><small>${esc(o.task.host)}:${o.task.port} · ${date(o.at)}</small></span><span class="trace-status">${esc(!o.stale && o.result.status === "ok" ? o.result.route : "未知")}<small>${o.stale ? "结果已过期" : esc(reasonName[o.result.reason] || statusName[o.result.status] || "未知")}</small></span></summary><div class="table-scroll"><table><thead><tr><th>跳数</th><th>地址（脱敏）</th><th>ASN</th><th>延迟</th></tr></thead><tbody>${(o.result.hops || []).map((h) => `<tr><td>${h.ttl}</td><td>${esc(h.address)}</td><td>${esc(h.asn || "未知")}</td><td>${h.rtt ? h.rtt.toFixed(1) + " ms" : "—"}</td></tr>`).join("") || '<tr><td colspan="4">暂无路径数据</td></tr>'}</tbody></table></div></details>`,
          )
          .join("")}</section>`
      : '<div class="chart-empty">此协议暂无回程结果。请在后台创建并绑定任务。</div>';
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
  d.querySelector("#history-range")?.addEventListener("change", guard(async (e) => {
    const hours = Number(e.target.value);
    const [samples, net] = await withDeadline(() =>
      Promise.all([
        api(`/nodes/${id}/history?hours=${hours}`),
        api(`/nodes/${id}/network-history?hours=${Math.min(hours, 168)}`),
      ]),
    );
    if (
      !d.open ||
      d.dataset.nodeId !== id ||
      Number(d.querySelector("#history-range")?.value) !== hours
    )
      return;
    selectedHours = hours;
    networkSeries = focused ? net.filter((x) => carrierOf(x.carrier) === focusCarrier) : net;
    // 聚焦视图没有主机历史，只有网络曲线——那里没有 #charts，别把整段交互打断。
    const hosts = d.querySelector("#charts");
    if (hosts) hosts.innerHTML = hostCharts(samples, hours);
    renderNetwork();
  }));
}

function targetLabel(series) {
  return [series.region, series.carrier || "自定义"].filter(Boolean).join(" · ") || series.name || "网络目标";
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

function hostCharts(samples, hours) {
  return chart(samples, "cpu", "CPU 使用率", "teal", hours) +
    chart(samples, "memory", "内存使用率", "orange", hours) +
    chart(samples, "rate_in", "下载速率", "teal", hours) +
    chart(samples, "rate_out", "上传速率", "orange", hours);
}

function networkOverview(series, metric, hours) {
  const usable = series.map((s) => ({ ...s, samples: s.samples.filter((p) => p[metric] != null) })).filter((s) => s.samples.length > 1);
  if (!usable.length) return '<div class="chart-empty">网络历史样本不足，采样后自动显示折线图</div>';
  const all = usable.flatMap((s) => s.samples), first = Math.min(...all.map((p) => p.at)), last = Math.max(...all.map((p) => p.at));
  const maxValue = metric === "failure" ? 100 : Math.max(100, ...all.map((p) => p.latency || 0));
  // 纵坐标：顶部是量程（丢包固定 100%，延迟取历史峰值，最低 100 ms），底部是 0。
  const tick = (v) => metric === "failure" ? v.toFixed(0) + "%" : (v >= 100 ? v.toFixed(0) : v.toFixed(1)) + " ms";
  const yAxis = `<div class="chart-y" aria-hidden="true"><span>${esc(tick(maxValue))}</span><span>${esc(tick(maxValue / 2))}</span><span>${esc(tick(0))}</span></div>`;
  // 多折线必须靠 class 上色：标记里的 stroke="…" 属性优先级低于样式表里的
  // `.chart polyline`，会被主题色统一覆盖成同一条蓝线。legend 用同一组 class 保持一致。
  const unit = metric === "failure" ? "%" : "ms";
  // 图例和读数框只写「地区 · 运营商」：IPv6 目标地址太长，一行放不下会把读数框撑到
  // 图表外面；地址放在 title 里，下面「三网回程」表本来也会列出完整目标。
  const seriesName = (s) => [s.region, s.carrier].filter(Boolean).join(" · ") || s.name || "网络目标";
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
