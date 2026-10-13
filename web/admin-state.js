import { esc, modal, guard } from "./api.js";
// 后台各页共用的状态与表单小工具。页面模块（admin-nodes / tasks / alerts / settings）
// 只经由这里互相知道对方：跨页跳转用 go()，重绘当前页用 render()，两者都由外壳
// admin.js 注册；页面模块不反过来 import admin.js。
export const state = {
  container: null,
  brandHTML: "",
  tab: "nodes",
  nodes: [],
  tasks: [],
  regions: [],
  // 服务端版本（/session）：它内置的 Agent 安装包与它同一个版本，节点上报的版本不一样就该升级。
  serverVersion: "",
  catalog: null,
  // 告警页的子页：当前告警 / 事件记录 / 提醒规则。节点页可以直接跳到其中一个。
  alertView: "active",
};
let shellRender = async () => {};
export function setRenderer(fn) {
  shellRender = fn;
}
export const render = () => shellRender();
export async function go(tab, alertView) {
  state.tab = tab;
  if (alertView) state.alertView = alertView;
  await render();
}
export const content = () => state.container.querySelector("#admin-content");
// 顶栏状态取自刚读到的节点列表：全部在线为绿，有离线为橙。以前这里写死「系统运行中」，
// 出了问题也照样亮着。
export function fleetStatus() {
  const el = state.container.querySelector("#fleet-status");
  if (!el) return;
  const online = state.nodes.filter((n) => n.online).length;
  el.className = "pill" + (!state.nodes.length ? "" : online === state.nodes.length ? " good" : " warn");
  el.textContent = state.nodes.length ? `● ${online} / ${state.nodes.length} 在线` : "● 暂无节点";
}
export function field(label, name, value, type = "text", extra = "") {
  return `<label>${label}<input name="${name}" type="${type}" value="${esc(value)}" ${extra}></label>`;
}
export function check(label, name, value) {
  return `<label class="checkbox"><input type="checkbox" name="${name}" ${value ? "checked" : ""}>${label}</label>`;
}
export function select(label, name, options, value) {
  return `<label>${label}<select name="${name}">${options.map(([v, l]) => `<option value="${v}" ${v === value ? "selected" : ""}>${l}</option>`).join("")}</select></label>`;
}
// 现值不在选项表里就追加一条（标签写现值本身，空值写「未设置」），保证打开表单保存一遍
// 不会把老值换掉。
export function withCurrent(options, current, emptyLabel = "未设置") {
  const list = options.map(([value, label]) => [value, label]);
  const value = String(current ?? "");
  if (!list.some(([known]) => known === value)) list.push([value, value || emptyLabel]);
  return list;
}
// 确认框写明对象：连着处理几台机器时，只写「删除节点」很容易点错行。
export const nodeName = (id) => state.nodes.find((n) => n.id === id)?.name || id;
export const taskName = (id) => state.tasks.find((t) => t.id === id)?.name || id;
export function confirmAction(title, message, action, { label = "确认", danger = true } = {}) {
  const d = modal(
    title,
    `<p>${esc(message)}</p><div class="form-footer"><button class="${danger ? "danger" : "primary"}" id="confirm">${esc(label)}</button></div>`,
  );
  d.querySelector("#confirm").onclick = guard(async () => {
    d.close();
    await action();
  });
}
// 行内只留最常用的两个动作，其余收进「更多」：一行七个链接在窄一点的屏幕上就折成两行。
// 菜单展开时挪到 <body> 下再 fixed 定位：表格外层的 .table-scroll 会裁掉浮层，带
// backdrop-filter 的面板又会让 fixed 变成相对面板定位。收起时挪回原处。
export function moreMenu(items) {
  return `<details class="row-more more"><summary>更多</summary><div class="row-menu" role="menu">${items}</div></details>`;
}
export function wireMoreMenus(root) {
  for (const more of root.querySelectorAll(".row-more")) {
    const menu = more.querySelector(".row-menu");
    more.addEventListener("toggle", () => {
      if (!more.open) {
        more.append(menu);
        return;
      }
      for (const other of document.querySelectorAll(".row-more[open]")) if (other !== more) other.open = false;
      document.body.append(menu);
      const rect = more.querySelector("summary").getBoundingClientRect();
      const width = menu.offsetWidth;
      const height = menu.offsetHeight;
      const below = rect.bottom + 6;
      menu.style.left = Math.max(8, Math.min(rect.right - width, document.documentElement.clientWidth - width - 8)) + "px";
      menu.style.top = (below + height > window.innerHeight - 8 ? Math.max(8, rect.top - height - 6) : below) + "px";
    });
    // 点了菜单里的哪一项都先收起（那一项自己的处理照常执行）。
    menu.addEventListener("click", () => (more.open = false));
  }
}
// 点到菜单外、按 Esc 或滚动时收起：fixed 的菜单不会跟着行走。
const closeMenus = () => document.querySelectorAll(".row-more[open]").forEach((m) => (m.open = false));
document.addEventListener("click", (e) => {
  if (!e.target?.closest?.(".row-more, .row-menu")) closeMenus();
});
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape") closeMenus();
});
window.addEventListener("scroll", closeMenus, { passive: true });
