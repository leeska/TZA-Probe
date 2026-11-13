import { controlTask, bindTasks, taskRuns } from "./task-controls.js";
import { taskEditor, taskSummary } from "./task-editor.js";
import { api, send, esc, toast, guard, modal } from "./api.js";
import { regionTaskEditor, regionTaskReading, regionTaskResults } from "./region-tasks.js";
import { blockTaskEditor, blockChecksHTML, wireBlockChecks, loadBlockChecks } from "./block-checks.js";
import { date } from "./format.js";
import { state, content, taskName, confirmAction, moreMenu, wireMoreMenus, render } from "./admin-state.js";
export async function tasksPage(nodesReady) {
  // 执行汇总读不到不该挡住任务列表：那一列退回「尚未执行」。
  // 地区偏移检测任务在它自己的模块里（/admin/ipguard），读不到也不挡任务列表：那几行不画。
  const [, list, health, blocks, region] = await Promise.all([nodesReady, api("/admin/tasks"), api("/admin/tasks/health").catch(() => ({})), loadBlockChecks(), api("/admin/ipguard").catch(() => null)]);
  state.tasks = list;
  regionData = region;
  renderTasks(health, blocks, region);
}

let regionData = null;
// 统一的「创建任务」：先选类型。
const TASK_TYPES = [
  { id: "probe", title: "延迟与回程", note: "TCP 延迟、丢包与三网回程路由，按目标目录或自定义地址" },
  { id: "unlock", title: "解锁检测", note: "流媒体与 AI 服务的可用性，按内置目录逐项检测" },
  { id: "block", title: "IP 被墙检测", note: "大陆节点与境外节点同时连接同一 IP 端口，对比判断" },
  { id: "region", title: "地区偏移检测", note: "Google、Meta 识别的地区是否偏离各节点的目标地区" },
];
function chooseTaskType() {
  const d = modal("创建任务", `<div class="task-types">${TASK_TYPES.map((t) => `<button type="button" class="task-type" data-type="${t.id}"><strong>${t.title}</strong><small>${t.note}</small></button>`).join("")}</div>`);
  d.querySelectorAll("[data-type]").forEach((b) => (b.onclick = guard(async () => {
    const type = b.dataset.type;
    if (type === "region") {
      if (!regionData) { toast("地区偏移检测暂时读不到，请稍后再试"); return; }
      return regionTaskEditor(null, regionData, state.nodes, render);
    }
    if (type === "block") return blockTaskEditor(null, state.nodes, render);
    await taskForm(type === "unlock" ? { type: "unlock", enabled: true, node_ids: [] } : null);
  })));
}

// 地区偏移检测任务的一行：节点数里标出还没生效的几台，状态取各节点各服务里最需要关注的那一个。
function regionRows(region) {
  if (!region) return "";
  const label = { google: "Google", meta: "Meta" };
  return (region.tasks || []).map((t) => {
    const nodes = t.node_ids.filter((id) => region.nodes.some((n) => n.id === id));
    const countries = [...new Set(nodes.map((id) => region.node_settings?.[id]?.country).filter(Boolean))];
    const { worst, waiting } = regionTaskReading({ ...t, node_ids: nodes }, region);
    const last = !nodes.length ? '<span class="muted">未绑定节点</span>' : worst ? `<span class="pill ${worst.tone}">${esc(worst.label)}</span><small>${esc(worst.node)}${worst.note ? " · " + esc(worst.note) : ""}</small>` : "—";
    return `<tr data-region-task="${esc(t.id)}"><td data-label="选择"></td><td data-label="任务">${esc(t.name)}<small>地区偏移检测 · 每 12 小时${t.auto_recover ? " · 自动校正" : ""}</small></td><td data-label="状态"><span class="pill ${t.enabled ? "good" : ""}">${t.enabled ? "已启用" : "已停用"}</span></td><td data-label="目标">${esc(t.services.map((s) => label[s] || s).join("、"))}</td><td data-label="地区 / 运营商">${esc(countries.join("、") || "—")}<small>按各节点设置</small></td><td data-label="节点">${nodes.length} 个${waiting ? `<small class="warn-text">${waiting} 台未生效</small>` : ""}</td><td data-label="最近执行">${last}</td><td data-label="操作"><div class="row-actions"><button data-edit-region="${esc(t.id)}">编辑</button><button data-toggle-region="${esc(t.id)}">${t.enabled ? "暂停" : "恢复"}</button>${moreMenu(`<button data-run-region="${esc(t.id)}" ${!t.enabled || !nodes.length ? "disabled" : ""}>立即检测</button><button data-results-region="${esc(t.id)}">检测结果</button><button class="menu-danger" data-delete-region="${esc(t.id)}">删除</button>`)}</div></td></tr>`;
  }).join("");
}
// 「最近执行」一格：各目标最近一次执行的汇总（/admin/tasks/health）。有失败就标橙并写几项，
// 全部成功标绿，只有在途的写「执行中」；明细仍在「执行记录」里。
function healthCell(t, h) {
  if (!t.node_ids?.length) return '<span class="muted">未绑定节点</span>';
  if (!h) return '<span class="muted">尚未执行</span>';
  const total = h.ok + h.failed + h.pending;
  const pill = h.failed
    ? `<span class="pill warn">${h.failed} / ${total} 项失败</span>`
    : h.ok
      ? `<span class="pill good">正常</span>`
      : `<span class="pill">执行中</span>`;
  return `${pill}<small>${h.last_finished ? "完成于 " + date(h.last_finished) : "尚无完成记录"}</small>`;
}
function renderTasks(health = {}, blocks = [], region = null) {
  content().innerHTML = `<section data-part="page-heading" class="page-heading"><div><div class="eyebrow">NETWORK OBSERVABILITY</div><h1>网络探测<span>${state.tasks.length + (region?.tasks?.length || 0)}</span></h1><p>延迟与回程、解锁、IP 被墙和地区偏移检测都在这里创建；每个任务独立配置目标、频率和参与节点。</p></div><div class="heading-actions"><button id="bind-selected">批量绑定节点</button><button id="add-task" class="primary">＋ 创建任务</button></div></section>${blockChecksHTML(blocks)}<section data-part="panel" class="panel"><div class="table-scroll"><table class="stack"><thead><tr><th class="select-col"><label class="checkbox select-all"><input id="select-tasks" type="checkbox" aria-label="选择全部任务"><span>全选</span></label></th><th>任务</th><th>状态</th><th>目标</th><th>地区 / 运营商</th><th>节点</th><th>最近执行</th><th>操作</th></tr></thead><tbody>${state.tasks.map((t) => { const s = taskSummary(t); const bound = t.node_ids?.filter((id) => state.nodes.some((n) => n.id === id)).length || 0; return `<tr><td data-label="选择"><input type="checkbox" data-select-task="${t.id}" aria-label="选择 ${esc(t.name)}"></td><td data-label="任务">${esc(t.name)}<small>${s.block ? "IP 被墙检测 · " : s.unlock ? "解锁检测 · " : ""}${esc(s.intervals)}</small></td><td data-label="状态"><span class="pill ${t.enabled ? "good" : ""}">${t.enabled ? "已启用" : "已停用"}</span></td><td data-label="目标">${s.unlock ? "内置目录" : s.count + " 个地址"}<small>${esc(s.families)}</small></td><td data-label="地区 / 运营商" title="${esc(s.targetsFull)}">${esc(s.targets)}<small>${s.regions > 3 ? `共 ${s.regions} 个地区` : ""}</small></td><td data-label="节点">${bound} 个</td><td data-label="最近执行">${healthCell(t, health[t.id])}</td><td data-label="操作"><div class="row-actions"><button data-edit-task="${t.id}">编辑</button><button data-control="${t.enabled ? "pause" : "resume"}" data-task="${t.id}">${t.enabled ? "暂停" : "恢复"}</button>${moreMenu(`<button data-control="run" data-task="${t.id}" ${!t.enabled || !bound ? "disabled" : ""}>立即执行</button><button data-runs-task="${t.id}">执行记录</button><button data-copy-task="${t.id}">复制</button><button class="menu-danger" data-delete-task="${t.id}">删除</button>`)}</div></td></tr>`; }).join("")}${regionRows(region)}${state.tasks.length || region?.tasks?.length ? "" : '<tr><td colspan="8" class="empty-cell">暂无任务。点「创建任务」选择类型。</td></tr>'}</tbody></table></div></section>`;
  wireMoreMenus(content());
  wireBlockChecks(content(), blocks, render);
  const regionTask = (id) => region?.tasks.find((t) => t.id === id);
  content().querySelectorAll("[data-edit-region]").forEach((b) => (b.onclick = () => regionTaskEditor(regionTask(b.dataset.editRegion), region, state.nodes, render)));
  content().querySelectorAll("[data-toggle-region]").forEach((b) => (b.onclick = guard(async () => {
    const t = regionTask(b.dataset.toggleRegion);
    await send(`/admin/ipguard/tasks/${encodeURIComponent(t.id)}`, "PUT", { name: t.name, enabled: !t.enabled, auto_recover: t.auto_recover, services: t.services, node_ids: t.node_ids });
    await render();
  })));
  content().querySelectorAll("[data-run-region]").forEach((b) => (b.onclick = guard(async () => {
    const out = await send(`/admin/ipguard/tasks/${encodeURIComponent(b.dataset.runRegion)}/run`, "POST", {});
    toast(out?.queued ? `已排队 ${out.queued} 项检测${out.skipped ? `，跳过 ${out.skipped} 项（未设置或出口未连接）` : ""}` : "没有可检测的节点：先在节点管理里设置目标地区，并确认出口已连接");
  })));
  content().querySelectorAll("[data-results-region]").forEach((b) => (b.onclick = guard(() => regionTaskResults(regionTask(b.dataset.resultsRegion), render))));
  content().querySelectorAll("[data-delete-region]").forEach((b) => (b.onclick = () => {
    const t = regionTask(b.dataset.deleteRegion);
    confirmAction(
      `删除任务 · ${t.name}`,
      `删除「${t.name}」，它包含的节点不再检测地区偏移，检测记录一并清除。各节点的地区偏移设置保留，之后可以加进别的任务。`,
      async () => { await send(`/admin/ipguard/tasks/${encodeURIComponent(t.id)}`, "DELETE"); await render(); },
      { label: "删除任务" },
    );
  }));
  content().querySelector("#select-tasks").onchange = (e) => content().querySelectorAll("[data-select-task]").forEach((b) => b.checked = e.target.checked);
  content().querySelector("#bind-selected").onclick = () => {
    const ids = new Set([...content().querySelectorAll("[data-select-task]:checked")].map((b) => b.dataset.selectTask));
    if (!ids.size) { toast("请先勾选任务"); return; }
    if (state.tasks.some((t) => ids.has(t.id) && t.type === "block")) { toast("IP 被墙检测需分别指定大陆和境外节点，请编辑对应任务"); return; }
    bindTasks(state.tasks.filter((t) => ids.has(t.id)), state.nodes, render);
  };
  content().querySelectorAll("[data-control]").forEach((b) => b.onclick = guard(async () => {
    b.disabled = true;
    try { await controlTask(b.dataset.task, b.dataset.control, render); } finally { b.disabled = false; }
  }));
  content().querySelectorAll("[data-runs-task]").forEach((b) => b.onclick = guard(() => taskRuns(state.tasks.find((t) => t.id === b.dataset.runsTask))));
  content().querySelectorAll("[data-copy-task]").forEach((b) => b.onclick = guard(async () => {
    const copy = structuredClone(state.tasks.find((t) => t.id === b.dataset.copyTask));
    delete copy.id; copy.name = copy.name.slice(0, 20) + " 副本"; copy.enabled = false;
    await taskForm(copy);
  }));
  content().querySelector("#add-task").onclick = chooseTaskType;
  content()
    .querySelectorAll("[data-edit-task]")
    .forEach(
      (b) =>
        (b.onclick = guard(() =>
          taskForm(state.tasks.find((t) => t.id === b.dataset.editTask)),
        )),
    );
  content()
    .querySelectorAll("[data-delete-task]")
    .forEach(
      (b) =>
        (b.onclick = () =>
          confirmAction(
            `删除任务 · ${taskName(b.dataset.deleteTask)}`,
            `删除「${taskName(b.dataset.deleteTask)}」，并清除它的全部探测结果。此操作无法撤销。`,
            async () => {
              await send("/admin/tasks/" + b.dataset.deleteTask, "DELETE");
              await render();
            },
            { label: "删除任务" },
          )),
    );
}
async function taskForm(existing) {
  if (existing?.type === "block") return blockTaskEditor(existing, state.nodes, render);
  if (!state.catalog) state.catalog = await api("/admin/catalog");
  taskEditor(existing, state.catalog, state.nodes, render);
}
