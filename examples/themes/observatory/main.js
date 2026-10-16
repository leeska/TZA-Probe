import { escape as e, bytes } from "./format.mjs";
const sdk = window.TZA;
const labels = await fetch(new URL("./labels.json", import.meta.url)).then((response) => response.json());
document.querySelector("aside p").textContent = labels.subtitle;
let nodes = [], query = "", order = "name", generation = 0;
const scene = document.querySelector("#scene");
const error = document.querySelector("#error");
const wrap = (fn) => async (...args) => { try { error.textContent = ""; await fn(...args); } catch (err) { error.textContent = err.message; } };
const pct = (n, key) => key === "cpu" ? n.metrics?.cpu || 0 : n.metrics?.memory_total ? n.metrics.memory_used / n.metrics.memory_total * 100 : 0;

function overview() {
  const online = nodes.filter((n) => n.online).length;
  const rows = nodes.filter((n) => `${n.name} ${n.region || ""} ${n.group || ""}`.toLowerCase().includes(query.toLowerCase())).sort((a, b) => order === "cpu" ? pct(b, "cpu") - pct(a, "cpu") : a.name.localeCompare(b.name));
  scene.innerHTML = `<div class="numbers"><section><small>在线 / 总数</small><strong>${online}<em> / ${nodes.length}</em></strong></section><section><small>实时吞吐</small><strong>${bytes(nodes.reduce((s, n) => s + (n.metrics?.rate_in || 0) + (n.metrics?.rate_out || 0), 0))}<em> /s</em></strong></section><section><small>地区</small><strong>${new Set(nodes.map((n) => n.region).filter(Boolean)).size}</strong></section></div><div class="tools"><input id="query" aria-label="搜索主机" placeholder="搜索主机、地区或分组" value="${e(query)}"><button id="sort">${order === "name" ? "按 CPU 排序" : "按名称排序"}</button></div><div class="table"><table><thead><tr><th>主机</th><th>状态</th><th>CPU</th><th>内存</th><th>网络 ↓ / ↑</th><th></th></tr></thead><tbody>${rows.map((n) => `<tr><td><b>${e(n.name)}</b><small>${e(n.region || "—")} / ${e(n.group || "默认分组")}</small></td><td><span class="status ${n.online ? "up" : ""}">${n.online ? "在线" : "离线"}</span></td><td>${pct(n, "cpu").toFixed(1)}%</td><td>${pct(n, "memory").toFixed(1)}%</td><td>${bytes(n.metrics?.rate_in)} / ${bytes(n.metrics?.rate_out)}</td><td><button data-node="${e(n.id)}">查看 →</button></td></tr>`).join("") || '<tr><td colspan="6">没有匹配的主机</td></tr>'}</tbody></table></div>`;
  document.querySelector("#query").oninput = (event) => { query = event.target.value; overview(); const input = document.querySelector("#query"); input.focus(); input.setSelectionRange(query.length, query.length); };
  document.querySelector("#sort").onclick = () => { order = order === "name" ? "cpu" : "name"; sdk.storage.set("sort", order).catch((err) => { error.textContent = err.message; }); overview(); };
}
async function render() {
  const ticket = ++generation;
  const path = new URL(sdk.context.route, "https://probe.invalid").pathname;
  const id = path.startsWith("/node/") ? path.slice(6) : null;
  if (!id) { overview(); return; }
  const node = nodes.find((n) => n.id === id);
  if (!node) { scene.innerHTML = '<h3>主机不存在或未公开</h3>'; return; }
  scene.innerHTML = `<h3>${e(node.name)}</h3><p>正在加载独立详情视图…</p>`;
  const [history, observations, network, chart] = await Promise.all([sdk.api(`/nodes/${id}/history?hours=24&slim=1`), sdk.api(`/nodes/${id}/observations`), sdk.api(`/nodes/${id}/network-history?hours=24`), import("./chart.mjs")]);
  if (ticket !== generation) return;
  scene.innerHTML = `<button id="back">← 返回总览</button><section class="detail"><h3>${e(node.name)}</h3><p>${e(node.region || "—")} · ${e(node.group || "默认分组")}</p><h4>CPU / 最近 24 小时</h4>${chart.draw(history)}<h4>网络探测</h4><p>${observations.length} 个最新结果 · ${Array.isArray(network) ? network.length : Object.keys(network || {}).length} 组历史</p><div class="results">${observations.map((o) => `<p>${e(o.task?.name || o.task?.host || o.task_id || "目标")} <b>${e(o.result?.route || o.result?.status || "—")}</b></p>`).join("") || '<p class="muted">暂无探测结果</p>'}</div></section>`;
  document.querySelector("#back").onclick = () => sdk.navigate("/");
}
async function refresh() {
  nodes = await sdk.api("/nodes");
  document.querySelector("#connection").textContent = `已同步 · ${new Date().toLocaleTimeString("zh-CN")}`;
  // Preserve a custom detail view while polling the independent data model.
  if (!new URL(sdk.context.route, "https://probe.invalid").pathname.startsWith("/node/")) overview();
}
scene.addEventListener("click", wrap(async (event) => { const button = event.target.closest("[data-node]"); if (button) await sdk.navigate("/node/" + button.dataset.node); }));
document.querySelector("#home").onclick = () => sdk.navigate("/");
document.querySelector("#mode").onclick = () => sdk.setMode(sdk.context.mode === "dark" ? "light" : "dark");
document.querySelector("#admin").onclick = () => sdk.openAdmin();
document.querySelector("#skins").onchange = (event) => sdk.setSkin(event.target.value);
const context = await sdk.ready;
document.querySelector("h1").textContent = context.brand.name || "观测台";
document.querySelector("#headline").textContent = context.theme.options?.headline === "network" ? "网络观测" : "机队总览";
document.querySelector("#skins").innerHTML = context.themes.filter((t) => t.type !== "addon").map((t) => `<option value="${e(t.id)}" ${t.id === context.theme.id ? "selected" : ""}>${e(t.name)}</option>`).join("");
order = await sdk.storage.get("sort") || "name";
await wrap(refresh)();
sdk.onContext(wrap(render));
const interval = setInterval(wrap(refresh), 5000);
addEventListener("pagehide", () => clearInterval(interval));
