import { api, esc, modal, guard } from "./api.js";
import { bytes, date, statusName, reasonName } from "./format.js";
import { chart } from "./charts.js";
export async function detail(id) {
  const list = await api("/nodes");
  const n = list.find((x) => x.id === id);
  if (!n) return;
  const d = modal(n.name, `<div class="loading">读取节点详情…</div>`);
  d.dataset.nodeId = id;
  const [history, obs, network] = await Promise.all([
    api(`/nodes/${id}/history?hours=24`),
    api(`/nodes/${id}/observations`),
    api(`/nodes/${id}/network-history?hours=24`),
  ]);
  if (!d.open || d.dataset.nodeId !== id) return;
  const reported = Array.isArray(n.ip_families) ? new Set(n.ip_families) : null;
  const hasIPv4 = reported
    ? reported.has("ipv4")
    : obs.some((o) => o.task.family === "ipv4" && o.task.enabled);
  const hasIPv6 = reported
    ? reported.has("ipv6")
    : obs.some((o) => o.task.family === "ipv6" && o.task.enabled);
  const familyControl =
    hasIPv4 && hasIPv6
      ? `<div class="segmented" role="group" aria-label="IP 协议"><button data-detail-family="ipv4" class="active">IPv4</button><button data-detail-family="ipv6">IPv6</button></div>`
      : `<span class="family-tag">${hasIPv4 ? "IPv4" : hasIPv6 ? "IPv6" : "无探测"}</span>`;
  const networkPanel =
    hasIPv4 || hasIPv6
      ? `<div id="network-overview"></div><div id="network-history"></div><div id="route-details"></div>`
      : `<p class="hint">未检测到可用的公网 IP 地址，已跳过网络探测。</p>`;
  const content = `<div class="detail-meta"><span class="pill ${n.online ? "good" : ""}">${n.online ? "在线" : "离线"}</span><span>最近上报 ${date(n.last_seen)}</span></div><div class="detail-stats"><div><span>本周期流量</span><strong>${bytes(n.traffic_used)}</strong><small>${date(n.cycle_start)} — ${date(n.cycle_end)} UTC 边界</small></div><div><span>本次开机网卡累计</span><strong>↓ ${bytes(n.metrics?.net_in)} / ↑ ${bytes(n.metrics?.net_out)}</strong><small>运行 ${Math.floor((n.metrics?.uptime || 0) / 86400)} 天 · ${esc(n.metrics?.os || "—")} ${esc(n.metrics?.arch || "")}</small></div></div><div class="section-head"><h3>主机历史</h3><select id="history-range" aria-label="历史时间范围"><option value="1">最近 1 小时</option><option value="24" selected>最近 24 小时</option><option value="168">最近 7 天</option><option value="720">最近 30 天</option></select></div><div id="charts">${hostCharts(history, 24)}</div><div class="section-head"><h3>网络与三网回程</h3>${familyControl}</div>${networkPanel}<p class="hint">回程标签按实际观测到的骨干命名（163 / CN2GIA / CN2GT / 4837 / 9929 / 10099 / CMIN2 / CMI / CTG）；跳地址前缀优先于 ASN，CN2 直接进入 59.43.* 记为 GIA、先经过 163 再进入记为 GT，仍不代表商业线路承诺。地址已脱敏；超时与过期结果显示未知。TCP 连接失败率不等同于 ICMP 丢包率。</p>`;
  const head = d.querySelector(".modal-head");
  d.replaceChildren(head);
  d.insertAdjacentHTML("beforeend", content);
  let networkSeries = network;
  let selectedFamily = hasIPv4 ? "ipv4" : "ipv6";
  let selectedHours = 24;
  let overviewMetric = "latency";
  function renderNetwork() {
    const overview = d.querySelector("#network-overview");
    const historyEl = d.querySelector("#network-history");
    if (!overview || !historyEl) return;
    const series = networkSeries.filter((s) => s.family === selectedFamily);
    overview.innerHTML = networkOverview(series, overviewMetric, selectedHours);
    d.querySelectorAll("[data-network-metric]").forEach((b) => (b.onclick = () => {
      overviewMetric = b.dataset.networkMetric;
      renderNetwork();
    }));
    historyEl.innerHTML = series.map((s, i) => {
      const samples = s.samples.map((p) => ({at: p.at, metrics: {latency: p.latency, failure: p.failure}}));
      const title = targetLabel(s);
      return `<details class="trace network-trace"><summary><span><b>${esc(title)}</b><small>${esc(s.host)}:${s.port} · ${s.samples.length} 个采样</small></span><span class="trace-chevron">查看趋势</span></summary><div class="trace-charts">${chart(samples, "latency", "TCP 延迟", "teal", Math.min(selectedHours, 168)).replaceAll("chart-grad-latency-teal", "network-latency-" + i)}${chart(samples, "failure", "TCP 连接失败率", "orange", Math.min(selectedHours, 168)).replaceAll("chart-grad-failure-orange", "network-failure-" + i)}</div></details>`;
    }).join("") || '<p class="hint">此协议暂无 TCP 历史，网络历史最多保留 7 天。</p>';
  }
  function renderRoutes(f) {
    selectedFamily = f;
    renderNetwork();
    const details = d.querySelector("#route-details");
    if (!details) return;
    const matches = obs.filter((o) => o.task.family === f);
    details.innerHTML = matches.length
      ? matches
          .map(
            (o) =>
              `<details class="trace"><summary><span><b>${esc(o.task.region || o.task.name)} · ${esc(o.task.carrier || "自定义")}</b><small>${esc(o.task.host)}:${o.task.port} · ${date(o.at)}</small></span><span class="trace-status">${o.task.kind === "route" ? esc(!o.stale && o.result.status === "ok" ? o.result.route : "未知") : o.result.latency != null ? o.result.latency.toFixed(1) + " ms" : "—"}<small>${o.stale ? "结果已过期" : esc(reasonName[o.result.reason] || statusName[o.result.status] || "未知")}</small></span></summary>${o.task.kind === "route" ? `<div class="table-scroll"><table><thead><tr><th>跳数</th><th>地址（脱敏）</th><th>ASN</th><th>延迟</th></tr></thead><tbody>${(o.result.hops || []).map((h) => `<tr><td>${h.ttl}</td><td>${esc(h.address)}</td><td>${esc(h.asn || "未知")}</td><td>${h.rtt ? h.rtt.toFixed(1) + " ms" : "—"}</td></tr>`).join("") || '<tr><td colspan="4">暂无路径数据</td></tr>'}</tbody></table></div>` : `<p class="hint">TCP 成功 ${o.result.received} / ${o.result.sent} 次</p>`}</details>`,
          )
          .join("")
      : '<div class="chart-empty">此协议暂无探测结果。请在后台创建并绑定任务。</div>';
  }
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
  d.querySelector("#history-range").onchange = guard(async (e) => {
    const hours = Number(e.target.value);
    const [samples, net] = await Promise.all([
      api(`/nodes/${id}/history?hours=${hours}`),
      api(`/nodes/${id}/network-history?hours=${Math.min(hours, 168)}`)
    ]);
    if (
      !d.open ||
      d.dataset.nodeId !== id ||
      Number(d.querySelector("#history-range")?.value) !== hours
    )
      return;
    selectedHours = hours;
    networkSeries = net;
    d.querySelector("#charts").innerHTML = hostCharts(samples, hours);
    renderNetwork();
  });
}

function targetLabel(series) {
  return [series.region, series.carrier || "自定义"].filter(Boolean).join(" · ") || series.name || "网络目标";
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
  const colors = ["#ff625f", "#36c8bf", "#9b7cff", "#5a9cff", "#ffad3d", "#eb63b5", "#36c98b", "#ff873d"];
  const lines = usable.map((s, i) => {
    const points = s.samples.map((p) => `${(((p.at - first) / Math.max(1, last - first)) * 680).toFixed(1)},${(110 - ((p[metric] || 0) / maxValue) * 102).toFixed(1)}`).join(" ");
    return `<polyline class="overview-line" stroke="${colors[i % colors.length]}" points="${points}" />`;
  }).join("");
  const legend = usable.map((s, i) => `<span><i class="legend-dot lc${i % colors.length}"></i>${esc([s.region, s.carrier, s.host].filter(Boolean).join(" · "))}</span>`).join("");
  return `<figure class="network-overview chart"><div class="overview-head"><figcaption><strong>${metric === "failure" ? "丢包率" : "延迟"}趋势</strong><span>最近 ${hours} 小时 · ${usable.length} 个目标</span></figcaption><div class="segmented"><button class="${metric === "latency" ? "active" : ""}" data-network-metric="latency">延迟</button><button class="${metric === "failure" ? "active" : ""}" data-network-metric="failure">丢包</button></div></div><svg viewBox="0 0 680 140" role="img" aria-label="网络${metric === "failure" ? "丢包率" : "延迟"}历史曲线"><path class="gridline" d="M0 8H680 M0 59H680 M0 110H680" />${lines}</svg><div class="chart-axis"><span>${new Date(first * 1000).toLocaleString("zh-CN", { hour12: false })}</span><span>${new Date(last * 1000).toLocaleString("zh-CN", { hour12: false })}</span></div><div class="overview-legend">${legend}</div></figure>`;
}
