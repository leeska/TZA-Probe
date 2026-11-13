// 后台「代理」：在受管节点上开入站（VLESS Reality / Shadowsocks 2022，内核可选 xray 或 sing-box）。
// 主控下载固定版本的内核并校验，经管理通道下发到节点，用 systemd 托管；入站里的使用者来自「使用者」页，
// 停用、过期或流量用完的使用者在下一次同步时从配置里去掉。
import { api, send, esc, toast, modal, guard } from "./api.js";
import { content, field, check, confirmAction, moreMenu, wireMoreMenus, render, state } from "./admin-state.js";

const PROTOCOLS = { "vless-reality": "VLESS Reality", ss2022: "Shadowsocks 2022" };

function statusCell(inbound, view) {
  if (!inbound.enabled) return '<span class="pill">已停用</span>';
  if (!view) return '<span class="pill">等待同步</span>';
  if (view.error) return `<span class="pill bad">未生效</span><small>${esc(view.error)}</small>`;
  const k = view.kernels?.[inbound.kernel];
  if (!k) return '<span class="pill">等待同步</span>';
  if (k.error) return `<span class="pill bad">出错</span><small>${esc(k.error)}</small>`;
  if (k.synced && k.active) return `<span class="pill good">运行中</span><small>${esc(inbound.kernel)} ${esc(k.version || "")}</small>`;
  return '<span class="pill">同步中</span>';
}

export async function proxyPage(nodesReady) {
  const [, data, people] = await Promise.all([nodesReady, api("/admin/proxy"), api("/admin/users").catch(() => [])]);
  const views = new Map(data.nodes.map((v) => [v.id, v]));
  const nodeName = (id) => state.nodes.find((n) => n.id === id)?.name || id;
  const rows = data.inbounds.map((i) => `<tr data-inbound="${esc(i.id)}"><td data-label="入站">${esc(i.name)}<small>${esc(nodeName(i.node_id))}</small></td><td data-label="协议">${PROTOCOLS[i.protocol] || esc(i.protocol)}<small>${esc(i.kernel)}</small></td><td data-label="端口">${i.port}<small>${esc(i.address || "未填连接地址")}</small></td><td data-label="使用者">${i.user_ids.length ? `${i.user_ids.length} 位` : "全部"}</td><td data-label="状态">${statusCell(i, views.get(i.node_id))}</td><td data-label="操作"><div class="row-actions"><button data-edit-inbound="${esc(i.id)}">编辑</button><button data-links-inbound="${esc(i.id)}">链接</button>${moreMenu(`<button data-toggle-inbound="${esc(i.id)}">${i.enabled ? "停用" : "启用"}</button><button class="menu-danger" data-delete-inbound="${esc(i.id)}">删除</button>`)}</div></td></tr>`).join("");
  const kernels = data.kernels.map((k) => `${esc(k.name)} ${esc(k.version)}`).join(" · ");
  content().innerHTML = `<section data-part="page-heading" class="page-heading"><div><div class="eyebrow">PROXY</div><h1>代理<span>${data.inbounds.length}</span></h1><p>在开启了管理通道的节点上开入站，分给「使用者」用。内核：${kernels}（主控下载并校验后分发，节点不用访问 GitHub）。</p></div><div class="heading-actions"><button id="sync-proxy">↻ 立即同步</button><button id="add-inbound" class="primary">＋ 添加入站</button></div></section><section data-part="panel" class="panel"><div class="table-scroll"><table class="stack"><thead><tr><th>入站</th><th>协议</th><th>端口</th><th>使用者</th><th>状态</th><th>操作</th></tr></thead><tbody>${rows || '<tr><td colspan="6" class="empty-cell">还没有入站。节点要先用 --manage 开启管理通道。</td></tr>'}</tbody></table></div></section>`;
  const byId = (id) => data.inbounds.find((i) => i.id === id);
  wireMoreMenus(content());
  content().querySelector("#add-inbound").onclick = () => inboundForm(null, data, people);
  content().querySelector("#sync-proxy").onclick = guard(async () => {
    toast("正在同步，首次安装内核可能需要一两分钟…");
    await send("/admin/proxy/sync", "POST");
    await render();
    toast("同步完成");
  });
  content().querySelectorAll("[data-edit-inbound]").forEach((b) => (b.onclick = () => inboundForm(byId(b.dataset.editInbound), data, people)));
  content().querySelectorAll("[data-links-inbound]").forEach((b) => (b.onclick = guard(() => showLinks(byId(b.dataset.linksInbound)))));
  content().querySelectorAll("[data-toggle-inbound]").forEach((b) => (b.onclick = guard(async () => {
    const i = byId(b.dataset.toggleInbound);
    await send(`/admin/proxy/inbounds/${i.id}`, "PUT", { ...i, enabled: !i.enabled });
    await render();
  })));
  content().querySelectorAll("[data-delete-inbound]").forEach((b) => (b.onclick = () => {
    const i = byId(b.dataset.deleteInbound);
    confirmAction(`删除入站 · ${i.name}`, `删除「${i.name}」，节点上对应的端口会在下一次同步时关闭，使用者的这条链接随之失效。`, async () => { await send(`/admin/proxy/inbounds/${i.id}`, "DELETE"); await render(); }, { label: "删除" });
  }));
}

async function showLinks(inbound) {
  const links = await api(`/admin/proxy/inbounds/${inbound.id}/links`);
  const items = links.map((l) => `<li><strong>${esc(l.name)}</strong><div class="command-block"><pre>${esc(l.link)}</pre><button type="button" class="primary" data-copy="${esc(l.link)}">复制</button></div></li>`).join("");
  const d = modal(`链接 · ${inbound.name}`, `<p class="hint">每位使用者一条，导入客户端即可。停用、过期或流量用完的使用者不在这里。链接就是凭证，请单独发给本人。</p><ul class="proxy-links">${items || "<li>没有可用的使用者</li>"}</ul>`);
  d.querySelectorAll("[data-copy]").forEach((b) => (b.onclick = () => navigator.clipboard?.writeText(b.dataset.copy).then(() => toast("已复制"), () => toast("复制失败，请手动选择"))));
}

function inboundForm(existing, data, people) {
  const i = existing || { name: "", node_id: "", kernel: "xray", protocol: "vless-reality", port: 443, enabled: true, address: "", user_ids: [], reality: { dest: "", server_name: "" } };
  const views = new Map(data.nodes.map((v) => [v.id, v]));
  const nodeOptions = state.nodes.map((n) => {
    const v = views.get(n.id);
    const note = v?.capable ? "" : "（未开启代理能力）";
    return `<option value="${esc(n.id)}" ${n.id === i.node_id ? "selected" : ""}>${esc(n.name)}${note}</option>`;
  }).join("");
  const userBoxes = people.map((u) => `<label class="checkbox"><input type="checkbox" name="user" value="${esc(u.id)}" ${i.user_ids.includes(u.id) ? "checked" : ""}>${esc(u.name)}<small>${u.status === "active" ? "" : "当前不可用"}</small></label>`).join("") || '<p class="hint">还没有使用者，先在「使用者」里添加。</p>';
  const d = modal(existing ? `编辑入站 · ${existing.name}` : "添加入站", `<form id="inbound-form"><section class="form-section"><h3>基本</h3><div class="form-grid">${field("名称", "name", i.name, "text", 'required maxlength="40" placeholder="如 日本直连"')}<label>节点<select name="node_id" required><option value="">选择节点</option>${nodeOptions}</select></label><label>协议<select name="protocol"><option value="vless-reality" ${i.protocol === "vless-reality" ? "selected" : ""}>VLESS Reality（直连）</option><option value="ss2022" ${i.protocol === "ss2022" ? "selected" : ""}>Shadowsocks 2022（中转 / 落地）</option></select></label><label>内核<select name="kernel"><option value="xray" ${i.kernel === "xray" ? "selected" : ""}>xray</option><option value="sing-box" ${i.kernel === "sing-box" ? "selected" : ""}>sing-box</option></select></label>${field("端口", "port", i.port, "number", 'min="1" max="65535" required')}${field("连接地址（客户端连的域名或 IP）", "address", i.address, "text", 'maxlength="253" placeholder="如 jp.example.com 或节点公网 IP"')}</div>${check("启用", "enabled", i.enabled)}</section><section class="form-section" data-reality><h3>Reality</h3><div class="form-grid">${field("目标网站（域名:443）", "dest", i.reality?.dest || "", "text", 'placeholder="与节点同地区、支持 TLS 1.3 的境外网站"')}${field("SNI（留空同目标域名）", "server_name", i.reality?.server_name || "", "text")}</div><p class="hint">Reality 借用目标网站的 TLS 握手。别用 microsoft、apple 这类大家都在用的站，挑一个和节点同地区、支持 TLS 1.3 的网站。密钥由主控生成，编辑时保留。</p></section><section class="form-section"><h3>使用者</h3><p class="hint">都不勾就是全部使用者都能用。停用、过期或流量用完的使用者会自动排除。</p><div class="node-checkboxes">${userBoxes}</div></section><div class="form-footer"><button type="submit" class="primary">保存</button></div></form>`);
  const form = d.querySelector("#inbound-form");
  const el = (n) => form.elements.namedItem(n);
  const syncProtocol = () => {
    const reality = el("protocol").value === "vless-reality";
    form.querySelector("[data-reality]").hidden = !reality;
    el("dest").required = reality;
  };
  el("protocol").onchange = syncProtocol;
  syncProtocol();
  form.onsubmit = guard(async (e) => {
    e.preventDefault();
    const body = {
      name: el("name").value.trim(),
      node_id: el("node_id").value,
      protocol: el("protocol").value,
      kernel: el("kernel").value,
      port: Number(el("port").value),
      address: el("address").value.trim(),
      enabled: el("enabled").checked,
      user_ids: [...form.querySelectorAll('[name="user"]:checked')].map((b) => b.value),
      reality: el("protocol").value === "vless-reality" ? { dest: el("dest").value.trim(), server_name: el("server_name").value.trim() } : undefined,
    };
    if (existing) await send(`/admin/proxy/inbounds/${existing.id}`, "PUT", body);
    else await send("/admin/proxy/inbounds", "POST", body);
    d.close();
    toast("已保存，正在下发到节点");
    await render();
  });
}
