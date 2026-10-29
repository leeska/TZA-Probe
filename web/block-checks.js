import { api, esc, modal, guard, send } from "./api.js";
import { date } from "./format.js";

const labels = { reachable: "大陆可达", suspected: "疑似被墙", partial: "部分线路不可达", unreachable: "各地均不可达", unknown: "证据不足", paused: "已暂停" };
const states = { reachable: "可达", failed: "连续两轮失败", unconfirmed: "失败，等待复测", pending: "等待检测", stale: "结果已过期", offline: "节点离线或已删除", unavailable: "无有效检测结果" };

export function blockTaskEditor(existing, nodes, onSaved) {
  const t = existing || { enabled: true };
  const b = t.block || { host: "", port: 443, family: "ipv4", interval: 300, mainland_node_ids: [], overseas_node_ids: [] };
  const choices = (role, ids) => nodes.map((n) => `<label class="checkbox"><input type="checkbox" name="${role}" value="${esc(n.id)}" ${ids.includes(n.id) ? "checked" : ""}>${esc(n.name)}<small>${esc(n.region || "未标地区")}${n.online ? "" : " · 离线"}</small></label>`).join("") || '<p class="hint">请先在节点管理中接入 Agent。</p>';
  const d = modal(t.id ? "编辑 IP 被墙检测" : "创建 IP 被墙检测", `<form id="block-task-form">
    <section class="form-section"><h3>检测目标</h3><div class="form-grid">
      <label>任务名称<input name="name" maxlength="100" value="${esc(t.name || "")}" placeholder="留空自动命名"></label>
      <label>公网 IP<input name="host" required value="${esc(b.host)}" placeholder="填写目标 IPv4 或 IPv6，不填域名" autocomplete="off"></label>
      <label>TCP 端口<input name="port" type="number" min="1" max="65535" required value="${b.port}"></label>
      <label>IP 协议<select name="family"><option value="ipv4"${b.family === "ipv4" ? " selected" : ""}>IPv4</option><option value="ipv6"${b.family === "ipv6" ? " selected" : ""}>IPv6</option></select></label>
      <label>检测间隔（秒）<input name="interval" type="number" min="60" max="3600" required value="${b.interval}"></label>
    </div><p class="hint">选择目标正在监听且允许检测节点访问的端口。检测的是该 IP 与端口的 TCP 可达性。</p></section>
    <section class="form-section"><h3>中国大陆检测节点</h3><div class="node-checkboxes">${choices("mainland", b.mainland_node_ids)}</div><p class="hint">至少一个；建议选择不同地区和运营商，节点须使用中国大陆直连出口。</p></section>
    <section class="form-section"><h3>境外对照节点</h3><div class="node-checkboxes">${choices("overseas", b.overseas_node_ids)}</div><p class="hint">至少一个；使用境外直连出口，不能与大陆节点重复，合计最多 20 个节点。旧 Agent 可直接执行。</p></section>
    <p class="notice">大陆连续两轮连接失败、境外同期可达时显示「疑似被墙」。路由故障、防火墙和地区封禁也可能产生相同现象；离线节点与缺失结果不算连接失败。</p>
    <label class="checkbox"><input type="checkbox" name="enabled" ${t.enabled ? "checked" : ""}>启用任务</label>
    <div class="form-footer"><button type="submit" class="primary">保存任务</button></div></form>`);
  const form = d.querySelector("form");
  form.onchange = (event) => {
    const el = event.target;
    if (!["mainland", "overseas"].includes(el.name) || !el.checked) return;
    for (const other of form.querySelectorAll('input[type="checkbox"][value]')) {
      if (other.name !== el.name && other.value === el.value) other.checked = false;
    }
  };
  form.onsubmit = guard(async (event) => {
    event.preventDefault();
    const data = new FormData(form);
    const mainland = data.getAll("mainland"), overseas = data.getAll("overseas");
    if (!mainland.length || !overseas.length) throw new Error("请分别选择至少一个大陆和境外节点");
    const host = data.get("host").trim();
    const task = { name: data.get("name").trim() || `IP 被墙检测 ${host}`, type: "block", enabled: data.has("enabled"), node_ids: [...mainland, ...overseas], block: { host, port: Number(data.get("port")), family: data.get("family"), interval: Number(data.get("interval")), mainland_node_ids: mainland, overseas_node_ids: overseas } };
    const button = form.querySelector('button[type="submit"]');
    button.disabled = true;
    try {
      await send("/admin/tasks" + (t.id ? "/" + t.id : ""), t.id ? "PUT" : "POST", task);
      d.close();
      await onSaved();
    } finally { button.disabled = false; }
  });
}

export function blockChecksHTML(checks) {
  if (!checks.length) return "";
  return `<section class="panel"><div class="form-section-head"><h2>IP 被墙检测</h2><button id="refresh-block-checks">刷新检测结果</button></div><p class="hint">使用所选大陆与境外 Agent 对照检测。首次判定失败至少需要两轮；下方任务列表可立即执行、暂停或修改。</p><div class="table-scroll"><table class="stack"><thead><tr><th>任务 / 目标</th><th>检测结论</th><th>明细</th></tr></thead><tbody>${checks.map((v) => `<tr><td data-label="任务 / 目标">${esc(v.name)}<small>${esc(v.host)} · TCP ${v.port}</small></td><td data-label="检测结论"><span class="pill ${v.status === "reachable" ? "good" : ["suspected", "partial", "unreachable"].includes(v.status) ? "warn" : ""}">${esc(labels[v.status] || "证据不足")}</span><small>${esc(v.detail)}</small></td><td data-label="明细"><button data-block-detail="${esc(v.task_id)}">节点检测明细</button></td></tr>`).join("")}</tbody></table></div></section>`;
}

export function wireBlockChecks(root, checks, refresh) {
  const button = root.querySelector("#refresh-block-checks");
  if (button) button.onclick = guard(refresh);
  root.querySelectorAll("[data-block-detail]").forEach((button) => button.onclick = () => {
    const v = checks.find((v) => v.task_id === button.dataset.blockDetail);
    modal(`IP 被墙检测 · ${v.name}`, `<p class="notice">${esc(v.detail)}</p><div class="table-scroll"><table class="stack"><thead><tr><th>节点 / 来源</th><th>状态</th><th>最近两轮</th></tr></thead><tbody>${v.evidence.map((e) => `<tr><td data-label="节点 / 来源">${esc(e.node_name)}<small>${e.role === "mainland" ? "中国大陆" : "境外对照"}</small></td><td data-label="状态">${esc(states[e.state] || "未知")}</td><td data-label="最近两轮">${e.attempts.map((a) => `<small>${esc(date(a.issued))} · ${a.finished ? `${a.result.received || 0}/${a.result.sent || 0} 次连接成功 · ${esc(a.result.reason || a.result.status)}` : "尚未上报结果"}</small>`).join("") || "暂无有效记录"}</td></tr>`).join("")}</tbody></table></div>`);
  });
}

export const loadBlockChecks = () => api("/admin/block-checks");
