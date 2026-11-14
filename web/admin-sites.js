// 后台「网站」：节点上的 nginx 站点（反向代理、静态网站、跳转）。面板只管自己写的
// conf.d/tza-site-*.conf，改完先 nginx -t 再重载，不通过自动恢复；节点上已有的其他站点只读显示。
// HTTPS 可直接选「证书」页的证书：主控自动把证书下发到这台节点，到位后站点才生效。
import { api, send, esc, toast, modal, guard } from "./api.js";
import { content, field, check, confirmAction, moreMenu, wireMoreMenus, render, state } from "./admin-state.js";

const KINDS = { proxy: "反向代理", static: "静态网站", redirect: "跳转" };
const STATES = { ok: ["运行中", "good"], waiting: ["等待", "warn"], error: ["出错", "bad"], disabled: ["已停用", ""] };

function target(s) {
  if (s.kind === "proxy") return `→ ${esc(s.upstream)}${s.websocket ? " · WebSocket" : ""}`;
  if (s.kind === "static") return `<code>${esc(s.root)}</code>`;
  return `${s.redirect_code} → ${esc(s.redirect_to)}${s.keep_path ? "（保留路径）" : ""}`;
}

export async function sitesPage(nodesReady) {
  const [, data, certList] = await Promise.all([nodesReady, api("/admin/sites"), api("/admin/certs").catch(() => ({ certs: [] }))]);
  const nodeName = (id) => state.nodes.find((n) => n.id === id)?.name || id;
  const certName = (id) => certList.certs.find((c) => c.id === id)?.domains.join("、") || "证书已删除";
  const rows = data.sites.map((s) => {
    const v = data.views[s.id] || { state: s.enabled ? "" : "disabled" };
    const [label, tone] = STATES[v.state] || ["等待同步", ""];
    const https = s.tls === "cert" ? `证书 ${esc(certName(s.cert_id))}` : s.tls === "files" ? "自备证书" : '<span class="muted">无</span>';
    return `<tr><td data-label="网站">${esc(s.name)}<small>${s.domains.map(esc).join("、")}</small></td><td data-label="节点">${esc(nodeName(s.node_id))}</td><td data-label="类型">${KINDS[s.kind]}<small>${target(s)}</small></td><td data-label="HTTPS">${https}${s.force_https ? "<small>强制 HTTPS</small>" : ""}</td><td data-label="状态"><span class="pill ${tone}">${label}</span>${v.note ? `<small>${esc(v.note)}</small>` : ""}</td><td data-label="操作"><div class="row-actions"><button data-edit-site="${esc(s.id)}">编辑</button>${moreMenu(`<button data-toggle-site="${esc(s.id)}">${s.enabled ? "停用" : "启用"}</button><button class="menu-danger" data-delete-site="${esc(s.id)}">删除</button>`)}</div></td></tr>`;
  }).join("");
  const nodePanels = Object.entries(data.nodes).map(([id, n]) => `<div class="site-node"><strong>${esc(nodeName(id))}</strong> ${n.installed ? `nginx ${esc(n.version || "")} · ${n.active ? '<span class="pill good">运行中</span>' : '<span class="pill bad">没在运行</span>'}` : `<span class="pill bad">${esc(n.error || "没装 nginx")}</span>`}${n.error && n.installed ? `<small class="warn-text">${esc(n.error)}</small>${n.output ? `<pre class="audit-detail">${esc(n.output)}</pre>` : ""}` : ""}${n.others?.length ? `<small>节点上的其他站点（只读）：${n.others.map((o) => `${o.names.length ? o.names.map(esc).join("、") : "默认站点"} <code>${esc(o.file)}</code>`).join("；")}</small>` : ""}</div>`).join("");
  content().innerHTML = `<section data-part="page-heading" class="page-heading"><div><div class="eyebrow">SITES</div><h1>网站<span>${data.sites.length}</span></h1><p>节点上的 nginx 站点：反向代理、静态网站、跳转。改完先 nginx -t 检查，不通过自动恢复，节点上已有的其他站点不动。</p></div><div class="heading-actions"><button id="sync-sites">↻ 立即同步</button><button id="add-site" class="primary">＋ 添加网站</button></div></section>
  <section data-part="panel" class="panel"><div class="table-scroll"><table class="stack"><thead><tr><th>网站</th><th>节点</th><th>类型</th><th>HTTPS</th><th>状态</th><th>操作</th></tr></thead><tbody>${rows || '<tr><td colspan="6" class="empty-cell">还没有网站。节点要装好 nginx，并用 --manage 接入。</td></tr>'}</tbody></table></div></section>
  ${nodePanels ? `<section data-part="panel" class="panel"><div class="panel-head"><h3>节点上的 nginx</h3></div><div class="site-nodes">${nodePanels}</div></section>` : ""}`;
  const byId = (id) => data.sites.find((s) => s.id === id);
  wireMoreMenus(content());
  content().querySelector("#add-site").onclick = () => siteForm(null, data, certList.certs);
  content().querySelector("#sync-sites").onclick = guard(async () => { toast("正在同步…"); await send("/admin/sites/sync", "POST"); await render(); toast("同步完成"); });
  content().querySelectorAll("[data-edit-site]").forEach((b) => (b.onclick = () => siteForm(byId(b.dataset.editSite), data, certList.certs)));
  content().querySelectorAll("[data-toggle-site]").forEach((b) => (b.onclick = guard(async () => {
    const s = byId(b.dataset.toggleSite);
    await send(`/admin/sites/${s.id}`, "PUT", { ...s, enabled: !s.enabled });
    await render();
  })));
  content().querySelectorAll("[data-delete-site]").forEach((b) => (b.onclick = () => {
    const s = byId(b.dataset.deleteSite);
    confirmAction(`删除网站 · ${s.name}`, `删除「${s.name}」，节点上的 nginx 配置在下一次同步时移除并重载。网站目录和上游服务不受影响。`, async () => { await send(`/admin/sites/${s.id}`, "DELETE"); await render(); }, { label: "删除" });
  }));
}

function siteForm(existing, data, certList) {
  const s = existing || { enabled: true, node_id: "", domains: [], kind: "proxy", upstream: "http://127.0.0.1:", websocket: false, root: "/var/www/", redirect_to: "https://", redirect_code: 301, keep_path: true, tls: "none", cert_id: "", cert_path: "", key_path: "", force_https: true, cloudflare_real_ip: false, max_body_mb: 0 };
  const nodeOptions = state.nodes.map((n) => `<option value="${esc(n.id)}" ${n.id === s.node_id ? "selected" : ""} ${data.capable.includes(n.id) || n.id === s.node_id ? "" : "disabled"}>${esc(n.name)}${data.capable.includes(n.id) ? "" : "（未开启管理通道）"}</option>`).join("");
  const certOptions = certList.map((c) => `<option value="${esc(c.id)}" ${c.id === s.cert_id ? "selected" : ""}>${esc(c.domains.join("、"))}${c.status === "valid" ? "" : "（还没签好）"}</option>`).join("");
  const radio = (name, value, label, current) => `<label class="checkbox"><input type="radio" name="${name}" value="${value}" ${current === value ? "checked" : ""}>${label}</label>`;
  const d = modal(existing ? `编辑网站 · ${existing.name}` : "添加网站", `<form id="site-form">
  <section class="form-section"><h3>基本</h3><div class="form-grid">${field("名称（可空，默认第一个域名）", "name", existing?.name || "", "text", 'maxlength="40"')}<label>节点<select name="node_id" required><option value="">选择节点</option>${nodeOptions}</select></label></div><label>域名（每行一个，可写 *.example.com）<textarea name="domains" rows="2" required spellcheck="false" class="code-input">${esc(s.domains.join("\n"))}</textarea></label>${check("启用", "enabled", s.enabled)}</section>
  <section class="form-section"><h3>类型</h3><div class="option-tiles" role="radiogroup">${radio("kind", "proxy", "反向代理", s.kind)}${radio("kind", "static", "静态网站", s.kind)}${radio("kind", "redirect", "跳转", s.kind)}</div>
  <div data-kind="proxy">${field("上游地址", "upstream", s.upstream || "", "text", 'placeholder="http://127.0.0.1:3000"')}${check("WebSocket（长连接，Upgrade 透传）", "websocket", s.websocket)}</div>
  <div data-kind="static">${field("网站目录（节点上的绝对路径）", "root", s.root || "", "text", 'placeholder="/var/www/example"')}<p class="hint">目录要提前放好文件，nginx 用户要能读。</p></div>
  <div data-kind="redirect"><div class="form-grid">${field("跳转到", "redirect_to", s.redirect_to || "", "url", 'placeholder="https://new.example.com"')}<label>状态码<select name="redirect_code">${[301, 302, 307, 308].map((c) => `<option value="${c}" ${c === s.redirect_code ? "selected" : ""}>${c}${c === 301 ? "（永久）" : c === 302 ? "（临时）" : c === 308 ? "（永久，保留方法）" : "（临时，保留方法）"}</option>`).join("")}</select></label></div>${check("保留路径和参数（/a?b=1 → 目标/a?b=1）", "keep_path", s.keep_path)}</div></section>
  <section class="form-section"><h3>HTTPS</h3><div class="option-tiles" role="radiogroup">${radio("tls", "none", "不用", s.tls)}${radio("tls", "cert", "用「证书」页的证书", s.tls)}${radio("tls", "files", "节点上已有的证书文件", s.tls)}</div>
  <div data-tls="cert"><label>证书<select name="cert_id"><option value="">选择证书</option>${certOptions}</select></label><p class="hint">保存后主控会把这张证书下发到这台节点（/etc/tza-certs/&lt;域名&gt;/，续期后自动重载 nginx），证书到位后站点才启用。证书要包含站点的全部域名。${certList.length ? "" : "还没有证书，先到「证书」页申请。"}</p></div>
  <div data-tls="files"><div class="form-grid">${field("证书文件（含中间证书）", "cert_path", s.cert_path || "", "text", 'placeholder="/etc/nginx/ssl/example.crt"')}${field("私钥文件", "key_path", s.key_path || "", "text", 'placeholder="/etc/nginx/ssl/example.key"')}</div></div>
  <div data-tls-any>${check("HTTP 自动跳到 HTTPS", "force_https", s.force_https)}</div></section>
  <section class="form-section"><h3>其他</h3>${check("经过 Cloudflare 代理（从 CF-Connecting-IP 取访客真实 IP）", "cloudflare_real_ip", s.cloudflare_real_ip)}${field("上传大小上限（MB，0 为 nginx 默认 1 MB）", "max_body_mb", s.max_body_mb || 0, "number", 'min="0" max="10240"')}</section>
  <div class="form-footer"><button type="submit" class="primary">保存</button></div></form>`);
  const form = d.querySelector("#site-form");
  const el = (n) => form.elements.namedItem(n);
  const picked = (n) => form.querySelector(`input[name="${n}"]:checked`)?.value;
  const sync = () => {
    form.querySelectorAll("[data-kind]").forEach((x) => (x.hidden = x.dataset.kind !== picked("kind")));
    form.querySelectorAll("[data-tls]").forEach((x) => (x.hidden = x.dataset.tls !== picked("tls")));
    form.querySelector("[data-tls-any]").hidden = picked("tls") === "none";
  };
  form.querySelectorAll('input[type="radio"]').forEach((r) => (r.onchange = sync));
  sync();
  form.onsubmit = guard(async (e) => {
    e.preventDefault();
    const body = {
      name: el("name").value.trim(),
      node_id: el("node_id").value,
      enabled: el("enabled").checked,
      domains: el("domains").value.split(/[\s,]+/).filter(Boolean),
      kind: picked("kind"),
      upstream: el("upstream").value.trim(),
      websocket: el("websocket").checked,
      root: el("root").value.trim(),
      redirect_to: el("redirect_to").value.trim(),
      redirect_code: Number(el("redirect_code").value),
      keep_path: el("keep_path").checked,
      tls: picked("tls"),
      cert_id: el("cert_id").value,
      cert_path: el("cert_path").value.trim(),
      key_path: el("key_path").value.trim(),
      force_https: el("force_https").checked,
      cloudflare_real_ip: el("cloudflare_real_ip").checked,
      max_body_mb: Number(el("max_body_mb").value || 0),
    };
    if (existing) await send(`/admin/sites/${existing.id}`, "PUT", body);
    else await send("/admin/sites", "POST", body);
    d.close();
    toast(body.tls === "cert" ? "已保存，证书下发到节点后生效" : "已保存，正在写到节点");
    await render();
  });
}
