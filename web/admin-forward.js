// 后台「转发」：端口转发与隧道，跑在节点的 xray 里（和「代理」共用内核、同步和端口检查）。
// 直接转发：入口节点的端口 → 目标地址；隧道：入口节点 → 加密（Shadowsocks 2022）→ 出口节点 → 目标。
// 每条规则可以归属一位使用者：流量计入他的额度，他停用、过期或用完时规则自动停掉。
import { api, send, esc, toast, modal, guard } from "./api.js";
import { bytes } from "./format.js";
import { content, field, check, confirmAction, moreMenu, wireMoreMenus, render, state } from "./admin-state.js";

const NETWORKS = { "tcp,udp": "TCP + UDP", tcp: "TCP", udp: "UDP" };

function nodeState(views, id) {
  const v = views.get(id);
  if (!v) return { tone: "", text: "等待同步" };
  if (v.error) return { tone: "bad", text: v.error };
  const k = v.kernels?.xray;
  if (!k) return { tone: "", text: "等待同步" };
  if (k.error) return { tone: "bad", text: k.error };
  if (k.synced && k.active) return { tone: "good", text: k.stats_error ? `流量读取失败：${k.stats_error}` : "" };
  return { tone: "", text: "同步中" };
}

function statusCell(f, views, people) {
  if (!f.enabled) return '<span class="pill">已停用</span>';
  if (f.user_id) {
    const u = people.find((p) => p.id === f.user_id);
    if (!u) return '<span class="pill bad">已停止</span><small>归属的使用者已删除</small>';
    if (u.status !== "active") return `<span class="pill bad">已停止</span><small>「${esc(u.name)}」${u.status === "exceeded" ? "流量已用完" : u.status === "expired" ? "已过期" : "已停用"}</small>`;
  }
  const sides = [nodeState(views, f.node_id)];
  if (f.exit) sides.push(nodeState(views, f.exit.node_id));
  const bad = sides.find((s) => s.tone === "bad");
  if (bad) return `<span class="pill bad">出错</span><small>${esc(bad.text)}</small>`;
  if (sides.every((s) => s.tone === "good")) return `<span class="pill good">运行中</span>${sides.map((s) => s.text).filter(Boolean).map((t) => `<small>${esc(t)}</small>`).join("")}`;
  return '<span class="pill">同步中</span>';
}

export async function forwardPage(nodesReady) {
  const [, data, people] = await Promise.all([nodesReady, api("/admin/proxy"), api("/admin/users").catch(() => [])]);
  const views = new Map(data.nodes.map((v) => [v.id, v]));
  const nodeName = (id) => state.nodes.find((n) => n.id === id)?.name || id;
  const userName = (id) => people.find((u) => u.id === id)?.name || "已删除的使用者";
  const rows = data.forwards.map((f) => {
    const path = f.exit
      ? `${esc(nodeName(f.node_id))} → ${esc(nodeName(f.exit.node_id))}<small>隧道 · 出口端口 ${f.exit.port}</small>`
      : `${esc(nodeName(f.node_id))}<small>直接转发</small>`;
    return `<tr data-forward="${esc(f.id)}"><td data-label="规则">${esc(f.name)}<small>${NETWORKS[f.network] || esc(f.network)}</small></td><td data-label="入口">:${f.port}</td><td data-label="路径">${path}</td><td data-label="目标">${esc(f.target)}:${f.target_port}</td><td data-label="使用者">${f.user_id ? esc(userName(f.user_id)) : '<span class="muted">只统计</span>'}</td><td data-label="累计流量">↑${bytes(f.traffic_up)}<small>↓${bytes(f.traffic_down)}</small></td><td data-label="状态">${statusCell(f, views, people)}</td><td data-label="操作"><div class="row-actions"><button data-edit-forward="${esc(f.id)}">编辑</button>${moreMenu(`<button data-toggle-forward="${esc(f.id)}">${f.enabled ? "停用" : "启用"}</button><button class="menu-danger" data-delete-forward="${esc(f.id)}">删除</button>`)}</div></td></tr>`;
  }).join("");
  content().innerHTML = `<section data-part="page-heading" class="page-heading"><div><div class="eyebrow">FORWARD</div><h1>转发<span>${data.forwards.length}</span></h1><p>端口转发与中转隧道，跑在节点的 xray 里（节点要用 --manage 接入）。流量按规则统计，归属使用者的计入他的额度。</p></div><div class="heading-actions"><button id="sync-forward">↻ 立即同步</button><button id="add-forward" class="primary">＋ 添加转发</button></div></section><section data-part="panel" class="panel"><div class="table-scroll"><table class="stack"><thead><tr><th>规则</th><th>入口</th><th>路径</th><th>目标</th><th>使用者</th><th>累计流量</th><th>状态</th><th>操作</th></tr></thead><tbody>${rows || '<tr><td colspan="8" class="empty-cell">还没有转发规则。</td></tr>'}</tbody></table></div></section>`;
  const byId = (id) => data.forwards.find((f) => f.id === id);
  wireMoreMenus(content());
  content().querySelector("#add-forward").onclick = () => forwardForm(null, data, people);
  content().querySelector("#sync-forward").onclick = guard(async () => {
    toast("正在同步…");
    await send("/admin/proxy/sync", "POST");
    await render();
    toast("同步完成");
  });
  content().querySelectorAll("[data-edit-forward]").forEach((b) => (b.onclick = () => forwardForm(byId(b.dataset.editForward), data, people)));
  content().querySelectorAll("[data-toggle-forward]").forEach((b) => (b.onclick = guard(async () => {
    const f = byId(b.dataset.toggleForward);
    await send(`/admin/proxy/forwards/${f.id}`, "PUT", { ...f, enabled: !f.enabled });
    await render();
  })));
  content().querySelectorAll("[data-delete-forward]").forEach((b) => (b.onclick = () => {
    const f = byId(b.dataset.deleteForward);
    confirmAction(`删除转发 · ${f.name}`, `删除「${f.name}」，节点上的端口会在下一次同步时关闭，累计流量记录一并删除。`, async () => { await send(`/admin/proxy/forwards/${f.id}`, "DELETE"); await render(); }, { label: "删除" });
  }));
}

function forwardForm(existing, data, people) {
  const f = existing || { name: "", enabled: true, user_id: "", node_id: "", port: "", network: "tcp,udp", target: "", target_port: "", exit: null };
  const views = new Map(data.nodes.map((v) => [v.id, v]));
  const options = (selected) => state.nodes.map((n) => `<option value="${esc(n.id)}" ${n.id === selected ? "selected" : ""}>${esc(n.name)}${views.get(n.id)?.capable ? "" : "（未开启管理通道）"}</option>`).join("");
  const userOptions = people.map((u) => `<option value="${esc(u.id)}" ${u.id === f.user_id ? "selected" : ""}>${esc(u.name)}${u.status === "active" ? "" : "（当前不可用）"}</option>`).join("");
  const tunnel = !!f.exit;
  const d = modal(existing ? `编辑转发 · ${existing.name}` : "添加转发", `<form id="forward-form"><section class="form-section"><h3>基本</h3><div class="form-grid">${field("名称", "name", f.name, "text", 'required maxlength="40" placeholder="如 游戏中转"')}<label>使用者<select name="user_id"><option value="">不归属（只统计流量）</option>${userOptions}</select></label></div>${check("启用", "enabled", f.enabled)}</section><section class="form-section"><h3>入口</h3><div class="form-grid"><label>入口节点<select name="node_id" required><option value="">选择节点</option>${options(f.node_id)}</select></label>${field("入口端口", "port", f.port, "number", 'min="1" max="65535" required')}<label>协议<select name="network">${Object.entries(NETWORKS).map(([v, l]) => `<option value="${v}" ${v === f.network ? "selected" : ""}>${l}</option>`).join("")}</select></label></div></section><section class="form-section"><h3>方式</h3><div class="option-tiles" role="radiogroup"><label class="checkbox"><input type="radio" name="mode" value="direct" ${tunnel ? "" : "checked"}>直接转发</label><label class="checkbox"><input type="radio" name="mode" value="tunnel" ${tunnel ? "checked" : ""}>经隧道中转</label></div><p class="hint" data-mode-hint></p><div class="form-grid" data-tunnel><label>出口节点<select name="exit_node"><option value="">选择节点</option>${options(f.exit?.node_id)}</select></label><label>出口地址（入口节点连它）<span class="input-with-button"><input name="exit_address" value="${esc(f.exit?.address || "")}" maxlength="253" placeholder="出口节点的域名或公网 IP"><button type="button" data-fill-exit>填入节点 IP</button></span></label>${field("隧道端口（出口节点上）", "exit_port", f.exit?.port || "", "number", 'min="1" max="65535"')}</div></section><section class="form-section"><h3>目标</h3><div class="form-grid">${field("目标地址", "target", f.target, "text", 'required maxlength="253" placeholder="域名或 IP"')}${field("目标端口", "target_port", f.target_port, "number", 'min="1" max="65535" required')}</div></section><div class="form-footer"><button type="submit" class="primary">保存</button></div></form>`);
  const form = d.querySelector("#forward-form");
  const el = (n) => form.elements.namedItem(n);
  const mode = () => form.querySelector('input[name="mode"]:checked').value;
  const syncMode = () => {
    const t = mode() === "tunnel";
    form.querySelector("[data-tunnel]").hidden = !t;
    for (const n of ["exit_node", "exit_address", "exit_port"]) el(n).required = t;
    form.querySelector("[data-mode-hint]").textContent = t
      ? "入口节点把流量加密（Shadowsocks 2022）送到出口节点，由出口节点去连目标。出口节点上会开一个隧道端口，有防火墙的话要放行它。"
      : "入口节点自己连目标，适合目标就在附近或入口线路本身就好的情况。";
  };
  form.querySelectorAll('input[name="mode"]').forEach((r) => (r.onchange = syncMode));
  syncMode();
  form.querySelector("[data-fill-exit]").onclick = guard(async () => {
    const id = el("exit_node").value;
    if (!id) return toast("先选择出口节点");
    const { ip } = await api(`/admin/nodes/${id}/address`);
    el("exit_address").value = ip;
  });
  form.onsubmit = guard(async (e) => {
    e.preventDefault();
    const body = {
      name: el("name").value.trim(),
      enabled: el("enabled").checked,
      user_id: el("user_id").value,
      node_id: el("node_id").value,
      port: Number(el("port").value),
      network: el("network").value,
      target: el("target").value.trim(),
      target_port: Number(el("target_port").value),
      exit: mode() === "tunnel" ? { node_id: el("exit_node").value, address: el("exit_address").value.trim(), port: Number(el("exit_port").value) } : null,
    };
    if (existing) await send(`/admin/proxy/forwards/${existing.id}`, "PUT", body);
    else await send("/admin/proxy/forwards", "POST", body);
    d.close();
    await render();
    toast(existing ? "已保存，正在同步到节点" : "已添加，正在同步到节点");
  });
}
