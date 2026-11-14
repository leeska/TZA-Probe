// 「代理」页下方的订阅设置：规则模板（Clash 写法，同时翻译成 sing-box 路由）与外部订阅
// （主控定时拉取别处的订阅，节点并入使用者的订阅）。
import { send, esc, toast, modal, guard } from "./api.js";
import { field, check, confirmAction, render } from "./admin-state.js";

const ago = (unix) => {
  if (!unix) return "还没拉取";
  const m = Math.round((Date.now() / 1000 - unix) / 60);
  return m < 1 ? "刚刚" : m < 60 ? `${m} 分钟前` : m < 1440 ? `${Math.round(m / 60)} 小时前` : `${Math.round(m / 1440)} 天前`;
};
const hostOf = (u) => { try { return new URL(u).host; } catch { return u; } };

export function subscriptionPanels(data, people) {
  const templates = data.templates || [];
  const sources = data.sources || [];
  const tplRows = templates.map((t) => `<tr><td data-label="模板">${esc(t.name)}${t.id === data.default_template ? ' <span class="pill good">默认</span>' : ""}<small>${t.built_in ? "内置" : `${t.rules.split("\n").filter((l) => l.trim() && !l.trim().startsWith("#")).length} 条规则`} · 订阅地址加 <code>?template=${esc(t.id)}</code></small></td><td data-label="操作"><div class="row-actions">${t.id === data.default_template ? "" : `<button data-default-template="${esc(t.id)}">设为默认</button>`}<button data-view-template="${esc(t.id)}">${t.built_in ? "查看" : "编辑"}</button>${t.built_in ? `<button data-copy-template="${esc(t.id)}">复制一份</button>` : `<button class="danger-text" data-delete-template="${esc(t.id)}">删除</button>`}</div></td></tr>`).join("");
  const userName = (id) => people.find((u) => u.id === id)?.name || "已删除";
  const srcRows = sources.map((s) => `<tr><td data-label="订阅">${esc(s.name)}${s.enabled ? "" : ' <span class="pill">已停用</span>'}<small>${esc(hostOf(s.url))}${s.prefix ? ` · 前缀「${esc(s.prefix)}」` : ""}</small></td><td data-label="节点">${s.node_count} 个${s.skipped ? `<small>跳过 ${s.skipped} 个不支持的</small>` : ""}${s.types.length ? `<small>${s.types.map(esc).join("、")}</small>` : ""}</td><td data-label="拉取">${ago(s.fetched_at)}${s.error ? `<small class="warn-text">${esc(s.error)}</small>` : ""}</td><td data-label="使用者">${s.user_ids.length ? s.user_ids.map((id) => esc(userName(id))).join("、") : "全部"}</td><td data-label="操作"><div class="row-actions"><button data-refresh-source="${esc(s.id)}">立即拉取</button><button data-edit-source="${esc(s.id)}">编辑</button><button class="danger-text" data-delete-source="${esc(s.id)}">删除</button></div></td></tr>`).join("");
  return `<section data-part="panel" class="panel"><div class="panel-head"><h3>规则模板</h3><button id="add-template">＋ 新建模板</button></div><p class="hint panel-note">订阅里的分流规则。默认模板用于所有订阅，订阅地址加 <code>?template=模板 ID</code> 可以临时换。</p><div class="table-scroll"><table class="stack"><thead><tr><th>模板</th><th>操作</th></tr></thead><tbody>${tplRows}</tbody></table></div></section>
  <section data-part="panel" class="panel"><div class="panel-head"><h3>外部订阅</h3><button id="add-source">＋ 添加</button></div><p class="hint panel-note">把别处的订阅（Clash YAML 或分享链接）并入使用者的订阅。主控每 6 小时拉取一次，支持 SS、VMess、VLESS（含 Reality）、Trojan、Hysteria2，其他协议跳过。</p><div class="table-scroll"><table class="stack"><thead><tr><th>订阅</th><th>节点</th><th>拉取</th><th>使用者</th><th>操作</th></tr></thead><tbody>${srcRows || '<tr><td colspan="5" class="empty-cell">还没有外部订阅。</td></tr>'}</tbody></table></div></section>`;
}

export function wireSubscriptionPanels(root, data, people) {
  const templates = data.templates || [];
  const sources = data.sources || [];
  const q = (sel, fn) => root.querySelectorAll(sel).forEach((b) => (b.onclick = () => fn(b)));
  root.querySelector("#add-template").onclick = () => templateForm(null);
  root.querySelector("#add-source").onclick = () => sourceForm(null, people);
  q("[data-default-template]", (b) => guard(async () => { await send("/admin/proxy/settings", "PUT", { default_template: b.dataset.defaultTemplate }); toast("已设为默认"); await render(); })());
  q("[data-view-template]", (b) => templateForm(templates.find((t) => t.id === b.dataset.viewTemplate)));
  q("[data-copy-template]", (b) => { const t = templates.find((x) => x.id === b.dataset.copyTemplate); templateForm({ name: `${t.name}（副本）`, rules: t.rules }); });
  q("[data-delete-template]", (b) => { const t = templates.find((x) => x.id === b.dataset.deleteTemplate); confirmAction(`删除模板 · ${t.name}`, `删除「${t.name}」。它是默认模板的话，默认改回内置的「国内直连」。`, async () => { await send(`/admin/proxy/templates/${t.id}`, "DELETE"); await render(); }, { label: "删除" }); });
  q("[data-refresh-source]", (b) => guard(async () => { toast("正在拉取…"); const s = await send(`/admin/proxy/sources/${b.dataset.refreshSource}/refresh`, "POST"); toast(s.error ? `拉取失败：${s.error}` : `拉到 ${s.node_count} 个节点`); await render(); })());
  q("[data-edit-source]", (b) => sourceForm(sources.find((s) => s.id === b.dataset.editSource), people));
  q("[data-delete-source]", (b) => { const s = sources.find((x) => x.id === b.dataset.deleteSource); confirmAction(`删除外部订阅 · ${s.name}`, `删除「${s.name}」，它的节点会从所有订阅里去掉。`, async () => { await send(`/admin/proxy/sources/${s.id}`, "DELETE"); await render(); }, { label: "删除" }); });
}

const RULE_HELP = `每行一条，写法同 Clash：<code>类型,值,出口</code>。类型：DOMAIN、DOMAIN-SUFFIX、DOMAIN-KEYWORD、IP-CIDR、GEOSITE、GEOIP（private 表示私有地址），最后一行 <code>MATCH,出口</code>（不写就是 MATCH,代理）。出口：代理、DIRECT、REJECT。IP 规则可加 <code>,no-resolve</code>。# 开头是注释。sing-box 订阅会自动翻译成对应的路由规则和 SagerNet 的规则集。`;

function templateForm(t) {
  const readOnly = t?.built_in;
  const d = modal(readOnly ? `内置模板 · ${t.name}` : t?.id ? `编辑模板 · ${t.name}` : "新建规则模板", `<form id="template-form">${field("名称", "name", t?.name || "", "text", `required maxlength="40" ${readOnly ? "readonly" : ""}`)}<label>规则<textarea name="rules" rows="16" spellcheck="false" class="code-input" ${readOnly ? "readonly" : "required"}>${esc(t?.rules || "GEOSITE,private,DIRECT\nGEOIP,private,DIRECT,no-resolve\nDOMAIN-SUFFIX,openai.com,代理\nGEOSITE,cn,DIRECT\nGEOIP,CN,DIRECT\nMATCH,代理")}</textarea></label><p class="hint">${RULE_HELP}</p>${readOnly ? '<p class="hint">内置模板不能修改，可以「复制一份」再改。</p>' : '<div class="form-footer"><button type="submit" class="primary">保存</button></div>'}</form>`);
  if (readOnly) return;
  const form = d.querySelector("#template-form");
  form.onsubmit = guard(async (e) => {
    e.preventDefault();
    const body = { name: form.elements.namedItem("name").value.trim(), rules: form.elements.namedItem("rules").value };
    if (t?.id) await send(`/admin/proxy/templates/${t.id}`, "PUT", body);
    else await send("/admin/proxy/templates", "POST", body);
    d.close();
    toast("已保存");
    await render();
  });
}

function sourceForm(s, people) {
  const boxes = people.map((u) => `<label class="checkbox"><input type="checkbox" name="user" value="${esc(u.id)}" ${s?.user_ids.includes(u.id) ? "checked" : ""}>${esc(u.name)}</label>`).join("") || '<p class="hint">还没有使用者。</p>';
  const d = modal(s ? `编辑外部订阅 · ${s.name}` : "添加外部订阅", `<form id="source-form"><div class="form-grid">${field("名称", "name", s?.name || "", "text", 'required maxlength="40" placeholder="如 某某机场"')}${field("节点名前缀（可空）", "prefix", s?.prefix || "", "text", 'maxlength="20" placeholder="如 机场A"')}</div><label>订阅地址<input name="url" type="url" required maxlength="2048" value="${esc(s?.url || "")}" placeholder="https://…" autocapitalize="off" spellcheck="false"></label>${check("启用", "enabled", s ? s.enabled : true)}<section class="form-section"><h3>给哪些使用者</h3><p class="hint">都不勾就是全部使用者。</p><div class="node-checkboxes">${boxes}</div></section><p class="hint">订阅地址就是凭证，只保存在主控上。保存后立即拉取一次。</p><div class="form-footer"><button type="submit" class="primary">保存</button></div></form>`);
  const form = d.querySelector("#source-form");
  form.onsubmit = guard(async (e) => {
    e.preventDefault();
    const el = (n) => form.elements.namedItem(n);
    const body = { name: el("name").value.trim(), prefix: el("prefix").value.trim(), url: el("url").value.trim(), enabled: el("enabled").checked, user_ids: [...form.querySelectorAll('input[name="user"]:checked')].map((x) => x.value) };
    const saved = s ? await send(`/admin/proxy/sources/${s.id}`, "PUT", body) : await send("/admin/proxy/sources", "POST", body);
    d.close();
    toast(saved.error ? `已保存，但拉取失败：${saved.error}` : `已保存，${saved.node_count} 个节点`);
    await render();
  });
}
