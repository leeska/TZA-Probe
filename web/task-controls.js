import { api, send, esc, modal, guard, toast } from "./api.js";
import { searchMatch } from "./search.js";
import { date, statusName, reasonName } from "./format.js";

// 暂停 / 恢复改的是 enabled，立即执行是单独的动作；两者都不清历史结果。
export async function controlTask(id, action, refresh) {
  if (action === "run") await send(`/admin/tasks/${id}/run`, "POST");
  else await send(`/admin/tasks/${id}/enabled`, "PUT", {enabled: action === "resume"});
  toast(action === "run" ? "已安排执行，等待 Agent 领取；离线节点上线后执行" : action === "pause" ? "已暂停后续调度，正在执行的探测会完成" : "已恢复调度");
  await refresh();
}

// 绑定节点：多选任务时各任务原本绑的节点往往不一样，所以每个节点按「全部已绑 / 部分已绑 / 未绑」
// 显示（部分已绑是半选），保存时只提交点过的节点（PUT /admin/tasks/bindings 的 add / remove），
// 各任务其余的绑定原样保留。以前多选时一律从空白开始、保存是整份替换，加一台就会把别的全解绑。
export function bindTasks(tasks, nodes, refresh) {
  const live = (t) => new Set((t.node_ids || []).filter((id) => nodes.some((n) => n.id === id)));
  const bound = tasks.map(live);
  const initial = new Map(nodes.map((n) => {
    const count = bound.filter((set) => set.has(n.id)).length;
    return [n.id, count === tasks.length ? "all" : count ? "some" : "none"];
  }));
  const state = new Map(initial);
  const one = tasks.length === 1;
  const names = tasks.map((t) => t.name).join("、");
  const d = modal(
    one ? `绑定节点 · ${tasks[0].name}` : `批量绑定节点（${tasks.length} 个任务）`,
    `<form><p>${one ? "勾选这个任务要在哪些节点上执行。取消全部勾选会停止它的调度。" : `所选任务：${esc(names)}。半选的节点只有部分任务绑定了：点一下改为全部绑定，再点一下全部解绑。保存时只改动你点过的节点，其余绑定保持不变。`}</p><label>搜索节点<input id="node-search" type="search" placeholder="名称 / 分组 / 地区（支持拼音与缩写）"></label><div class="catalog-toolbar"><button type="button" id="select-visible">全部绑定筛选结果</button><button type="button" id="clear-nodes">全部解绑</button></div><div id="binding-nodes" class="node-checkboxes"></div><p id="binding-count" class="hint"></p><div class="form-footer"><button type="submit" class="primary">保存绑定</button></div></form>`,
  );
  let query = "";
  const matches = () => nodes.filter((n) => searchMatch(query, n.name, n.group, n.region));
  const changes = () => {
    const add = [], remove = [];
    for (const [id, now] of state) {
      if (now === initial.get(id)) continue;
      if (now === "all") add.push(id);
      else if (now === "none") remove.push(id);
    }
    return { add, remove };
  };
  const count = () => {
    const { add, remove } = changes();
    const bound = [...state.values()].filter((v) => v === "all").length;
    const partial = [...state.values()].filter((v) => v === "some").length;
    d.querySelector("#binding-count").textContent =
      `全部绑定 ${bound} 个${partial ? `、部分绑定 ${partial} 个` : ""}节点` +
      (add.length || remove.length ? ` · 将添加 ${add.length} 个、移除 ${remove.length} 个` : " · 尚未改动");
  };
  const render = () => {
    const list = d.querySelector("#binding-nodes");
    list.innerHTML = matches().map((n) => `<label class="checkbox"><input type="checkbox" data-node="${esc(n.id)}" ${state.get(n.id) === "all" ? "checked" : ""}>${esc(n.name)} · ${esc(n.group || "默认")}</label>`).join("") || '<p class="hint">没有匹配节点</p>';
    list.querySelectorAll("[data-node]").forEach((el) => {
      el.indeterminate = state.get(el.dataset.node) === "some";
      el.onchange = () => {
        state.set(el.dataset.node, el.checked ? "all" : "none");
        count();
      };
    });
    count();
  };
  d.querySelector("#node-search").oninput = (e) => {query = e.target.value; render();};
  d.querySelector("#select-visible").onclick = () => {matches().forEach((n) => state.set(n.id, "all")); render();};
  d.querySelector("#clear-nodes").onclick = () => {nodes.forEach((n) => state.set(n.id, "none")); render();};
  render();
  d.querySelector("form").onsubmit = guard(async (e) => {
    e.preventDefault();
    const { add, remove } = changes();
    if (!add.length && !remove.length) {
      d.close();
      toast("绑定没有改动");
      return;
    }
    const button = d.querySelector('[type="submit"]'); button.disabled = true;
    // 一次提交全部任务，服务端在一个事务里改：要么都改了，要么都没改。
    try {
      await send("/admin/tasks/bindings", "PUT", {task_ids: tasks.map((t) => t.id), add, remove});
      d.close(); toast(`已添加 ${add.length} 个、移除 ${remove.length} 个节点`); await refresh();
    } finally {button.disabled = false;}
  });
}

export async function taskRuns(task) {
  const d = modal(task.name + " · 执行记录", '<p class="loading">读取执行状态…</p>');
  const render = async () => {
    const runs = await api(`/admin/tasks/${task.id}/runs`);
    if (!d.open) return;
    const body = `<p class="hint">显示当前绑定节点各目标的最近一次执行，记录最多保留 7 天。暂停不会取消已领取的探测。</p><div class="catalog-toolbar"><button type="button" id="refresh-runs">↻ 刷新状态</button></div><div class="table-scroll"><table class="stack"><thead><tr><th>节点</th><th>目标</th><th>最近执行</th><th>结果 / 原因</th></tr></thead><tbody>${runs.map((r) => `<tr><td data-label="节点">${esc(r.node_name)}</td><td data-label="目标">${esc([r.family,r.region,r.carrier,r.host,r.port || ""].filter(Boolean).join(" · "))}<small>${r.kind === "route" ? "回程" : r.kind === "tcp" ? "TCP 延迟" : "尚未领取"}</small></td><td data-label="最近执行">${r.issued ? date(r.issued) : "尚未执行"}<small>${r.finished ? "完成 " + date(r.finished) : ""}</small></td><td data-label="结果 / 原因">${esc(statusName[r.status] || "尚未执行")}<small>${esc(reasonName[r.reason] || r.reason || "")}</small></td></tr>`).join("") || '<tr><td colspan="4">未绑定节点，请先设置节点绑定。</td></tr>'}</tbody></table></div>`;
    const head = d.querySelector(".modal-head"); d.replaceChildren(head); d.insertAdjacentHTML("beforeend",body);
    d.querySelector("#refresh-runs").onclick = guard(render);
  };
  await render();
}
