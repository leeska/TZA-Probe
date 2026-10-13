import { controlTask, bindTasks, taskRuns } from "./task-controls.js";
import { taskEditor, taskSummary } from "./task-editor.js";
import { api, send, esc, toast, guard } from "./api.js";
import { date } from "./format.js";
import { state, content, taskName, confirmAction, moreMenu, wireMoreMenus, render } from "./admin-state.js";
export async function tasksPage() {
  // 执行汇总读不到不该挡住任务列表：那一列退回「尚未执行」。
  const [list, health] = await Promise.all([api("/admin/tasks"), api("/admin/tasks/health").catch(() => ({}))]);
  state.tasks = list;
  renderTasks(health);
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
function renderTasks(health = {}) {
  content().innerHTML = `<section class="page-heading"><div><div class="eyebrow">NETWORK OBSERVABILITY</div><h1>网络探测<span>${state.tasks.length}</span></h1><p>每个任务独立配置目标、协议、频率和参与节点。</p></div><div class="heading-actions"><button id="bind-selected">批量绑定节点</button><button id="add-task" class="primary">＋ 创建任务</button></div></section><section class="panel"><div class="table-scroll"><table class="stack"><thead><tr><th class="select-col"><label class="checkbox select-all"><input id="select-tasks" type="checkbox" aria-label="选择全部任务"><span>全选</span></label></th><th>任务</th><th>状态</th><th>目标</th><th>地区 / 运营商</th><th>节点</th><th>最近执行</th><th>操作</th></tr></thead><tbody>${state.tasks.map((t) => { const s = taskSummary(t); const bound = t.node_ids?.filter((id) => state.nodes.some((n) => n.id === id)).length || 0; return `<tr><td data-label="选择"><input type="checkbox" data-select-task="${t.id}" aria-label="选择 ${esc(t.name)}"></td><td data-label="任务">${esc(t.name)}<small>${s.unlock ? "解锁检测 · " : ""}${esc(s.intervals)}</small></td><td data-label="状态"><span class="pill ${t.enabled ? "good" : ""}">${t.enabled ? "已启用" : "已停用"}</span></td><td data-label="目标">${s.unlock ? "内置目录" : s.count + " 个地址"}<small>${esc(s.families)}</small></td><td data-label="地区 / 运营商" title="${esc(s.targetsFull)}">${esc(s.targets)}<small>${s.regions > 3 ? `共 ${s.regions} 个地区` : ""}</small></td><td data-label="节点">${bound} 个</td><td data-label="最近执行">${healthCell(t, health[t.id])}</td><td data-label="操作"><div class="row-actions"><button data-edit-task="${t.id}">编辑</button><button data-control="${t.enabled ? "pause" : "resume"}" data-task="${t.id}">${t.enabled ? "暂停" : "恢复"}</button>${moreMenu(`<button data-control="run" data-task="${t.id}" ${!t.enabled || !bound ? "disabled" : ""}>立即执行</button><button data-runs-task="${t.id}">执行记录</button><button data-copy-task="${t.id}">复制</button><button class="menu-danger" data-delete-task="${t.id}">删除</button>`)}</div></td></tr>`; }).join("") || '<tr><td colspan="8" class="empty-cell">暂无任务。可从内置三网目标目录选择，也可填写自定义目标。</td></tr>'}</tbody></table></div></section>`;
  wireMoreMenus(content());
  content().querySelector("#select-tasks").onchange = (e) => content().querySelectorAll("[data-select-task]").forEach((b) => b.checked = e.target.checked);
  content().querySelector("#bind-selected").onclick = () => {
    const ids = new Set([...content().querySelectorAll("[data-select-task]:checked")].map((b) => b.dataset.selectTask));
    if (!ids.size) { toast("请先勾选任务"); return; }
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
  content().querySelector("#add-task").onclick = guard(() => taskForm());
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
  if (!state.catalog) state.catalog = await api("/admin/catalog");
  taskEditor(existing, state.catalog, state.nodes, render);
}
