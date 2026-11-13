// 后台「证书」：主控用 Let's Encrypt 签发（Cloudflare DNS 验证，支持通配符），有效期剩三分之一
// （最多 30 天）时自动续期，签发或续期后写到所选节点的目录并执行重载命令（节点要用 --manage 接入）。
import { api, send, esc, toast, modal, guard } from "./api.js";
import { content, field, confirmAction, moreMenu, wireMoreMenus, render, state } from "./admin-state.js";

const day = (unix) => (unix ? new Date(unix * 1000).toLocaleDateString("zh-CN") : "");
let poller;

function statusCell(c) {
  if (c.issuing) return '<span class="pill">签发中…</span><small>DNS 验证通常要一两分钟</small>';
  if (c.status === "pending") return '<span class="pill">等待签发</span>';
  const left = c.not_after ? Math.floor((c.not_after * 1000 - Date.now()) / 86400000) : null;
  const expiry = c.not_after ? `<small>${day(c.not_after)} 到期（剩 ${left} 天）</small>` : "";
  if (c.status === "error") return `<span class="pill bad">签发失败</span><small>${esc(c.error)}</small>${c.next_attempt ? `<small>${new Date(c.next_attempt * 1000).toLocaleString("zh-CN")} 再试</small>` : ""}${expiry}`;
  if (left !== null && left <= 0) return `<span class="pill bad">已过期</span>${expiry}`;
  return `<span class="pill ${left !== null && left < 14 ? "warn" : "good"}">有效</span>${expiry}`;
}

function targetsCell(c, nodeName) {
  if (!c.targets.length) return '<span class="muted">不下发（只在面板下载）</span>';
  return c.targets.map((t) => {
    const state = t.error ? `<span class="pill bad">失败</span> ${esc(t.error)}` : t.deployed && t.deployed === c.serial ? '<span class="pill good">已下发</span>' : '<span class="pill">待下发</span>';
    return `<div class="cert-target"><strong>${esc(nodeName(t.node_id))}</strong> <code>${esc(t.dir)}</code><small>${state}${t.output ? ` · ${esc(t.output.slice(-120))}` : ""}</small></div>`;
  }).join("");
}

export async function certsPage(nodesReady) {
  clearTimeout(poller);
  const [, data] = await Promise.all([nodesReady, api("/admin/certs")]);
  const nodeName = (id) => state.nodes.find((n) => n.id === id)?.name || id;
  const rows = data.certs.map((c) => `<tr><td data-label="域名">${c.domains.map(esc).join("<br>")}<small>${esc(data.dns.find((a) => a.id === c.dns_id)?.name || "DNS 账号已删除")}${c.issuer ? ` · ${esc(c.issuer)}` : ""}</small></td><td data-label="状态">${statusCell(c)}</td><td data-label="下发">${targetsCell(c, nodeName)}</td><td data-label="操作"><div class="row-actions"><button data-edit-cert="${esc(c.id)}">编辑</button>${moreMenu(`<button data-renew="${esc(c.id)}">立即续期</button>${c.targets.length ? `<button data-redeploy="${esc(c.id)}">重新下发</button>` : ""}${c.serial ? `<a class="button" href="/api/v1/admin/certs/${esc(c.id)}/download?part=fullchain">下载证书</a><a class="button" href="/api/v1/admin/certs/${esc(c.id)}/download?part=key">下载私钥</a>` : ""}<button class="menu-danger" data-delete-cert="${esc(c.id)}">删除</button>`)}</div></td></tr>`).join("");
  const dnsRows = data.dns.map((a) => `<tr><td data-label="名称">${esc(a.name)}<small>Cloudflare · 令牌 ${esc(a.token_hint)}</small></td><td data-label="操作"><div class="row-actions"><button data-edit-dns="${esc(a.id)}">编辑</button><button class="danger-text" data-delete-dns="${esc(a.id)}">删除</button></div></td></tr>`).join("");
  content().innerHTML = `<section data-part="page-heading" class="page-heading"><div><div class="eyebrow">CERTIFICATES</div><h1>证书<span>${data.certs.length}</span></h1><p>Let's Encrypt 证书，用 Cloudflare DNS 验证（支持通配符）。到期前自动续期，并写到所选节点的目录、执行重载命令。</p></div><div class="heading-actions"><button id="add-cert" class="primary" ${data.dns.length ? "" : "disabled"}>＋ 申请证书</button></div></section>
  ${data.staging ? '<section class="panel docker-error"><p>正在使用 Let\'s Encrypt <strong>测试环境</strong>：签出的证书浏览器不信任，只用来试流程。</p></section>' : ""}
  <section data-part="panel" class="panel"><div class="table-scroll"><table class="stack"><thead><tr><th>域名</th><th>状态</th><th>下发</th><th>操作</th></tr></thead><tbody>${rows || `<tr><td colspan="4" class="empty-cell">${data.dns.length ? "还没有证书。" : "先在下面添加一个 Cloudflare DNS 账号。"}</td></tr>`}</tbody></table></div></section>
  <section data-part="panel" class="panel"><div class="panel-head"><h3>Cloudflare DNS 账号</h3><button id="add-dns">＋ 添加</button></div><div class="table-scroll"><table class="stack"><thead><tr><th>名称</th><th>操作</th></tr></thead><tbody>${dnsRows || '<tr><td colspan="2" class="empty-cell">还没有。需要一个有「区域 → 读取」和「DNS → 编辑」权限的 API 令牌。</td></tr>'}</tbody></table></div><label class="checkbox cert-staging"><input type="checkbox" id="cert-staging" ${data.staging ? "checked" : ""}>使用 Let's Encrypt 测试环境（不受签发次数限制，证书不被浏览器信任）</label></section>`;
  wireMoreMenus(content());
  const byId = (id) => data.certs.find((c) => c.id === id);
  const q = (sel, fn) => content().querySelectorAll(sel).forEach((b) => (b.onclick = () => fn(b)));
  content().querySelector("#add-cert").onclick = () => certForm(null, data);
  content().querySelector("#add-dns").onclick = () => dnsForm(null);
  q("[data-edit-cert]", (b) => certForm(byId(b.dataset.editCert), data));
  q("[data-edit-dns]", (b) => dnsForm(data.dns.find((a) => a.id === b.dataset.editDns)));
  q("[data-renew]", (b) => guard(async () => { await send(`/admin/certs/${b.dataset.renew}/renew`, "POST"); toast("已开始续期"); await render(); })());
  q("[data-redeploy]", (b) => guard(async () => { await send(`/admin/certs/${b.dataset.redeploy}/deploy`, "POST"); toast("正在重新下发"); await render(); })());
  q("[data-delete-cert]", (b) => {
    const c = byId(b.dataset.deleteCert);
    confirmAction(`删除证书 · ${c.domains[0]}`, `不再续期和下发「${c.domains.join("、")}」。节点上已经写好的文件不会删除。`, async () => { await send(`/admin/certs/${c.id}`, "DELETE"); await render(); }, { label: "删除" });
  });
  q("[data-delete-dns]", (b) => {
    const a = data.dns.find((x) => x.id === b.dataset.deleteDns);
    confirmAction(`删除 DNS 账号 · ${a.name}`, `从面板删除这个 Cloudflare 令牌（Cloudflare 那边的令牌不受影响）。`, async () => { await send(`/admin/certs/dns/${a.id}`, "DELETE"); await render(); }, { label: "删除" });
  });
  content().querySelector("#cert-staging").onchange = guard(async (e) => {
    await send("/admin/certs/settings", "PUT", { staging: e.target.checked });
    toast(e.target.checked ? "已切到测试环境，之后签发的证书不被浏览器信任" : "已切回正式环境");
    await render();
  });
  // 有证书在签发或等待下发时，每 5 秒刷新一次。
  const busy = data.certs.some((c) => c.issuing || c.status === "pending" || (c.serial && c.targets.some((t) => !t.error && t.deployed !== c.serial)));
  const tick = () => {
    if (state.tab !== "certs") return;
    if (document.querySelector("dialog[open]")) poller = setTimeout(tick, 5000); // 不打断正在填的表单
    else render();
  };
  if (busy) poller = setTimeout(tick, 5000);
}

function dnsForm(existing) {
  const d = modal(existing ? `编辑 DNS 账号 · ${existing.name}` : "添加 Cloudflare DNS 账号", `<form id="dns-form">${field("名称", "name", existing?.name || "", "text", 'required maxlength="40" placeholder="如 主账号"')}<label>API 令牌<input name="token" type="password" autocomplete="off" ${existing ? 'placeholder="留空则不改"' : "required"}></label><p class="hint">在 Cloudflare「我的个人资料 → API 令牌」创建，用「编辑区域 DNS」模板：权限是区域 → DNS → 编辑，加上区域 → 区域 → 读取；区域资源选要签证书的域名。保存时会向 Cloudflare 校验一次。</p><div class="form-footer"><button type="submit" class="primary">保存</button></div></form>`);
  const form = d.querySelector("#dns-form");
  form.onsubmit = guard(async (e) => {
    e.preventDefault();
    const body = { name: form.elements.namedItem("name").value.trim(), token: form.elements.namedItem("token").value.trim() };
    if (existing) await send(`/admin/certs/dns/${existing.id}`, "PUT", body);
    else await send("/admin/certs/dns", "POST", body);
    d.close();
    toast("已保存");
    await render();
  });
}

function targetRow(t = {}) {
  const options = state.nodes.map((n) => `<option value="${esc(n.id)}" ${n.id === t.node_id ? "selected" : ""}>${esc(n.name)}</option>`).join("");
  return `<div class="cert-target-row"><select name="t_node" required><option value="">选择节点</option>${options}</select><input name="t_dir" value="${esc(t.dir || "")}" placeholder="目录，留空为 /etc/tza-certs/<域名>"><input name="t_reload" value="${esc(t.reload || "")}" placeholder="重载命令，如 systemctl reload nginx"><button type="button" class="danger-text" data-remove-target>移除</button></div>`;
}

function certForm(existing, data) {
  const c = existing || { domains: [], dns_id: data.dns[0]?.id || "", targets: [] };
  const dnsOptions = data.dns.map((a) => `<option value="${esc(a.id)}" ${a.id === c.dns_id ? "selected" : ""}>${esc(a.name)}</option>`).join("");
  const d = modal(existing ? `编辑证书 · ${existing.domains[0]}` : "申请证书", `<form id="cert-form"><section class="form-section"><h3>域名</h3><label>域名（每行一个，通配符写 *.example.com）<textarea name="domains" rows="3" required spellcheck="false" class="code-input" placeholder="example.com&#10;*.example.com">${esc(c.domains.join("\n"))}</textarea></label><label>DNS 账号<select name="dns_id" required>${dnsOptions}</select></label>${existing ? '<p class="hint">改了域名会立即重新签发。</p>' : ""}</section><section class="form-section"><h3>下发到节点</h3><p class="hint">写入 <code>fullchain.pem</code> 和 <code>privkey.pem</code>（私钥 0600），然后在该目录下执行重载命令。nginx 里指向 <code>&lt;目录&gt;/fullchain.pem</code> 和 <code>&lt;目录&gt;/privkey.pem</code> 即可。不添加就只在面板里下载。</p><div class="cert-targets">${c.targets.map(targetRow).join("")}</div><button type="button" id="add-target">＋ 添加节点</button></section><div class="form-footer"><button type="submit" class="primary">${existing ? "保存" : "申请"}</button></div></form>`);
  const form = d.querySelector("#cert-form");
  const box = form.querySelector(".cert-targets");
  const wireRemove = () => box.querySelectorAll("[data-remove-target]").forEach((b) => (b.onclick = () => b.closest(".cert-target-row").remove()));
  form.querySelector("#add-target").onclick = () => { box.insertAdjacentHTML("beforeend", targetRow()); wireRemove(); };
  wireRemove();
  form.onsubmit = guard(async (e) => {
    e.preventDefault();
    const body = {
      domains: form.elements.namedItem("domains").value.split(/[\s,]+/).filter(Boolean),
      dns_id: form.elements.namedItem("dns_id").value,
      targets: [...box.querySelectorAll(".cert-target-row")].map((row) => ({ node_id: row.querySelector('[name="t_node"]').value, dir: row.querySelector('[name="t_dir"]').value.trim(), reload: row.querySelector('[name="t_reload"]').value.trim() })),
    };
    if (existing) await send(`/admin/certs/${existing.id}`, "PUT", body);
    else await send("/admin/certs", "POST", body);
    d.close();
    toast(existing ? "已保存" : "已提交，正在签发（通常一两分钟）");
    await render();
  });
}
