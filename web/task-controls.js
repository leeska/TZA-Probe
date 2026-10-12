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

export function bindTasks(tasks, nodes, refresh) {
  const selected = new Set(tasks.length === 1 ? tasks[0].node_ids.filter((id) => nodes.some((n) => n.id === id)) : []);
  const d = modal("批量绑定节点", `<form><p>为 ${tasks.length} 个任务设置相同的节点列表，将替换原绑定。取消全部选择会停止这些任务的调度。</p><label>搜索节点<input id="node-search" type="search" placeholder="名称 / 分组 / 地区（支持拼音与缩写）"></label><div class="catalog-toolbar"><button type="button" id="select-visible">选中筛选结果</button><button type="button" id="clear-nodes">清空选择</button></div><div id="binding-nodes"></div><p id="binding-count"></p><div class="form-footer"><button type="submit" class="primary">保存绑定</button></div></form>`);
  let query = "";
  const matches = () => nodes.filter((n) => searchMatch(query, n.name, n.group, n.region));
  const render = () => {
    d.querySelector("#binding-nodes").innerHTML = matches().map((n) => `<label class="checkbox"><input type="checkbox" data-node="${esc(n.id)}" ${selected.has(n.id) ? "checked" : ""}>${esc(n.name)} · ${esc(n.group || "默认")}</label>`).join("") || '<p class="hint">没有匹配节点</p>';
    d.querySelector("#binding-count").textContent = `已选 ${selected.size} 个节点`;
    d.querySelectorAll("[data-node]").forEach((el) => el.onchange = () => {
      el.checked ? selected.add(el.dataset.node) : selected.delete(el.dataset.node);
      d.querySelector("#binding-count").textContent = `已选 ${selected.size} 个节点`;
    });
  };
  d.querySelector("#node-search").oninput = (e) => {query = e.target.value; render();};
  d.querySelector("#select-visible").onclick = () => {matches().forEach((n) => selected.add(n.id)); render();};
  d.querySelector("#clear-nodes").onclick = () => {selected.clear(); render();};
  render();
  d.querySelector("form").onsubmit = guard(async (e) => {
    e.preventDefault();
    const button = d.querySelector('[type="submit"]'); button.disabled = true;
    // 一次提交全部任务，服务端在一个事务里改：要么都改了，要么都没改。
    try {
      await send("/admin/tasks/bindings", "PUT", {task_ids: tasks.map((t) => t.id), node_ids: [...selected]});
      d.close(); toast("节点绑定已更新"); await refresh();
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
