import { api, send, esc, guard, modal, toast } from "./api.js";
import { content, state, field, loadNodes, confirmAction } from "./admin-state.js";
import { showSchedule } from "./admin-executor-schedules.js";

const states = {
  unknown: "证据未知",
  anomaly_observed: "观察到异常表现",
  target_observed: "观察到目标表现（无异常基线）",
  change_pending: "表现变化，等待独立复测",
  change_confirmed: "连续干净复测通过",
  other_region: "观察到其他地区",
  not_applicable: "不适用恢复判读",
};

function evidenceText(assessment, receivedAt, now = Date.now()) {
  if (!receivedAt || now - receivedAt * 1000 >= 45000) return "同步已过期，当前证据未知";
  if (assessment.state !== "not_applicable" && (!assessment.valid_until || Date.parse(assessment.valid_until) <= now)) return "证据已过期或缺失，等待有效观测";
  if (assessment.state === "change_confirmed" && assessment.signal_kind === "google_home_redirect") return "首页跳转消失，连续干净复测通过";
  return states[assessment.state] || "证据未知";
}

function refreshValidity() {
  for (const element of document.querySelectorAll("[data-executor-evidence]")) {
    const assessment = { state: element.dataset.state, valid_until: element.dataset.until, signal_kind: element.dataset.signal };
    element.textContent = evidenceText(assessment, Number(element.dataset.received));
  }
  for (const element of document.querySelectorAll("[data-executor-freshness]")) {
    const received = Number(element.dataset.received);
    element.textContent = received && Date.now() - received * 1000 < 45000 ? "最近同步有效" : "等待同步 / 同步过期";
  }
  for (const element of document.querySelectorAll("[data-executor-write]")) {
    element.disabled = !isFresh({ received_at: Number(element.dataset.received) });
  }
}

function isFresh(view) { return view.received_at && Date.now() - view.received_at * 1000 < 45000; }

setInterval(refreshValidity, 5000);

function bindForm(view) {
  const nodes = state.nodes;
  if (!nodes.length) { toast("请先添加节点"); return; }
  const binding = view?.binding || {};
  const dialog = modal("授权节点执行器", `<form id="executor-binding"><p>先在指定 VPS 完成本机接入和身份核对，再选择授权范围。安装、模式交接和创建任务分别进行。</p><label>目标节点<select name="node_id" required>${nodes.map(node => `<option value="${esc(node.id)}" ${node.id === view?.node_id ? "selected" : ""}>${esc(node.name)}</option>`).join("")}</select></label><label>授权范围<select name="mode"><option value="managed_readonly" ${binding.mode !== "managed" ? "selected" : ""}>只读同步状态与历史</option><option value="managed" ${binding.mode === "managed" ? "selected" : ""}>托管配置与单任务执行</option></select></label>${field("本机控制器 UUID", "controller_id", binding.target?.controller_id || "")}${field("本机节点 UUID", "protocol_node_id", binding.target?.node_id || "")}${field("执行器实例 UUID", "executor_instance_id", binding.executor_instance_id || "")}${field("运行声明 SHA-256", "descriptor_sha256", binding.descriptor_sha256 || "")}${field("控制代次 epoch", "epoch", binding.epoch || 1, "number", 'min="1" step="1" required')}<p class="hint">托管授权要求本机已停止旧排期并完成 managed adopt。此表单不会安装浏览器，也不会自动创建任务。身份或 epoch 变化后必须重新核对。</p><div class="form-footer"><button class="primary" type="submit">保存授权范围</button></div></form>`);
  dialog.querySelector("form").onsubmit = guard(async event => {
    event.preventDefault();
    const data = new FormData(event.target);
    const button = event.target.querySelector("[type=submit]");
    button.disabled = true;
    try {
      await send(`/admin/nodes/${encodeURIComponent(data.get("node_id"))}/executors/ipsentinel`, "PUT", {
        target: { controller_id: data.get("controller_id").trim(), node_id: data.get("protocol_node_id").trim() },
        executor_instance_id: data.get("executor_instance_id").trim(),
        descriptor_sha256: data.get("descriptor_sha256").trim(), epoch: Number(data.get("epoch")), mode: data.get("mode"),
      });
      dialog.close();
      toast("授权已保存，等待 Agent 同步有效状态");
      await renderExecutors();
    } finally { button.disabled = false; }
  });
}

async function action(nodeID,kind,payload={}) {
  const receipt=await send(`/admin/nodes/${encodeURIComponent(nodeID)}/executors/ipsentinel/actions`,"POST",{kind,...payload});
  toast(`命令已持久保存（${receipt.id}），等待节点确认`);
  await renderExecutors();
}

function configForm(view) {
  const initial = view.config ? JSON.stringify(view.config, null, 2) : "";
  const dialog = modal("验证托管配置", `<form><p>填写本机批准的配置。验证不会应用配置；取得本机通过回执后，再核对预算并单独确认应用。</p><p class="hint">可用 Profile：${(view.hello?.profiles || []).map(profile => esc(`${profile.profile_id}（${profile.purpose}）`)).join("、") || "尚未上报"}</p><label>配置 JSON<textarea name="config" rows="16" required spellcheck="false">${esc(initial)}</textarea></label><div class="form-footer"><button class="primary">提交本机验证</button></div></form>`);
  dialog.querySelector("form").onsubmit = guard(async event => {
    event.preventDefault();
    const button = event.target.querySelector('button.primary');
    button.disabled = true;
    try {
      const config = JSON.parse(new FormData(event.target).get("config"));
      await action(view.node_id, "config.validate", { config });
      dialog.close();
      await showActions(view);
    } finally { button.disabled = false; }
  });
}

function managedControls(view) {
  const nodeID=view.node_id;
  const snapshot=view.hello?.status;
  if(view.binding.mode!=="managed" || snapshot?.control.mode!=="managed")return;
  const paused=snapshot.control.paused;
  const pause=document.createElement("button");
  pause.textContent=paused?"恢复托管执行":"暂停托管执行";
  pause.onclick=()=>confirmAction(pause.textContent,paused?"只按当前暂停代次恢复；本机能力和预检仍需通过。":"暂停会阻止新任务，并要求本机取消活动 worker。已存历史不会删除。",()=>action(nodeID,paused?"control.resume":"control.pause",paused?{expected_pause_generation:snapshot.control.pause_generation}:{}));
  return pause;
}

function createJobForm(view) {
  const snapshot=view.hello?.status;
  const config=snapshot?.config;
  const services = view.config?.services.filter(service => service.enabled) || [];
  if (!services.length || !config) { toast("先验证并应用本机配置"); return; }
  const dialog=modal("创建单次托管任务",`<form><p>只执行当前修订中的一项任务。观测与实验分别保存，实验不参与恢复确认。</p><label>服务<select name="service_id">${services.map(service => `<option value="${esc(service.service_id)}">${esc(service.service_id)}</option>`).join("")}</select></label><label>用途与阶段<select name="phase"><option value="baseline">干净基线观测</option><option value="followup">干净独立复测</option><option value="experiment">本机已批准的实验</option></select></label>${field("超时（毫秒）","timeout_ms",view.config.limits.timeout_ms,"number",`min="1000" max="${view.config.limits.timeout_ms}" required`)}<p class="hint">配置修订 ${config.revision}；五分钟后过期。单次网络预留 ${view.config.limits.network_bytes} 字节，每日最多 ${view.config.daily_budget.runs} 次。取消和失败不退还执行器额度。</p><div class="form-footer"><button class="primary">创建单次任务</button></div></form>`);
  dialog.querySelector("form").onsubmit=guard(async event=>{
    event.preventDefault();const form=new FormData(event.target);const now=new Date();
    const service = services.find(service => service.service_id === form.get("service_id"));
    if (form.get("phase") === "experiment" && !service.experiment) throw new Error("该服务没有批准实验用途");
    const button=event.target.querySelector("button.primary");button.disabled=true;
    try {
      await action(view.node_id,"run.submit",{job_id:crypto.randomUUID(),service_id:service.service_id,purpose:form.get("phase")==="experiment"?"experiment":"observe",phase:form.get("phase"),not_before:now.toISOString(),expires_at:new Date(now.getTime()+5*60000).toISOString(),timeout_ms:Number(form.get("timeout_ms")),config_revision:config.revision,config_sha256:config.sha256});
      dialog.close();
    } finally { button.disabled=false; }
  });
}

async function showActions(view) {
  const actions=await api(`/admin/nodes/${encodeURIComponent(view.node_id)}/executors/ipsentinel/actions`);
  const labels={pending:"等待派发／重试",leased:"等待节点回执",completed:"命令已确认",accepted:"任务已受理",rejected:"本机拒绝",cancelled:"派发已取消",expired:"窗口或配置已失效"};
  const dialog=modal("托管命令与任务",`<p>命令确认与浏览任务完成分别显示。${actions.length ? "点击刷新获取节点回执。" : "尚无托管命令。"}</p><button data-refresh-actions>刷新回执</button>${actions.map((item,index)=>`<article class="panel"><p>${esc(item.kind)} · ${esc(labels[item.status] || item.status)} · 已派发 ${item.attempt || 0} 次</p>${item.job_id?`<p>逻辑任务：${esc(item.job_id)}</p>`:""}${item.run?`<p>执行状态：${esc(item.run.state)}${item.run.result?` · ${esc(item.run.result.reason || "已形成终态记录")}`:""}</p>`:""}${item.result?.error_code?`<p>拒绝原因：${esc(item.result.error_code)}</p>`:""}${item.result?.validation?`<p>本机配置验证：${item.result.validation.valid?"通过":"未通过"}</p>${item.result.validation.issues.map(issue=>`<p>${esc(issue.pointer)}：${esc(issue.message)}</p>`).join("")}`:""}${item.kind==="config.validate"&&item.status==="completed"&&item.result?.validation?.valid&&item.config?`<button data-apply="${index}">核对预算并应用配置</button>`:""}${item.kind==="run.submit"&&["pending","leased","accepted"].includes(item.status)&&!item.run?.result?`<button data-cancel="${esc(item.job_id)}">取消任务</button>`:""}</article>`).join("")}`);
  dialog.querySelector("[data-refresh-actions]").onclick=guard(()=>showActions(view));
  dialog.querySelectorAll("[data-cancel]").forEach(button=>button.onclick=guard(async()=>{await action(view.node_id,"run.cancel",{job_id:button.dataset.cancel});dialog.close();}));
  dialog.querySelectorAll("[data-apply]").forEach(button=>button.onclick=()=> {
    const config=actions[Number(button.dataset.apply)].config;
    confirmAction("应用本机已验证配置",`将批准 ${config.services.filter(service=>service.enabled).map(service=>service.service_id).join("、")}；单次超时 ${config.limits.timeout_ms} 毫秒、内存 ${config.limits.memory_bytes} 字节；每日 ${config.daily_budget.runs} 次、${config.daily_budget.network_bytes} 字节。配置变化时旧环境证据失效。`,async()=>{
      const latest=(await api("/admin/executors")).find(item=>item.node_id===view.node_id);
      await action(view.node_id,"config.apply",{config,expected_revision:latest.hello.status.config?.revision||0});
    });
  });
}

async function showRecords(nodeID) {
  const dialog = modal("执行器历史记录", '<p aria-live="polite">正在读取持久记录…</p>');
  let after = 0;
  const load = async () => {
    const records = await api(`/admin/nodes/${encodeURIComponent(nodeID)}/executors/ipsentinel/records?after_seq=${after}`);
    if (!dialog.open) return;
    dialog.querySelector("p")?.remove();
    dialog.querySelector("[data-more]")?.remove();
    if (!records.length) {
      dialog.insertAdjacentHTML("beforeend", '<p>没有更多记录。</p>');
      return;
    }
    for (const record of records) {
      const result = record.kind === "run.terminal" ? record.data : null;
      dialog.insertAdjacentHTML("beforeend", `<article class="panel"><p>#${esc(record.seq)} · ${esc(record.kind)} · ${esc(record.recorded_at)}</p>${result ? `<p>${esc(result.service_id)} / ${result.purpose === "experiment" ? "实验" : "观测"} · 执行状态：${esc(result.execution)}</p><p>${result.provenance === "legacy_import" ? "旧历史导入，不参与恢复确认" : "原生执行记录"}</p>${result.pages.map(page => `<p>${esc(page.adapter_id)} · 页面 ${esc(page.outcome)} · ${page.signals.map(signal => esc(signal.status === "observed" ? signal.value : "未知")).join(" / ")}</p>`).join("")}` : '<p>控制或判读记录已持久保存。</p>'}</article>`);
    }
    after = records.at(-1).seq;
    if (records.length === 50) {
      dialog.insertAdjacentHTML("beforeend", '<button data-more>加载更多</button>');
      dialog.querySelector("[data-more]").onclick = guard(load);
    }
  };
  await load();
}

export async function renderExecutors(nodesPromise = loadNodes()) {
  const [views] = await Promise.all([api("/admin/executors"), nodesPromise]);
  content().innerHTML = `<section class="page-heading" data-part="page-heading"><div><div class="eyebrow">IPSentinel / EXECUTOR</div><h1>节点执行器</h1><p>同步证据，并在明确批准后执行单任务托管操作。</p></div><div class="heading-actions"><button id="executor-refresh">刷新状态</button><button class="primary" id="executor-bind">授权节点接入</button></div></section><div class="executor-list"><section class="panel"><p>只读绑定保留 VPS 本机调度；只有本机切换到 managed、能力和健康检查通过时才开放托管操作。所有命令异步确认，不启动本地浏览器。</p></section>${views.length ? views.map((view, index) => {
    const node = state.nodes.find(node => node.id === view.node_id);
    const snapshot = view.hello?.status;
    return `<section class="panel"><div class="page-heading"><h2>${esc(node?.name || view.node_id)}</h2><span class="pill">${node?.online ? "节点在线" : "节点离线 / 未上报"}</span></div><p data-executor-freshness data-received="${view.received_at}"></p><p>控制面最后健康状态：${esc({ready:"就绪",degraded:"降级",unavailable:"不可用"}[snapshot?.health] || "等待上报")} · 执行能力：${snapshot?.control.mode==="managed"&&view.hello.features.includes("run")?"已上报，等待准入":"未开放"}</p><p>本机模式：${esc(snapshot?.control.mode || "等待上报")} · ${snapshot?.control.paused ? `已暂停（${esc(snapshot.control.pause_reason)}）` : snapshot ? "未暂停" : "暂停状态未知"}</p><p>已持久保存至记录 #${view.through_seq} · 最后同步：${view.received_at ? esc(new Date(view.received_at * 1000).toLocaleString()) : "尚未收到"}</p>${snapshot?.assessments.length ? snapshot.assessments.map(assessment => `<p><strong>${esc(assessment.service_id)}</strong>：<span data-executor-evidence data-state="${esc(assessment.state)}" data-signal="${esc(assessment.signal_kind)}" data-until="${esc(assessment.valid_until || "")}" data-received="${view.received_at}">${esc(evidenceText(assessment, view.received_at))}</span></p>`).join("") : "<p>服务证据未知，等待有效观测。</p>"}<details><summary>安装身份</summary><p>实例：${esc(view.binding.executor_instance_id)}</p><p>epoch：${view.binding.epoch} · 暂停代次：${snapshot?.control.pause_generation ?? "未知"}</p><p>配置修订：${snapshot?.config?.revision ?? "未应用"} · 预算：${snapshot?.budget?`${snapshot.budget.used_runs}/${snapshot.budget.limits.runs} 次`:"未知"}</p></details><div class="heading-actions"><button data-records="${index}">查看历史</button><button data-bind="${index}">核对并重新授权</button></div><div class="heading-actions" data-managed="${index}"></div></section>`;
  }).join("") : '<section class="panel"><h2>还没有授权执行器</h2><p>先在指定 VPS 完成本机只读接入，再授权同步。已有的网络探测继续正常运行。</p></section>'}</div>`;
  content().querySelector("#executor-refresh").onclick = guard(() => renderExecutors());
  content().querySelector("#executor-bind").onclick = () => bindForm();
  content().querySelectorAll("[data-bind]").forEach(button => {
    const view = views[Number(button.dataset.bind)];
    button.onclick = () => bindForm(view);
    const revoke = document.createElement("button");
    revoke.textContent = "撤销接入授权";
    button.after(revoke);
    revoke.onclick = () => confirmAction("撤销执行器接入授权", "撤销后停止接收数据并取消待派发命令。已受理任务仍由本机管理；如需停止，先暂停并确认执行器已清理。只读模式的本机排期继续保留。", async () => {
      await send(`/admin/nodes/${encodeURIComponent(view.node_id)}/executors/ipsentinel`, "DELETE");
      await renderExecutors();
    });
  });
  content().querySelectorAll("[data-records]").forEach(button => { button.onclick = guard(() => showRecords(views[Number(button.dataset.records)].node_id)); });
  content().querySelectorAll("[data-managed]").forEach(container=>{
    const view=views[Number(container.dataset.managed)];const control=managedControls(view);if(control)container.append(control);
    const schedule=document.createElement("button");schedule.textContent="托管日程";schedule.onclick=guard(()=>showSchedule(view.node_id));container.append(schedule);
    if(view.binding.mode!=="managed"||view.hello?.status?.control.mode!=="managed")return;
    if(view.config&&view.hello.features.includes("run")&&!view.hello.status.control.paused&&view.hello.status.health==="ready"){
      const create=document.createElement("button");create.textContent="创建单次任务";create.onclick=()=>createJobForm(view);container.append(create);
    }
    if(view.hello.features.includes("config")){
      const config=document.createElement("button");config.textContent="验证托管配置";config.onclick=()=>configForm(view);container.append(config);
    }
    if(view.hello?.status?.control.mode==="managed"){
      const history=document.createElement("button");history.textContent="托管命令";history.onclick=guard(()=>showActions(view));container.append(history);
    }
    container.querySelectorAll("button").forEach(button=>{if(!["托管命令","托管日程"].includes(button.textContent)){button.dataset.executorWrite="";button.dataset.received=String(view.received_at);}});
  });
  refreshValidity();
}
