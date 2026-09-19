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
  const content = `<div class="detail-meta"><span class="pill ${n.online ? "good" : ""}">${n.online ? "在线" : "离线"}</span><span>最近上报 ${date(n.last_seen)}</span></div><div class="detail-stats"><div><span>本周期流量</span><strong>${bytes(n.traffic_used)}</strong><small>${date(n.cycle_start)} — ${date(n.cycle_end)} UTC 边界</small></div><div><span>本次开机网卡累计</span><strong>↓ ${bytes(n.metrics?.net_in)} / ↑ ${bytes(n.metrics?.net_out)}</strong><small>运行 ${Math.floor((n.metrics?.uptime || 0) / 86400)} 天 · ${esc(n.metrics?.os || "—")} ${esc(n.metrics?.arch || "")}</small></div></div><div class="section-head"><h3>主机历史</h3><select id="history-range" aria-label="历史时间范围"><option value="1">最近 1 小时</option><option value="24" selected>最近 24 小时</option><option value="168">最近 7 天</option><option value="720">最近 30 天</option></select></div><div id="charts">${hostCharts(history, 24)}</div><div class="section-head"><h3>网络与三网回程</h3><div class="segmented"><button data-detail-family="ipv4" class="active">IPv4</button><button data-detail-family="ipv6">IPv6</button></div></div><div id="network-history"></div><div id="route-details"></div><p class="hint">回程标签来自已观测 ASN，不代表商业线路承诺。地址已脱敏；超时与过期结果显示未知。TCP 连接失败率不等同于 ICMP 丢包率。</p>`;
  const head = d.querySelector(".modal-head");
  d.replaceChildren(head);
  d.insertAdjacentHTML("beforeend", content);
  let networkSeries = network;
  let selectedFamily = "ipv4";
  let selectedHours = 24;
  function renderNetwork() {
    const series = networkSeries.filter((s) => s.family === selectedFamily);
    d.querySelector("#network-history").innerHTML = series.map((s, i) => {
      const samples = s.samples.map((p) => ({at: p.at, metrics: {latency: p.latency, failure: p.failure}}));
      const title = `${s.name} · ${s.region || ""} ${s.carrier || ""} · ${s.host}:${s.port}`;
      return `<details class="trace"><summary>${esc(title)} · 历史趋势</summary>${chart(samples, "latency", "TCP 延迟", "teal", Math.min(selectedHours, 168)).replaceAll("chart-grad-latency-teal", "network-latency-" + i)}${chart(samples, "failure", "TCP 连接失败率", "orange", Math.min(selectedHours, 168)).replaceAll("chart-grad-failure-orange", "network-failure-" + i)}</details>`;
    }).join("") || '<p class="hint">此协议暂无 TCP 历史，网络历史最多保留 7 天。</p>';
  }
  function renderRoutes(f) {
    selectedFamily = f;
    renderNetwork();
    const matches = obs.filter((o) => o.task.family === f);
    d.querySelector("#route-details").innerHTML = matches.length
      ? matches
          .map(
            (o) =>
              `<details class="trace"><summary><span>${esc(o.task.region || o.task.name)} · ${esc(o.task.carrier || "自定义")} · ${o.task.kind === "route" ? "回程" : "TCP 延迟"}<small>${esc(o.task.host)}:${o.task.port} · ${date(o.at)}</small></span><span class="trace-status">${o.task.kind === "route" ? esc(!o.stale && o.result.status === "ok" ? o.result.route : "未知") : o.result.latency != null ? o.result.latency.toFixed(1) + " ms" : "—"}<small>${o.stale ? "结果已过期" : esc(reasonName[o.result.reason] || statusName[o.result.status] || "未知")}</small></span></summary>${o.task.kind === "route" ? `<div class="table-scroll"><table><thead><tr><th>跳数</th><th>地址（脱敏）</th><th>ASN</th><th>延迟</th></tr></thead><tbody>${(o.result.hops || []).map((h) => `<tr><td>${h.ttl}</td><td>${esc(h.address)}</td><td>${esc(h.asn || "未知")}</td><td>${h.rtt ? h.rtt.toFixed(1) + " ms" : "—"}</td></tr>`).join("") || '<tr><td colspan="4">暂无路径数据</td></tr>'}</tbody></table></div>` : `<p class="hint">TCP 成功 ${o.result.received} / ${o.result.sent} 次</p>`}</details>`,
          )
          .join("")
      : '<div class="chart-empty">此协议暂无探测结果。请在后台创建并绑定任务。</div>';
  }
  renderRoutes("ipv4");
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

function hostCharts(samples, hours) {
  return chart(samples, "cpu", "CPU 使用率", "teal", hours) +
    chart(samples, "memory", "内存使用率", "orange", hours) +
    chart(samples, "rate_in", "下载速率", "teal", hours) +
    chart(samples, "rate_out", "上传速率", "orange", hours);
}
