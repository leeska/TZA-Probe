// 后台「使用者」：分给朋友用的账号。每人有流量额度（可按月重置）、到期时间、开关和一个订阅 token；
// token 也是使用者自己查看用量的链接（/u/{token}）。代理入站与订阅输出在后续步骤里接上。
import { api, send, esc, toast, modal, guard } from "./api.js";
import { bytes } from "./format.js";
import { content, field, check, confirmAction, moreMenu, wireMoreMenus, render } from "./admin-state.js";

const STATUS = { active: ["可用", "good"], disabled: ["已停用", ""], expired: ["已过期", "bad"], exceeded: ["流量用完", "bad"] };
const dayOf = (iso) => (iso ? iso.slice(0, 10) : "");
const pageLink = (u) => `${location.origin}/u/${u.token}`;

function usageCell(u) {
  const limit = u.traffic_limit;
  const percent = limit ? Math.min(100, (u.traffic_used / limit) * 100) : 0;
  return `${bytes(u.traffic_used)}<small>${limit ? `/ ${bytes(limit)}` : "不限流量"}</small>${limit ? `<progress max="100" value="${percent.toFixed(1)}" aria-label="已用 ${percent.toFixed(0)}%"></progress>` : ""}`;
}
function resetCell(u) {
  if (!u.reset_day) return '<span class="muted">不重置</span>';
  return `每月 ${u.reset_day} 日<small>下次 ${new Date(u.next_reset * 1000).toLocaleDateString("zh-CN")}</small>`;
}

export async function usersPage(nodesReady) {
  const [, list] = await Promise.all([nodesReady, api("/admin/users")]);
  const rows = list.map((u) => {
    const [label, tone] = STATUS[u.status] || [u.status, ""];
    return `<tr data-user="${esc(u.id)}"><td data-label="使用者">${esc(u.name)}<small>${esc(u.note || "")}</small></td><td data-label="状态"><span class="pill ${tone}">${label}</span></td><td data-label="本期流量">${usageCell(u)}</td><td data-label="重置">${resetCell(u)}</td><td data-label="到期">${u.expires_at ? esc(dayOf(u.expires_at)) : '<span class="muted">长期</span>'}</td><td data-label="操作"><div class="row-actions"><button data-edit-user="${esc(u.id)}">编辑</button><button data-copy-user="${esc(u.id)}">复制用量页</button>${moreMenu(`<button data-reset-user="${esc(u.id)}">清零本期用量</button><button data-rotate-user="${esc(u.id)}">更换链接</button><button class="menu-danger" data-delete-user="${esc(u.id)}">删除</button>`)}</div></td></tr>`;
  }).join("");
  content().innerHTML = `<section data-part="page-heading" class="page-heading"><div><div class="eyebrow">USERS</div><h1>使用者<span>${list.length}</span></h1><p>分给朋友用的账号：流量额度、到期时间和各自的用量页。代理节点与订阅链接在后续版本接上。</p></div><div class="heading-actions"><button id="add-user" class="primary">＋ 添加使用者</button></div></section><section data-part="panel" class="panel"><div class="table-scroll"><table class="stack"><thead><tr><th>使用者</th><th>状态</th><th>本期流量</th><th>重置</th><th>到期</th><th>操作</th></tr></thead><tbody>${rows || '<tr><td colspan="6" class="empty-cell">还没有使用者。点「添加使用者」给朋友开一个。</td></tr>'}</tbody></table></div></section>`;
  const byId = (id) => list.find((u) => u.id === id);
  wireMoreMenus(content());
  content().querySelector("#add-user").onclick = () => userForm(null);
  content().querySelectorAll("[data-edit-user]").forEach((b) => (b.onclick = () => userForm(byId(b.dataset.editUser))));
  content().querySelectorAll("[data-copy-user]").forEach((b) => (b.onclick = () => copyLink(byId(b.dataset.copyUser))));
  content().querySelectorAll("[data-reset-user]").forEach((b) => (b.onclick = () => {
    const u = byId(b.dataset.resetUser);
    confirmAction(`清零本期用量 · ${u.name}`, `把「${u.name}」本期已用流量清零，并从现在开始新的周期。`, async () => { await send(`/admin/users/${u.id}/reset`, "POST"); await render(); }, { label: "清零", danger: false });
  }));
  content().querySelectorAll("[data-rotate-user]").forEach((b) => (b.onclick = () => {
    const u = byId(b.dataset.rotateUser);
    confirmAction(`更换链接 · ${u.name}`, `「${u.name}」现在的用量页和订阅链接会立即失效，需要把新链接发给对方。`, async () => {
      const next = await send(`/admin/users/${u.id}/token`, "POST");
      await render();
      copyLink(next);
    }, { label: "更换链接" });
  }));
  content().querySelectorAll("[data-delete-user]").forEach((b) => (b.onclick = () => {
    const u = byId(b.dataset.deleteUser);
    confirmAction(`删除使用者 · ${u.name}`, `删除「${u.name}」，对方的链接立即失效。此操作无法撤销。`, async () => { await send(`/admin/users/${u.id}`, "DELETE"); await render(); }, { label: "删除" });
  }));
}

function copyLink(u) {
  const link = pageLink(u);
  navigator.clipboard?.writeText(link).then(() => toast("已复制用量页链接"), () => {
    const d = modal(`用量页链接 · ${u.name}`, `<p class="hint">复制下面的链接发给对方。链接就是凭证，请不要公开。</p><div class="command-block"><pre>${esc(link)}</pre></div>`);
    d.querySelector("pre").focus?.();
  });
}

function userForm(existing) {
  const u = existing || { name: "", note: "", enabled: true, traffic_limit: 0, reset_day: 1, expires_at: "" };
  const d = modal(existing ? `编辑使用者 · ${existing.name}` : "添加使用者", `<form id="user-form"><section class="form-section"><h3>基本</h3><div class="form-grid">${field("名称", "name", u.name, "text", 'required maxlength="40"')}${field("备注（只有你能看到）", "note", u.note, "text", 'maxlength="200"')}</div>${check("启用", "enabled", u.enabled)}</section><section class="form-section"><h3>流量与期限</h3><div class="form-grid">${field("流量额度（GB，0 为不限）", "quota", u.traffic_limit / 1e9, "number", 'min="0" step="any"')}${field("每月重置日（1–28，0 为不重置）", "reset_day", u.reset_day, "number", 'min="0" max="28" step="1"')}${field("到期日（留空为长期）", "expires", dayOf(u.expires_at), "date")}</div><p class="hint">到期日当天 0 点（UTC）起停止使用；流量用完也会停止，直到下一个重置日或手动清零。</p></section><div class="form-footer"><button type="submit" class="primary">保存</button></div></form>`);
  const form = d.querySelector("#user-form");
  form.onsubmit = guard(async (e) => {
    e.preventDefault();
    const el = (n) => form.elements.namedItem(n);
    const body = {
      name: el("name").value.trim(),
      note: el("note").value.trim(),
      enabled: el("enabled").checked,
      traffic_limit: Math.round(Number(el("quota").value || 0) * 1e9),
      reset_day: Number(el("reset_day").value || 0),
      expires_at: el("expires").value ? `${el("expires").value}T00:00:00Z` : "",
    };
    const saved = existing ? await send(`/admin/users/${existing.id}`, "PUT", body) : await send("/admin/users", "POST", body);
    d.close();
    await render();
    if (!existing) {
      toast(`已添加「${saved.name}」`);
      copyLink(saved);
    } else toast("已保存");
  });
}
