// 地区偏移检测任务（网络探测里的一种任务）：和解锁检测一样可以一次选多台节点。每台节点不同的
// 目标地区、出口地址族与定位在「节点管理 → 地区偏移设置」里设；这里只管检测哪些服务、是否启用、
// 是否自动校正、包含哪些节点。接口：/admin/ipguard/tasks。
import { api, send, esc, guard, modal, toast } from "./api.js";
import { serviceReading } from "./admin-ip-guard.js";

const SERVICES = [{ id: "google", name: "Google" }, { id: "meta", name: "Meta" }];
const serviceName = (id) => SERVICES.find((s) => s.id === id)?.name || id;
const countryName = (code) => {
  try { return new Intl.DisplayNames(["zh-CN"], { type: "region" }).of(code); } catch { return code; }
};

// 一台节点在任务表单里的说明：它的节点设置，或还缺什么。
function nodeNote(region, id) {
  const s = region.node_settings?.[id];
  if (!s) return "未设置目标地区：在节点管理里设置";
  return `目标 ${countryName(s.country)} · ${s.family === "ipv6" ? "IPv6" : "IPv4"}${s.position ? " · 已定位" : ""}`;
}

export function regionTaskEditor(existing, region, nodes, onSaved) {
  const t = existing || { name: "地区偏移检测", enabled: true, auto_recover: false, services: ["google", "meta"], node_ids: [] };
  const owner = new Map();
  for (const task of region.tasks || []) if (task.id !== t.id) for (const id of task.node_ids) owner.set(id, task.name);
  const nodeBoxes = nodes.map((n) => {
    const taken = owner.get(n.id);
    return `<label class="checkbox"><input type="checkbox" name="node" value="${esc(n.id)}" ${t.node_ids.includes(n.id) ? "checked" : ""} ${taken ? "disabled" : ""}>${esc(n.name)}<small>${taken ? `已在「${esc(taken)}」` : esc(nodeNote(region, n.id))}${n.online ? "" : " · 离线"}</small></label>`;
  }).join("") || '<p class="hint">请先在节点管理中接入 Agent。</p>';
  const d = modal(existing?.id ? "编辑地区偏移检测" : "创建地区偏移检测", `<form id="region-task-form"><section class="form-section"><h3>基本</h3><div class="form-grid"><label>任务名称<input name="name" maxlength="40" required value="${esc(t.name)}"></label></div><label class="checkbox"><input type="checkbox" name="enabled" ${t.enabled ? "checked" : ""}>启用（每 12 小时检测一次）</label><label class="checkbox"><input type="checkbox" name="auto_recover" ${t.auto_recover ? "checked" : ""}>偏移时尝试自动校正（尝试一次，并独立复测确认）</label></section><section class="form-section"><h3>检测服务</h3><div class="node-checkboxes">${SERVICES.map((s) => `<label class="checkbox"><input type="checkbox" name="service" value="${s.id}" ${t.services.includes(s.id) ? "checked" : ""}>${s.name}</label>`).join("")}</div></section><section class="form-section"><h3>节点</h3><p class="hint">每台节点的目标地区、出口地址族与定位在「节点管理 → 地区偏移设置」里设置；没设置的节点先加进来也可以，设置好后自动开始检测。一台节点只能属于一个地区偏移检测任务。</p><div class="node-checkboxes">${nodeBoxes}</div></section><div class="form-footer"><button type="submit" class="primary">保存</button></div></form>`);
  const form = d.querySelector("#region-task-form");
  form.onsubmit = guard(async (e) => {
    e.preventDefault();
    const body = {
      name: form.elements.name.value.trim(),
      enabled: form.elements.enabled.checked,
      auto_recover: form.elements.auto_recover.checked,
      services: [...form.querySelectorAll('[name="service"]:checked')].map((b) => b.value),
      node_ids: [...form.querySelectorAll('[name="node"]:checked')].map((b) => b.value),
    };
    if (!body.services.length) { toast("至少选择一个服务"); return; }
    if (existing?.id) await send(`/admin/ipguard/tasks/${encodeURIComponent(existing.id)}`, "PUT", body);
    else await send("/admin/ipguard/tasks", "POST", body);
    d.close();
    toast("已保存地区偏移检测任务");
    await onSaved?.();
  });
}

// 任务的整体状态：各节点各服务里最需要关注的那一个。
const ORDER = { bad: 3, "": 1, good: 0 };
export function regionTaskReading(task, region) {
  let worst = null, waiting = 0;
  for (const id of task.node_ids) {
    const node = region.nodes.find((n) => n.id === id);
    if (!node) continue;
    const reason = region.pending?.[id];
    if (reason) waiting++;
    for (const service of task.services) {
      const r = reason ? { label: "未生效", tone: "", note: reason } : serviceReading(node, service, region.jobs);
      if (!worst || (ORDER[r.tone] ?? 1) > (ORDER[worst.tone] ?? 1)) worst = { ...r, node: node.name };
    }
  }
  return { worst, waiting };
}

// 检测结果：每台节点、每个服务一格，可以单独检测一次。
export async function regionTaskResults(task, onChanged) {
  const region = await api("/admin/ipguard");
  const fresh = region.tasks.find((t) => t.id === task.id) || task;
  const rows = fresh.node_ids.map((id) => {
    const node = region.nodes.find((n) => n.id === id);
    if (!node) return "";
    const cells = fresh.services.map((service) => {
      const r = serviceReading(node, service, region.jobs);
      return `<td data-label="${esc(serviceName(service))}"><span class="pill ${r.tone}">${esc(r.label)}</span><small>${esc(r.note || "")}</small></td>`;
    }).join("");
    const waiting = region.pending?.[id];
    return `<tr><td data-label="节点">${esc(node.name)}<small>${esc(waiting || nodeNote(region, id))}</small></td>${cells}<td data-label="操作"><div class="row-actions">${fresh.services.map((service) => `<button data-check-node="${esc(id)}" data-service="${service}" ${node.policy?.enabled ? "" : "disabled"}>检测 ${esc(serviceName(service))}</button>`).join("")}</div></td></tr>`;
  }).join("");
  const d = modal(`检测结果 · ${fresh.name}`, `<div class="table-scroll"><table class="stack"><thead><tr><th>节点</th>${fresh.services.map((s) => `<th>${esc(serviceName(s))}</th>`).join("")}<th>操作</th></tr></thead><tbody>${rows || `<tr><td colspan="${fresh.services.length + 2}" class="empty-cell">任务里还没有节点</td></tr>`}</tbody></table></div><p class="hint">每 12 小时检查一次。正常只表示未发现页面地区异常，不代表服务商的 IP 归属或信誉已改变。</p>`);
  d.classList.add("wide-dialog");
  const closed = () => { d.removeEventListener("close", closed); d.classList.remove("wide-dialog"); onChanged?.(); };
  d.addEventListener("close", closed);
  d.querySelectorAll("[data-check-node]").forEach((b) => (b.onclick = guard(async () => {
    await send("/admin/ipguard/jobs", "POST", { node_id: b.dataset.checkNode, service: b.dataset.service });
    toast("已排队，完成后自动更新");
    b.disabled = true;
  })));
}
