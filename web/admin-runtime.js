import { api, send, esc, guard, modal, toast } from "./api.js";
import { state } from "./admin-state.js";

const labels = {
  requested: "等待节点生成计划", awaiting_approval: "等待管理员批准",
  approved: "已批准，等待节点执行", running: "节点正在执行",
  staged: "已暂存，未安装", installed: "已安装，未选择或启动",
  selected: "已选择，未启动或 adopt", cancelled: "已撤销",
  drained: "已排空并停止，派发仍暂停", started_paused: "启动检查通过，等待重新授权与恢复",
  failed: "计划不可用或已改变", expired: "批准已过期",
  unknown: "结果未知，需本机核对",
};
const errors = {
  plan_unavailable: "节点暂时无法生成有效计划，请核对信任密钥、版本、制品和本机配置。",
  plan_changed: "本机输入或制品发生变化，旧批准已失效。",
  operation_interrupted: "操作可能已产生本机变更。系统不会再次执行；先在节点核对 current、selection.json 和安装日志。",
  node_configuration_changed: "节点配置范围已改变，原批准已失效。执行中变更需要本机核对。",
};
const activeStates = ["requested", "awaiting_approval", "approved", "running"];
let authorizeExecutor;

function approvalAttributes(view, request) {
  return `data-runtime-seen="${view.seen_at}" data-runtime-planned="${request.updated_at}"`;
}

function lifecycleHint(action) {
  if (action === "drain") return "暂停本机准入并取消活动任务，等待全部记录 ACK 后停止服务。撤销或失败仍保留维护暂停。";
  if (action === "activate") return "启动已选择版本，检查身份、epoch、配置、隔离能力及 journal 连续性。失败时尝试停止该服务并保留维护暂停；通过后还需重新授权，再单独恢复。";
  return "暂存不安装；安装不选择或启动；选择不启动或 adopt。";
}

function planDetails(request) {
  const plan = request.review?.plan || request.operation?.plan || request.lifecycle?.plan.operation;
  if (!plan) return "";
  const lifecycle = request.lifecycle?.plan;
  const identity = lifecycle ? `<div><dt>升级前实例与 epoch</dt><dd>${esc(lifecycle.before.instance_id)} · ${esc(lifecycle.before.epoch)}</dd></div><div><dt>Socket 通信身份</dt><dd>UID ${esc(lifecycle.peer.uid)} / GID ${esc(lifecycle.peer.gid)} · ${esc(lifecycle.peer.socket_path)}</dd></div>` : "";
  return `<dl class="runtime-plan"><div><dt>计划 SHA-256</dt><dd>${esc(request.plan_sha256)}</dd></div><div><dt>发行归档 SHA-256</dt><dd>${esc(plan.archive_sha256)}</dd></div><div><dt>版本</dt><dd>${esc(plan.version)}</dd></div><div><dt>目标节点 UUID</dt><dd>${esc(plan.target.node_id)}</dd></div>${plan.artifacts ? `<div><dt>制品与权限</dt><dd>${plan.artifacts.length} 件 / ${(plan.total_bytes / 1048576).toFixed(1)} MiB；${esc(plan.permissions.join("、"))}</dd></div>` : `<div><dt>安装路径与服务</dt><dd>${esc(plan.options.install_root)}${plan.options.unit ? ` · ${esc(plan.options.unit)}` : ""}</dd></div>`}${identity}</dl><details><summary>完整计划：权限、兼容性、制品与本机路径</summary><pre class="runtime-json">${esc(JSON.stringify({enrollment: request.enrollment, plan: lifecycle || plan}, null, 2))}</pre></details>`;
}

function requestCard(request, index, viewIndex, view) {
  const step = {stage: "暂存", install: "安装", select: "选择版本", drain: "排空并停止", activate: "启动并验收"}[request.action] || request.action;
  const approval = request.state === "awaiting_approval" ? `<details><summary>核对批准计划</summary>${planDetails(request)}<p class="hint">${esc(lifecycleHint(request.action))}批准有效期为 15 分钟。</p><button class="primary" data-runtime-approve="${viewIndex}" data-request="${index}" ${approvalAttributes(view, request)}>批准这个精确计划</button></details>` : "";
  const cancel = ["requested", "awaiting_approval", "approved"].includes(request.state) ? `<button data-runtime-cancel="${viewIndex}" data-request="${index}">撤销未执行请求</button>` : "";
  const receipt = request.receipt || request.operation_receipt || request.lifecycle_receipt;
  return `<article class="panel runtime-panel"><div class="page-heading"><h3>${esc(step)} · ${esc(request.version)}</h3><span class="pill">${esc(labels[request.state] || request.state)}</span></div><p>请求 ${esc(request.id)} · ${esc(new Date(request.updated_at * 1000).toLocaleString())}</p>${request.stage_request_id ? `<p>关联暂存：${esc(request.stage_request_id)}</p>` : ""}${request.error_code ? `<p role="status">${esc(errors[request.error_code] || request.error_code)}</p>` : ""}${request.state === "running" ? "<p>运行中的操作不能远程撤销；断线后仍等待原操作回执。</p>" : ""}${approval}${request.state !== "awaiting_approval" && (request.review || request.operation || request.lifecycle) ? `<details><summary>查看已保存的批准计划</summary>${planDetails(request)}</details>` : ""}${receipt ? `<details><summary>查看已保存的节点回执</summary><pre class="runtime-json">${esc(JSON.stringify(receipt, null, 2))}</pre></details>` : ""}${cancel}</article>`;
}

function createForm(view, viewIndex) {
  const stages = view.requests.filter(request => request.action === "stage" && request.state === "staged" && request.enrollment.scope_sha256 === view.enrollment.scope_sha256);
  return `<details class="runtime-create"><summary>新建安装请求</summary><form data-runtime-create="${viewIndex}"><label>操作<select name="action"><option value="stage">仅暂存</option><option value="install" ${stages.length ? "" : "disabled"}>安装已暂存版本</option><option value="select" ${stages.length ? "" : "disabled"}>选择已安装版本</option></select></label><label data-runtime-version>精确版本<input name="version" required pattern="\\d+\\.\\d+\\.\\d+" placeholder="例如 0.4.0" autocomplete="off"></label><label data-runtime-source hidden>关联已完成暂存<select name="stage_request_id" disabled><option value="">选择一个暂存请求</option>${stages.map(request => `<option value="${esc(request.id)}">${esc(request.version)} · ${esc(request.plan_sha256.slice(0, 12))}</option>`).join("")}</select></label><p class="hint">节点独立生成计划，管理员核对后才执行。选择版本前需在本机排空任务并停止执行器服务。</p><div class="form-footer"><button class="primary" type="submit" data-runtime-seen="${view.seen_at}">生成节点计划</button></div></form></details>`;
}

export function refreshRuntimeValidity() {
  const now = Date.now() / 1000;
  for (const button of document.querySelectorAll("button[data-runtime-seen]")) {
    const seen = Number(button.dataset.runtimeSeen);
    const planned = Number(button.dataset.runtimePlanned || now);
    const expired = !seen || now - seen >= 45 || now - planned > 900;
    button.disabled = expired || button.dataset.runtimeBusy === "true";
    button.title = expired ? "节点上报或计划已过期，请刷新状态并重新核对。" : "";
  }
  for (const element of document.querySelectorAll("[data-runtime-freshness]")) {
    element.textContent = now - Number(element.dataset.runtimeFreshness) < 45 ? "通道最近上报有效" : "通道状态已过期";
  }
}

export async function renderRuntime(onAuthorize) {
  if (typeof onAuthorize === "function") authorizeExecutor = onAuthorize;
  const views = await api("/admin/runtime");
  const section = document.querySelector("[data-runtime-channels]");
  if (!section) return;
  section.innerHTML = `<div class="page-heading"><div><h2>Runtime 安装通道</h2><p>逐节点批准暂存、安装、排空、选版与启动验收。安装或启动回执不代表托管授权或恢复执行。</p></div><button id="runtime-refresh">刷新安装状态</button></div>${views.length ? views.map((view, viewIndex) => {
    const current = view.requests.find(request => activeStates.includes(request.state));
    const history = view.requests.filter(request => request !== current);
    const name = state.nodes.find(node => node.id === view.node_id)?.name || view.node_id;
    return `<section class="panel runtime-panel"><div class="page-heading"><h3>${esc(name)}</h3><span class="pill" data-runtime-freshness="${view.seen_at}"></span></div><p>${esc(view.enrollment.host.platform)} · Agent ${esc(view.enrollment.host.agent_version)} · Probe ${esc(view.enrollment.host.probe_version)}</p><details><summary>节点安装身份</summary><pre class="runtime-json">${esc(JSON.stringify(view.enrollment, null, 2))}</pre></details>${current ? requestCard(current, view.requests.indexOf(current), viewIndex, view) : createForm(view, viewIndex)}${history.length ? `<details><summary>历史安装请求（${history.length}）</summary>${history.map(request => requestCard(request, view.requests.indexOf(request), viewIndex, view)).join("")}</details>` : ""}</section>`;
  }).join("") : "<p>尚无节点启用 runtime 安装通道。先在指定节点配置专用安装服务；普通 Agent 与 IPSentinel Socket 接入不会启用安装权限。</p>"}`;
  views.forEach((view, viewIndex) => {
    const panels = [...section.children].filter(element => element.matches("section.runtime-panel"));
    const panel = panels[viewIndex];
    if (!panel) return;
    if (view.maintenance) {
      const hint = document.createElement("p");
      hint.setAttribute("role", "status");
      hint.textContent = "升级维护暂停中：新任务、恢复和配置应用已阻止。完成启动检查后，核对实例、epoch 与运行声明重新授权，再单独恢复；重绑会停用旧日程。";
      panel.prepend(hint);
    }
    const form = panel.querySelector("form[data-runtime-create]");
    if (form) {
      for (const [value, text] of [["drain", "排空任务并停止服务"], ["activate", "启动已选择版本并验收"]]) {
        const option = document.createElement("option");
        option.value = value;
        option.textContent = text;
        option.disabled = !view.requests.some(request => request.action === "stage" && request.state === "staged" && request.enrollment.scope_sha256 === view.enrollment.scope_sha256) || (value === "activate" && !view.maintenance);
        form.elements.action.append(option);
      }
      form.querySelector("p.hint").textContent = "升级步骤：暂停并排空 → 选择已安装版本 → 启动验收 → 核对新身份重新授权 → 单独恢复。各步分别批准。暂停取消活动任务，等待全部记录 ACK；失败保持维护暂停。首次 adopt 仍需本机操作。";
    }
    const startup = view.requests.find(request => request.state === "started_paused" && request.lifecycle_receipt);
    if (view.maintenance && startup && authorizeExecutor) {
      const button = document.createElement("button");
      button.textContent = "按启动回执核对重新授权";
      button.dataset.runtimeSeen = String(view.seen_at);
      button.dataset.runtimePlanned = String(Math.min(startup.updated_at, view.seen_at));
      button.onclick = () => {
        const hello = startup.lifecycle_receipt.hello;
        authorizeExecutor({node_id: view.node_id, binding: {
          target: startup.enrollment.target, mode: "managed",
          executor_instance_id: hello.status.executor_instance_id, epoch: hello.status.control.epoch,
          descriptor_sha256: hello.descriptor_sha256,
        }});
      };
      panel.prepend(button);
    }
  });
  section.querySelector("#runtime-refresh").onclick = guard(renderRuntime);
  section.querySelectorAll("[data-runtime-approve]").forEach(button => {
    button.onclick = () => {
      const view = views[Number(button.dataset.runtimeApprove)];
      const request = view.requests[Number(button.dataset.request)];
      const dialog = modal("批准节点运行时计划", `<p>目标：${esc(state.nodes.find(node => node.id === view.node_id)?.name || view.node_id)} · ${esc(request.action)} ${esc(request.version)}</p>${planDetails(request)}<p>${esc(lifecycleHint(request.action))}本机输入变化后须重新批准。</p><div class="form-footer"><button class="primary" data-confirm-runtime ${approvalAttributes(view, request)}>确认批准</button></div>`);
      refreshRuntimeValidity();
      const confirm = dialog.querySelector("[data-confirm-runtime]");
      confirm.onclick = guard(async () => {
        confirm.dataset.runtimeBusy = "true";
        refreshRuntimeValidity();
        try {
          await send(`/admin/nodes/${encodeURIComponent(view.node_id)}/runtime/requests/${encodeURIComponent(request.id)}`, "PUT", {approved: true, plan_sha256: request.plan_sha256});
          dialog.close();
          toast("该节点的精确计划已批准");
          await renderRuntime();
        } finally {
          confirm.dataset.runtimeBusy = "false";
          refreshRuntimeValidity();
        }
      });
    };
  });
  section.querySelectorAll("[data-runtime-cancel]").forEach(button => {
    button.onclick = guard(async () => {
      const view = views[Number(button.dataset.runtimeCancel)];
      const request = view.requests[Number(button.dataset.request)];
      button.disabled = true;
      try {
        await send(`/admin/nodes/${encodeURIComponent(view.node_id)}/runtime/requests/${encodeURIComponent(request.id)}`, "DELETE");
        toast("未执行请求已撤销");
        await renderRuntime();
      } finally { button.disabled = false; }
    });
  });
  section.querySelectorAll("[data-runtime-create]").forEach(form => {
    const actionField = form.elements.action;
    const versionField = form.elements.version;
    const sourceField = form.elements.stage_request_id;
    actionField.onchange = () => {
      const stage = actionField.value === "stage";
      form.querySelector("[data-runtime-version]").hidden = !stage;
      form.querySelector("[data-runtime-source]").hidden = stage;
      versionField.disabled = !stage;
      sourceField.disabled = stage;
      sourceField.required = !stage;
    };
    form.onsubmit = guard(async event => {
      event.preventDefault();
      const view = views[Number(form.dataset.runtimeCreate)];
      const stage = view.requests.find(request => request.id === sourceField.value);
      if (actionField.value !== "stage" && !stage) throw new Error("请选择该节点已完成的暂存请求");
      const button = form.querySelector("[type=submit]");
      button.dataset.runtimeBusy = "true";
      refreshRuntimeValidity();
      try {
        await send(`/admin/nodes/${encodeURIComponent(view.node_id)}/runtime/requests`, "POST", {
          action: actionField.value, version: actionField.value === "stage" ? versionField.value.trim() : stage.version,
          stage_request_id: actionField.value === "stage" ? "" : stage.id,
        });
        toast("等待节点生成计划，当前尚未批准执行");
        await renderRuntime();
      } finally {
        button.dataset.runtimeBusy = "false";
        refreshRuntimeValidity();
      }
    });
  });
  refreshRuntimeValidity();
}
