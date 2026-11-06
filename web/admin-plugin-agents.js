import { api, send, esc, guard, modal, toast } from "./api.js";
import { nodePluginEligibility } from "./admin-node-plugins.js";

const activeStates = new Set(["requested", "awaiting_approval", "approved", "running"]);
const stateNames = { requested: "准备中", awaiting_approval: "等待确认", approved: "等待 Agent", running: "处理中", staged: "资源已下载", installed: "已安装，待启用", selected: "待启用", started_paused: "已启动，待授权", drained: "维护暂停", failed: "未完成", expired: "确认已过期", unknown: "结果待核对", cancelled: "已取消" };

export function pluginAgentState(channel, binding, version) {
  const requests = (channel?.requests || []).filter(request => request.enrollment?.scope_sha256 === channel.enrollment.scope_sha256);
  const current = requests.find(request => activeStates.has(request.state));
  const installed = requests.find(request => request.state === "installed" && request.version === version);
  const stage = requests.find(request => request.state === "staged" && request.version === version);
  if (current) return { label: stateNames[current.state], current };
  if (binding) {
    const fresh = Number.isFinite(binding.received_at) && binding.received_at > 0 && Math.abs(Date.now() / 1000 - binding.received_at) < 45;
    return { label: fresh ? "已接入" : "已接入，状态待刷新", hint: "检测与拉回状态见功能页；安装记录不代表功能已启用。" };
  }
  if (installed || requests.some(request => ["selected", "started_paused"].includes(request.state))) return { label: "已安装，待启用", hint: "组件已安装；首次启用仍需管理员在 Agent 本机完成接入配置，暂不支持自动启用。" };
  const reason = nodePluginEligibility(channel);
  if (reason) return { label: "暂不可安装", hint: reason };
  if (channel.maintenance) return { label: "维护暂停", hint: "需要管理员完成本机维护并核对服务状态；不会自动恢复。" };
  if (!version) return { label: "版本不可用", hint: "请先更新或重新安装主控管理部分。" };
  if (requests.some(request => request.state === "unknown")) return { label: "结果待核对", hint: "之前操作的结果未知，请管理员先在 Agent 本机核对，避免重复安装。" };
  return stage ? { label: "等待安装", action: "install", stage } : { label: "未安装", action: "stage" };
}

function planSummary(request) {
  const plan = request.review?.plan || request.operation?.plan || request.lifecycle?.plan.operation;
  if (!plan) return '<p role="alert">计划不完整，请刷新或取消请求。</p>';
  return `<p>组件版本：v${esc(request.version)}</p>${plan.artifacts ? `<p>下载量：${(plan.total_bytes / 1048576).toFixed(1)} MiB · ${plan.artifacts.length} 件</p><p>权限：${esc((plan.permissions || []).join("、"))}</p>` : "<p>安装之前已核验并下载的组件；不会自动启动或启用任务。</p>"}<details><summary>技术详情</summary><pre class="runtime-json">${esc(JSON.stringify({ enrollment: request.enrollment, plan, plan_sha256: request.plan_sha256 }, null, 2))}</pre></details>`;
}

export async function renderPluginAgents(pkg, root) {
  const [nodes, channels, bindings] = await Promise.all([api("/admin/nodes"), api("/admin/runtime"), api("/admin/executors")]);
  if (!root.isConnected) return;
  const version = pkg.installed?.version;
  const refresh = () => renderPluginAgents(pkg, root);
  const byNode = new Map(channels.map(channel => [channel.node_id, channel]));
  const byBinding = new Map(bindings.map(binding => [binding.node_id, binding]));
  const rows = nodes.map(node => {
    const state = { node, channel: byNode.get(node.id), ...pluginAgentState(byNode.get(node.id), byBinding.get(node.id), version) };
    if (node.runtime_install_disabled) {
      state.label = "主控执行已关闭";
      state.hint = "已停止下发插件操作；已开始的操作会继续完成并上报结果";
      state.action = undefined;
      state.stage = undefined;
      state.canReview = false;
    } else {
      state.canReview = state.current?.state === "awaiting_approval";
    }
    return state;
  });
  root.innerHTML = `<section class="panel theme-panel">
    <div class="panel-head"><div><h2>Agent 组件</h2><p>选择需要防护的 Agent，按需下载、安装。主控不运行检测。</p></div>
      <div class="form-actions"><button data-agents-allow-all ${nodes.length ? "" : "disabled"}>全部允许</button><button data-agents-refresh>刷新</button></div>
    </div><div class="theme-list">${rows.map((row, index) => `<div class="theme-row">
      <span class="theme-copy"><b>${esc(row.node.name || row.node.id)}</b><small>${esc(row.hint || row.channel?.enrollment.host.platform || "")}</small></span>
      <span class="pill">${esc(row.label)}</span>
      ${row.action ? `<button data-agent-action="${index}">${row.action === "stage" ? "安装组件" : "继续安装"}</button>` : ""}
      ${row.canReview ? `<button class="primary" data-agent-review="${index}">查看并确认</button>` : ""}
      ${row.current && ["requested", "awaiting_approval", "approved"].includes(row.current.state) ? `<button class="quiet" data-agent-cancel="${index}">取消</button>` : ""}
      <button class="quiet" data-agent-toggle="${index}">${row.node.runtime_install_disabled ? "允许主控执行" : "不允许主控执行"}</button>
    </div>`).join("") || '<p>还没有 Agent，请先添加节点。</p>'}</div></section>
    <p class="hint">默认允许主控安装和运行插件，可单独关闭。允许后仅在安装插件时下载运行依赖。关闭会停止下发新操作，已开始的操作继续完成；重新允许后待处理操作可继续。</p>
    <details class="panel"><summary>安装记录与诊断</summary>${rows.filter(row => row.channel?.requests?.length).map(row => `<h3>${esc(row.node.name || row.node.id)}</h3>${row.channel.requests.map(request => `<p>v${esc(request.version)} · ${esc(stateNames[request.state] || request.state)}${request.error_code ? ` · ${esc(request.error_code)}` : ""}</p>`).join("")}`).join("") || "<p>暂无安装记录。</p>"}</details>`;
  root.querySelector("[data-agents-refresh]").onclick = guard(refresh);
  root.querySelector("[data-agents-allow-all]").onclick = guard(async () => {
    const button = root.querySelector("[data-agents-allow-all]");
    button.disabled = true;
    try {
      await send("/admin/nodes/batch", "POST", { ids: nodes.map(node => node.id), patch: { runtime_install_disabled: false } });
      toast("已允许全部 Agent 通过主控使用插件");
      await refresh();
    } finally { button.disabled = false; }
  });
  const channelFresh = async row => {
    const fresh = (await api("/admin/runtime")).find(channel => channel.node_id === row.node.id);
    if (!fresh || fresh.enrollment.scope_sha256 !== row.channel?.enrollment.scope_sha256 || !Number.isFinite(fresh.seen_at) || Math.abs(Date.now() / 1000 - fresh.seen_at) >= 45) throw new Error("Agent 状态或安装许可已变化，请刷新后重试");
    return fresh;
  };
  root.querySelectorAll("[data-agent-action]").forEach(button => {
    button.onclick = guard(async () => {
      button.disabled = true;
      try {
        const row = rows[Number(button.dataset.agentAction)];
        const fresh = await channelFresh(row);
        const next = pluginAgentState(fresh, byBinding.get(row.node.id), version);
        if (next.action !== row.action || next.stage?.id !== row.stage?.id) throw new Error("安装状态已变化，请刷新后重试");
        if (row.action === "stage") await send("/admin/market/node-plans", "POST", { id: pkg.id, version, node_id: row.node.id, scope_sha256: fresh.enrollment.scope_sha256 });
        else await send(`/admin/nodes/${encodeURIComponent(row.node.id)}/runtime/requests`, "POST", { action: "install", version, stage_request_id: row.stage.id });
        toast("等待 Agent 准备计划；确认后才执行");
        await refresh();
      } finally { button.disabled = false; }
    });
  });
  root.querySelectorAll("[data-agent-review]").forEach(button => {
    button.onclick = () => {
      const row = rows[Number(button.dataset.agentReview)];
      const request = row.current;
      const supported = ["stage", "install"].includes(request.action);
      const dialog = modal("确认 Agent 组件操作", `<p>${esc(row.node.name || row.node.id)} · ${esc(request.action === "stage" ? "下载组件" : request.action === "install" ? "安装组件" : "维护操作")}</p>${planSummary(request)}<p>此操作不会自动启用检测或拉回任务。</p>${supported ? '<div class="form-footer"><button class="primary" data-agent-confirm>确认</button></div>' : '<p>维护操作需管理员在本机核对，本页不提供自动批准。</p>'}`);
      const confirm = dialog.querySelector("[data-agent-confirm]");
      if (confirm) confirm.onclick = guard(async () => {
        confirm.disabled = true;
        try {
          const fresh = await channelFresh(row);
          const planned = fresh.requests.find(item => item.id === request.id);
          if (!planned || planned.state !== "awaiting_approval" || planned.plan_sha256 !== request.plan_sha256 || Date.now() / 1000 - planned.updated_at > 900 || !request.plan_sha256) throw new Error("计划已变化或过期，请刷新后重新核对");
          await send(`/admin/nodes/${encodeURIComponent(row.node.id)}/runtime/requests/${encodeURIComponent(request.id)}`, "PUT", { approved: true, plan_sha256: request.plan_sha256 });
          dialog.close();
          await refresh();
        } finally { confirm.disabled = false; }
      });
    };
  });
  root.querySelectorAll("[data-agent-cancel]").forEach(button => {
    button.onclick = guard(async () => {
      const row = rows[Number(button.dataset.agentCancel)];
      button.disabled = true;
      try {
        await send(`/admin/nodes/${encodeURIComponent(row.node.id)}/runtime/requests/${encodeURIComponent(row.current.id)}`, "DELETE");
        await refresh();
      } finally { button.disabled = false; }
    });
  });
  root.querySelectorAll("[data-agent-toggle]").forEach(button => {
    button.onclick = guard(async () => {
      const row = rows[Number(button.dataset.agentToggle)];
      button.disabled = true;
      try {
        await send(`/admin/nodes/${encodeURIComponent(row.node.id)}`, "PATCH", { runtime_install_disabled: !row.node.runtime_install_disabled });
        await refresh();
      } finally { button.disabled = false; }
    });
  });
}
