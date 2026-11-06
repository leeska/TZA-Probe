import { api, send, esc, guard, toast } from "./api.js";
import { state, confirmAction } from "./admin-state.js";
import { showSchedule } from "./admin-executor-schedules.js";
import { showRecords } from "./admin-executors.js";

const serviceNames = { google: "Google", youtube: "YouTube", meta: "Meta" };
const serviceName = service => serviceNames[service] || service;
const phases = { baseline: "检测送中状态", experiment: "尝试拉回", followup: "干净复测" };

export function guardEvidence(assessment, view, now = Date.now()) {
  if (!Number.isFinite(view.received_at) || !view.received_at || now < view.received_at * 1000 - 5000 || now - view.received_at * 1000 >= 45000) return "状态已过期，请刷新";
  if (!assessment || !Number.isFinite(Date.parse(assessment.valid_until)) || Date.parse(assessment.valid_until) <= now) return "暂无有效证据";
  const redirect = assessment.signal_kind === "google_home_redirect";
  return {
    anomaly_observed: redirect ? "检测到送中跳转" : "检测到地区异常",
    target_observed: "当前符合目标表现，尚无异常基线",
    change_pending: "表现有变化，待独立复测",
    change_confirmed: redirect ? "送中跳转消失，连续复测通过" : "连续干净复测通过",
    other_region: "检测到其他地区",
    not_applicable: "此信号不支持恢复判读",
  }[assessment.state] || "地区判读未知";
}

export function guardRunReason(view, service, phase, now = Date.now()) {
  if (!Number.isFinite(view?.received_at) || !view.received_at || view.received_at * 1000 > now + 5000 || now - view.received_at * 1000 >= 45000) return "节点状态已过期，请刷新后再试";
  const hello = view.hello;
  const status = hello?.status;
  if (view.binding?.mode !== "managed" || status?.control?.mode !== "managed") return "当前只读，请管理员完成 Agent 的托管授权";
  if (status.control.paused || status.control.draining) return "节点已暂停或正在维护，不会启动新任务";
  if (status.control.active_run_id) return "节点已有任务执行中，请完成后再试";
  if (status.health !== "ready" || !hello.features?.includes("run")) return "节点尚未就绪，暂不能执行";
  if (!status.config || !view.config || !service?.enabled) return "服务尚未启用，请管理员完成 Agent 的服务配置";
  const purpose = phase === "experiment" ? "experiment" : "observe";
  const profileID = purpose === "experiment" ? service.experiment?.profile_id : service.observation_profile_id;
  const profile = hello.profiles?.find(item => item.profile_id === profileID && item.purpose === purpose && item.adapter_id === service.adapter_id);
  if (!profile || (purpose === "experiment" && !profile.recovery_capable)) return purpose === "experiment" ? "此服务没有已批准且支持拉回尝试的方案" : "节点未提供该服务的检测方案";
  const budget = status.budget;
  if (!budget || budget.day_utc !== new Date(now).toISOString().slice(0, 10) || !Number.isFinite(budget.used_runs) || !Number.isFinite(budget.limits?.runs) || !Number.isFinite(budget.accounted_network_bytes) || !Number.isFinite(budget.limits?.network_bytes) || !Number.isFinite(view.config.limits?.network_bytes)) return "今日额度状态未知，请等待节点更新";
  if (budget.used_runs >= budget.limits.runs || budget.limits.network_bytes - budget.accounted_network_bytes < view.config.limits.network_bytes) return "今日节点额度不足，不会创建新任务";
  return "";
}

export async function renderIPGuard(root, onSetup) {
  root.innerHTML = '<p role="status">正在读取节点与地区状态…</p>';
  const [nodes, views] = await Promise.all([api("/admin/nodes"), api("/admin/executors")]);
  if (!root.isConnected) return;
  state.nodes = nodes;
  let selectedNode = views[0]?.node_id || "";
  let selectedService = "";
  let submitting = false;
  let timer;
  const stop = () => clearInterval(timer);
  const render = () => {
    const view = views.find(item => item.node_id === selectedNode);
    const services = view?.config?.services?.filter(service => service.enabled) || [];
    if (!services.some(service => service.service_id === selectedService)) selectedService = services[0]?.service_id || "";
    const service = services.find(item => item.service_id === selectedService);
    const assessment = view?.hello?.status?.assessments?.find(item => item.service_id === selectedService);
    const budget = view?.hello?.status?.budget;
    root.innerHTML = `<section class="panel ip-guard-workspace"><div class="page-heading"><h2>防护汇总</h2><button data-guard-refresh>刷新状态</button></div>${views.length ? `<div class="ip-guard-picker"><label>Agent<select data-guard-node>${views.map(item => `<option value="${esc(item.node_id)}" ${item.node_id === selectedNode ? "selected" : ""}>${esc(nodes.find(node => node.id === item.node_id)?.name || item.node_id)}</option>`).join("")}</select></label>${services.length ? `<label>服务<select data-guard-service>${services.map(item => `<option value="${esc(item.service_id)}" ${item.service_id === selectedService ? "selected" : ""}>${esc(serviceName(item.service_id))}</option>`).join("")}</select></label>` : ""}</div><div class="ip-guard-reading" aria-live="polite"><span>最近地区判读${service?.target_country ? ` · 目标 ${esc(service.target_country)}` : ""}</span><strong data-guard-evidence></strong><p>各服务独立判断；页面能打开，不代表地区已恢复。</p></div><div class="ip-guard-actions">${Object.entries(phases).map(([phase, label]) => `<button ${phase === "baseline" ? 'class="primary"' : ""} data-guard-run="${phase}">${label}</button>`).join("")}</div><p class="hint" data-guard-reason role="status"></p><p class="hint" data-guard-budget></p><div class="heading-actions"><button data-guard-schedule>自动检测与拉回排期</button><button data-guard-history>查看记录</button><button class="quiet" data-guard-setup>管理 Agent</button></div>` : '<div class="market-empty"><h3>先添加一个防护节点</h3><p>在 Agent 页添加组件并完成启用，即可检测地区、尝试拉回和安排复测。主控仅汇总管理，不运行浏览器。</p><button class="primary" data-guard-setup>添加 Agent 组件</button></div>'}</section>`;
    root.querySelector("[data-guard-refresh]").onclick = guard(() => { stop(); return renderIPGuard(root, onSetup); });
    root.querySelector("[data-guard-setup]").onclick = guard(onSetup);
    if (!view) return;
    const update = () => {
      if (!root.isConnected) { stop(); return; }
      root.querySelector("[data-guard-evidence]").textContent = guardEvidence(assessment, view);
      const reasons = [];
      root.querySelectorAll("[data-guard-run]").forEach(button => {
        const reason = guardRunReason(view, service, button.dataset.guardRun);
        button.disabled = submitting || Boolean(reason);
        button.title = reason;
        if (reason && !reasons.includes(reason)) reasons.push(reason);
      });
      root.querySelector("[data-guard-reason]").textContent = reasons.join("；") || "先检测现状；拉回尝试完成后，请单独做干净复测。";
      const today = new Date().toISOString().slice(0, 10);
      root.querySelector("[data-guard-budget]").textContent = budget?.day_utc === today && Number.isFinite(budget.used_runs) && Number.isFinite(budget.limits?.runs) ? `今日已用 ${budget.used_runs} / ${budget.limits.runs} 次（UTC，节点所有服务共享）` : "今日额度未知，等待节点上报。";
    };
    root.querySelector("[data-guard-node]").onchange = event => { selectedNode = event.target.value; render(); };
    const serviceField = root.querySelector("[data-guard-service]");
    if (serviceField) serviceField.onchange = event => { selectedService = event.target.value; render(); };
    root.querySelector("[data-guard-schedule]").onclick = guard(() => showSchedule(view.node_id));
    root.querySelector("[data-guard-history]").onclick = guard(() => showRecords(view.node_id));
    root.querySelectorAll("[data-guard-run]").forEach(button => {
      button.onclick = () => {
        const phase = button.dataset.guardRun;
        const reason = guardRunReason(view, service, phase);
        if (reason) { toast(reason); return; }
        const detail = phase === "experiment" ? "按节点已经批准的地区、浏览方案和持久会话执行一次尝试。不会修改出口 IP，也不能保证改变服务商的地区判定；完成后需单独做干净复测。" : "使用独立的干净会话检测，不使用拉回会话的 Cookie 或定位。单次结果不等于恢复确认。";
        const nodeName = nodes.find(node => node.id === view.node_id)?.name || view.node_id;
        confirmAction(`${phases[phase]} · ${nodeName} / ${serviceName(service.service_id)}`, `${detail} 将消耗该节点的执行与网络额度。`, async () => {
          if (submitting) return;
          submitting = true;
          update();
          try {
            const current = (await api("/admin/executors")).find(item => item.node_id === view.node_id);
            const currentService = current?.config?.services?.find(item => item.service_id === service.service_id);
            const latestReason = guardRunReason(current, currentService, phase);
            if (latestReason) throw new Error(latestReason);
            if (JSON.stringify(current.binding) !== JSON.stringify(view.binding) || JSON.stringify(current.hello.status.config) !== JSON.stringify(view.hello.status.config) || JSON.stringify(current.config) !== JSON.stringify(view.config)) throw new Error("节点身份或配置已变化，请刷新后重新确认");
            const now = new Date();
            await send(`/admin/nodes/${encodeURIComponent(view.node_id)}/executors/ipsentinel/actions`, "POST", {
              kind: "run.submit", job_id: crypto.randomUUID(), service_id: service.service_id,
              purpose: phase === "experiment" ? "experiment" : "observe", phase,
              not_before: now.toISOString(), expires_at: new Date(now.getTime() + 300000).toISOString(),
              timeout_ms: current.config.limits.timeout_ms, config_revision: current.hello.status.config.revision, config_sha256: current.hello.status.config.sha256,
            });
            toast("请求已保存，等待节点受理；请在记录中查看结果");
            stop();
            if (root.isConnected) await renderIPGuard(root, onSetup);
          } finally { submitting = false; }
        }, { danger: false, label: "确认执行一次" });
      };
    });
    update();
    stop();
    timer = setInterval(update, 1000);
  };
  render();
}
