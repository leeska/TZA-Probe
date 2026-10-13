import { controlTask, bindTasks, taskRuns } from "./task-controls.js";
import { themeButton, THEMES } from "./theme.js";
import { skinButton, SKINS } from "./skins.js";
import { showToken } from "./agent-setup.js";
import { taskEditor, taskSummary } from "./task-editor.js";
import { api, send, esc, toast, modal, guard } from "./api.js";
import { bytes, date, days, kindName, stamp, stampISO, statusLabel } from "./format.js";
import { detail } from "./detail.js";
import { regionOptions } from "./regions.js";
import { regionCombobox, wireComboboxes } from "./region-picker.js";
let container,
  brandHTML,
  tab = "nodes",
  nodes = [],
  tasks = [],
  regions = [],
  catalog;
const defaults = {
  name: "",
  group: "",
  region: "",
  order: 0,
  public: true,
  display: { route_region: "", latency_region: "", latency_carriers: [], route_carriers: [] },
  billing: {
    reset_at: "",
    traffic_limit: 0,
    traffic_mode: "sum",
    free: false,
    price: 0,
    currency: "CNY",
    price_period: "月",
    expires_at: "",
  },
  line: { mbps: 0, throttle_mbps: 0, throttle_hours: 24, throttle_on: "bandwidth" },
  // 提醒规则是一个列表，列出的规则开启；新节点默认一条都不开。
  alerts: [],
};
export async function mountAdmin(root, brand) {
  container = root;
  brandHTML = brand;
  try {
    await api("/session");
    shell();
    await render();
  } catch (e) {
    if (e.status === 401) login();
    else throw e;
  }
}
function login() {
  container.innerHTML = `<div class="login-layout"><section class="login-story">${brandHTML}<div><div class="eyebrow">SMALL STACK. CLEAR CONTROL.</div><h1>少一点复杂，<br>多一点掌控。</h1><p>主机、网络、用量与提醒。<br>一套属于自己的基础设施观测工具。</p></div><div class="login-story-foot"><small>TZA PROBE / CONTROL PLANE</small>${skinButton()}${themeButton()}</div></section><main class="login-panel"><form id="login"><div class="eyebrow">WELCOME BACK</div><h2>进入管理控制台</h2><p>使用部署时生成的管理密钥登录。</p><input type="text" name="username" value="admin" autocomplete="username" readonly hidden aria-hidden="true" tabindex="-1"><label>管理密钥<input type="password" name="token" required minlength="32" autocomplete="current-password" autofocus></label><p class="hint">密钥保存在服务器数据目录的 admin-token 文件中。</p><button class="primary" type="submit">登录控制台 →</button><a href="/">← 返回公开面板</a></form></main></div>`;
  container.querySelector("form").onsubmit = guard(async (e) => {
    e.preventDefault();
    const button = e.target.querySelector("button");
    button.disabled = true;
    try {
      await send("/login", "POST", {
        token: new FormData(e.target).get("token"),
      });
      shell();
      await render();
    } finally {
      button.disabled = false;
    }
  });
}
function shell() {
  container.innerHTML = `<div class="admin-layout"><aside class="sidebar">${brandHTML}<div class="sidebar-caption">WORKSPACE</div><nav><button data-tab="nodes"><svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="3" y="3" width="6" height="6" rx="1.5"/><rect x="11" y="3" width="6" height="6" rx="1.5"/><rect x="3" y="11" width="6" height="6" rx="1.5"/><rect x="11" y="11" width="6" height="6" rx="1.5"/></svg><span>节点管理</span></button><button data-tab="tasks"><svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="10" cy="10" r="7"/><circle cx="10" cy="10" r="3"/><circle cx="10" cy="10" r="0.8" fill="currentColor"/></svg><span>网络探测</span></button><button data-tab="alerts"><svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="10" cy="10" r="7"/><polyline points="10 6 10 10 13 12"/></svg><span>告警与通知</span></button><button data-tab="settings"><svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><line x1="4" y1="6" x2="16" y2="6"/><line x1="4" y1="14" x2="16" y2="14"/><circle cx="8" cy="6" r="2" fill="currentColor"/><circle cx="12" cy="14" r="2" fill="currentColor"/></svg><span>面板设置</span></button></nav><div class="sidebar-bottom"><a href="/">公开面板 ↗</a>${skinButton("sidebar-skin")}${themeButton("sidebar-theme")}<button id="logout">退出登录</button><small>独立运行 · TZA PROBE V2</small></div></aside><main class="admin-main"><header class="admin-top"><span>控制台 <span class="muted">/</span> <b id="breadcrumb"></b></span><span class="pill" id="fleet-status" aria-live="polite"></span></header><div id="admin-content"></div></main></div>`;
  container.querySelectorAll("[data-tab]").forEach(
    (b) =>
      (b.onclick = guard(async () => {
        tab = b.dataset.tab;
        await render();
      })),
  );
  container.querySelector("#logout").onclick = guard(async () => {
    await send("/logout", "POST");
    login();
  });
}
async function render() {
  // 切页时面包屑与侧栏高亮立刻换，内容要等接口回来：中间这半秒把旧内容调暗并标成忙，
  // 不然看起来像点到了「网络探测」却还停在「节点管理」。
  const area = content();
  // 换页 / 重绘会把行连同「更多」一起换掉；挪到 body 下还开着的菜单一并清掉，不留孤儿。
  document.querySelectorAll("body > .row-menu").forEach((m) => m.remove());
  area?.classList.add("is-loading");
  area?.setAttribute("aria-busy", "true");
  try {
    await renderTab();
  } finally {
    area?.classList.remove("is-loading");
    area?.removeAttribute("aria-busy");
  }
}
async function renderTab() {
  container
    .querySelectorAll("[data-tab]")
    .forEach((b) => b.classList.toggle("active", b.dataset.tab === tab));
  container.querySelector("#breadcrumb").textContent = {
    nodes: "节点管理",
    tasks: "网络探测",
    alerts: "告警与通知",
    settings: "面板设置",
  }[tab];
  nodes = await api("/admin/nodes");
  fleetStatus();
  if (tab === "nodes") {
    // 「正在告警」的数字读不到时只少这一格的数，不挡住节点列表。
    const [known, firing] = await Promise.all([knownRegions(), api("/admin/events?active=1&limit=500").catch(() => null)]);
    regions = known;
    renderNodes(firing);
  }
  if (tab === "tasks") {
    // 执行汇总读不到不该挡住任务列表：那一列退回「尚未执行」。
    const [list, health] = await Promise.all([api("/admin/tasks"), api("/admin/tasks/health").catch(() => ({}))]);
    tasks = list;
    renderTasks(health);
  }
  if (tab === "alerts") await renderAlerts();
  if (tab === "settings") await renderSettings();
}
const content = () => container.querySelector("#admin-content");
// 顶栏状态取自刚读到的节点列表：全部在线为绿，有离线为橙。以前这里写死「系统运行中」，
// 出了问题也照样亮着。
function fleetStatus() {
  const el = container.querySelector("#fleet-status");
  if (!el) return;
  const online = nodes.filter((n) => n.online).length;
  el.className = "pill" + (!nodes.length ? "" : online === nodes.length ? " good" : " warn");
  el.textContent = nodes.length ? `● ${online} / ${nodes.length} 在线` : "● 暂无节点";
}
// 到期时间：已过期标红、7 天内到期标橙（与公开卡片的到期胶囊同一套阈值），扫一眼就知道该续哪台。
function dueCell(expiresAt) {
  if (!expiresAt) return "—";
  const left = days(expiresAt);
  const cls = left < 0 ? "due expired" : left <= 7 ? "due soon" : "due";
  const note = left < 0 ? `<small>已过期 ${-left} 天</small>` : left === 0 ? "<small>今天到期</small>" : left <= 7 ? `<small>剩余 ${left} 天</small>` : "";
  return `<span class="${cls}">${esc(expiresAt.slice(0, 10))}</span>${note}`;
}
function renderNodes(firing) {
  content().innerHTML = `<section class="page-heading"><div><div class="eyebrow">INVENTORY</div><h1>节点管理<span>${nodes.length}</span></h1><p>管理接入、展示、计费周期和每台主机的提醒规则。</p></div><button id="add-node" class="primary">＋ 添加节点</button></section><div class="admin-stats"><div><span>在线</span><strong>${nodes.filter((n) => n.online).length}</strong></div><div><span>公开展示</span><strong>${nodes.filter((n) => n.public).length}</strong></div><div><span>7 天内到期或过期</span><strong>${nodes.filter((n) => n.billing.expires_at && days(n.billing.expires_at) <= 7).length}</strong></div><div id="goto-firing" class="stat-link${firing?.length ? " stat-warn" : ""}" role="button" tabindex="0" title="查看当前告警"><span>正在告警</span><strong>${firing ? firing.length : "—"}</strong></div></div><section class="panel"><div class="panel-head"><h3>全部节点</h3><button id="refresh-nodes" class="quiet">↻ 刷新</button></div><div class="table-scroll"><table class="stack"><thead><tr><th class="drag-col" title="按住手柄拖动调整顺序">顺序</th><th>主机</th><th>状态</th><th>分组 / 地区</th><th>周期用量</th><th>到期时间</th><th>操作</th></tr></thead><tbody>${nodes.map((n) => `<tr data-node="${n.id}"><td class="drag-cell" data-label="顺序">${dragHandle(n)}</td><td data-label="主机"><button class="text-button" data-inspect="${n.id}">${esc(n.name)}</button><small>${n.public ? "公开" : "私有"} · 回程地区 ${esc(n.display.route_region || "未设置")}</small></td><td data-label="状态"><span class="pill ${n.online ? "good" : ""}">${statusLabel(n)}</span><small>Agent ${n.metrics ? [n.metrics.version, n.metrics.arch].filter(Boolean).map(esc).join(" · ") : "未接入"}</small><small>上报 ${n.last_seen ? date(n.last_seen) : "从未"}</small></td><td data-label="分组 / 地区">${esc(n.group || "默认")}<small>${esc(n.region || "—")}</small></td><td data-label="周期用量">${bytes(n.traffic_used)}<small>${n.billing.traffic_limit ? "/ " + bytes(n.billing.traffic_limit) : "未设置额度"}</small></td><td data-label="到期时间">${dueCell(n.billing.expires_at)}</td><td data-label="操作"><div class="row-actions"><button data-edit="${n.id}">编辑</button><button data-token="${n.id}" title="${n.last_seen ? "生成新密钥并给出安装命令，旧密钥立即失效" : "生成密钥并给出安装命令"}">${n.last_seen ? "重置密钥" : "接入"}</button><button data-billing="${n.id}">用量校正</button><button data-delete="${n.id}" class="danger">删除</button></div></td></tr>`).join("") || '<tr><td colspan="7" class="empty-cell">还没有节点。添加后下载接入配置，启动 Agent。</td></tr>'}</tbody></table></div></section>`;
  content().querySelectorAll("[data-billing]").forEach((b) => {
    b.onclick = () => {
      const n = nodes.find((x) => x.id === b.dataset.billing);
      const d = modal("校正本周期用量", `<form><p>将当前周期总用量校正为指定值；后续采集继续累加，下个周期自动失效。</p>${field("实际已用流量（GB）", "used", n.traffic_used / 1e9, "number", 'min="0" max="10000000" step="any" required')}<p class="hint">当前周期：${date(n.cycle_start)} — ${date(n.cycle_end)}。修改计费方向或重置日会清除校正。</p><div class="form-footer"><button class="primary" type="submit">保存校正</button></div></form>`);
      d.querySelector("form").onsubmit = guard(async (e) => {
        e.preventDefault();
        await send(`/admin/nodes/${n.id}/traffic`, "POST", {used: Math.round(Number(new FormData(e.target).get("used")) * 1e9), cycle: n.cycle_start});
        d.close(); await render(); toast("周期用量已校正");
      });
    };
  });
  content().querySelector("#add-node").onclick = guard(() => nodeForm());
  // 进后台最想先知道的是有没有东西在告警：这一格点进去就是告警页的「当前告警」。
  const firingCard = content().querySelector("#goto-firing");
  const gotoFiring = guard(async () => {
    tab = "alerts";
    alertView = "active";
    await render();
  });
  firingCard.onclick = gotoFiring;
  firingCard.onkeydown = (e) => {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      gotoFiring();
    }
  };
  content().querySelector("#refresh-nodes").onclick = guard(render);
  content()
    .querySelectorAll("[data-edit]")
    .forEach(
      (b) =>
        (b.onclick = guard(() =>
          nodeForm(nodes.find((n) => n.id === b.dataset.edit)))),
    );
  content()
    .querySelectorAll("[data-inspect]")
    .forEach((b) => (b.onclick = guard(() => detail(b.dataset.inspect))));
  content()
    .querySelectorAll("[data-token]")
    .forEach(
      (b) =>
        (b.onclick = () =>
          confirmAction(
            `重新生成密钥 · ${nodeName(b.dataset.token)}`,
            `「${nodeName(b.dataset.token)}」的旧 Agent 密钥会立即失效，生成后请用新的安装命令更新这台机器。`,
            async () => {
              const result = await send(
                `/admin/nodes/${b.dataset.token}/token`,
                "POST",
              );
              showToken(result.token);
            },
          )),
    );
  content()
    .querySelectorAll("[data-delete]")
    .forEach(
      (b) =>
        (b.onclick = () =>
          confirmAction(
            `删除节点 · ${nodeName(b.dataset.delete)}`,
            `删除「${nodeName(b.dataset.delete)}」及其历史、绑定和探测结果。此操作无法撤销。`,
            async () => {
              await send(`/admin/nodes/${b.dataset.delete}`, "DELETE");
              await render();
            },
          )),
    );
  wireReorder();
}
// 拖动排序：这张表就是服务端按 order 排出来的（storage.Nodes → 先 order 后 name），拖动
// 只改这个顺序：松手时把整份可见 id 列表交给 POST /admin/nodes/order，由服务端按 0,1,2…
// 重写 order——前端不自己算号（算错会和分组次序打架），更不逐台 PUT 整份配置（那会把没
// 回填的字段清掉）。
//
// 用 pointer 事件而不是 HTML5 drag：后者在手机上根本不触发，而这套面板是会在手机上开的。
// 手柄 setPointerCapture 之后 pointermove / pointerup 都回到手柄，落点用 elementFromPoint
// 认（它是命中测试，不受指针捕获影响）。
function wireReorder() {
  const body = content().querySelector("table.stack tbody");
  if (!body) return;
  const rows = () => [...body.querySelectorAll("tr[data-node]")];
  body.querySelectorAll("[data-drag]").forEach((handle) => {
    const row = handle.closest("tr[data-node]");
    let moved = false;
    const move = (e) => {
      const over = document
        .elementFromPoint(e.clientX, e.clientY)
        ?.closest("tr[data-node]");
      if (over && over !== row) {
        const rect = over.getBoundingClientRect();
        moved = true;
        // 落在下半行就插到它后面，落在上半行就插到它前面。
        body.insertBefore(
          row,
          e.clientY > rect.top + rect.height / 2 ? over.nextSibling : over,
        );
      }
      // 拖到视口上下边缘时跟着滚一点，长一点的列表才拖得到头。用滚动元素的 scrollTop
      // 而不是 window.scrollBy：jsdom 没实现 scrollBy，会往控制台扔 "Not implemented"，
      // 而 ui-smoke 断言管理端控制台零报错（滚动元素在浏览器里就是 <html>）。
      const edge = 80;
      const scroller = document.scrollingElement || document.documentElement;
      if (e.clientY < edge) scroller.scrollTop -= 12;
      else if (e.clientY > window.innerHeight - edge) scroller.scrollTop += 12;
    };
    const drop = guard(async () => {
      stop();
      if (!moved) return;
      await send("/admin/nodes/order", "POST", {
        ids: rows().map((r) => r.dataset.node),
      });
      await render();
      toast("顺序已保存");
    });
    const cancel = () => {
      moved = false;
      stop();
    };
    // 监听挂在 document 上，不用 setPointerCapture：真浏览器里捕获会在拖动途中被隐式
    // 释放（lostpointercapture，26px 的手柄走出几十像素就丢了），之后 pointermove 直接
    // 落到别的元素上，手柄再也收不到事件——整段拖动白做，且完全没有报错。挂在 document
    // 上则无论指针在哪、中途有没有重排 DOM，事件都还在。
    const stop = () => {
      row.classList.remove("row-dragging");
      document.removeEventListener("pointermove", move);
      document.removeEventListener("pointerup", drop);
      document.removeEventListener("pointercancel", cancel);
    };
    handle.addEventListener("pointerdown", (e) => {
      if (e.button) return;
      // 按住手柄不顺带选中文字或起原生拖拽；触摸设备上的滚动由样式的 touch-action 关掉。
      e.preventDefault();
      moved = false;
      row.classList.add("row-dragging");
      document.addEventListener("pointermove", move);
      document.addEventListener("pointerup", drop);
      document.addEventListener("pointercancel", cancel);
    });
    handle.addEventListener("lostpointercapture", cancel);
  });
}
function field(label, name, value, type = "text", extra = "") {
  return `<label>${label}<input name="${name}" type="${type}" value="${esc(value)}" ${extra}></label>`;
}
// 新节点的默认重置时间：当月 1 日 00:00:00 UTC，与旧默认「每月 1 日」一致。
function monthStartStamp() {
  const now = new Date();
  return stamp(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), 1) / 1000);
}
function check(label, name, value) {
  return `<label class="checkbox"><input type="checkbox" name="${name}" ${value ? "checked" : ""}>${label}</label>`;
}
function select(label, name, options, value) {
  return `<label>${label}<select name="${name}">${options.map(([v, l]) => `<option value="${v}" ${v === value ? "selected" : ""}>${l}</option>`).join("")}</select></label>`;
}
// 地区必须与任务的地区字段逐字相同，回程与延迟匹配才成立；手打容易写出
// “上海市”或带尾随空格这类静默失配。候选 = 目录地区 + 已建任务用到的地区
// （含自定义目标）+ 当前节点的值，保证既有数据不会因为换控件而丢失。
async function knownRegions() {
  const found = new Set();
  const add = (value) => {
    const region = String(value ?? "").trim();
    if (region) found.add(region);
  };
  try {
    if (!catalog) catalog = await api("/admin/catalog");
    for (const target of catalog?.targets || []) add(target.region);
  } catch {}
  try {
    const list = tasks.length ? tasks : await api("/admin/tasks");
    for (const task of list)
      for (const target of task.probe?.targets || []) add(target.region);
  } catch {}
  return [...found].sort((a, b) => a.localeCompare(b, "zh-Hans-CN"));
}
// 候选列表在 render() 里预取，弹窗才能同步打开；节点自身的历史值再并进来，
// 保证目录外或已废弃的地区不会因为换控件而被清掉。海外地区由 regions.js 统一补上。
function nodeRegions(values) {
  return regionOptions(regions, values);
}
// 价格周期与货币都固定成选项（2026-10-10 用户要求：周期固定「周、月、季、半年、年、
// 两年、三年」，货币固定成常见几种）。落库的仍是原来那个自由文本字段（服务端只限长度），
// 选项表里没有的现值——例如没填过货币的空值——会作为一条额外选项追加在末尾，编辑不会被
// 静默改掉。「免费」不是周期，是单独的 free 勾选（数据库 v10 已把旧的「免费」周期转过去）。
const PRICE_PERIODS = ["周", "月", "季", "半年", "年", "两年", "三年"];
// 货币的显示一直是 ISO 代码（卡片写「CNY 179.25 / 年」），所以选项的 value 也保持代码，
// 只有标签是中文名——换中文当 value 会把既有节点的显示从 CNY 改成人民币，那是另一件事。
const CURRENCIES = [
  ["CNY", "人民币（CNY）"],
  ["USD", "美元（USD）"],
  ["EUR", "欧元（EUR）"],
  ["GBP", "英镑（GBP）"],
  ["JPY", "日元（JPY）"],
  ["CAD", "加拿大币（CAD）"],
  ["HKD", "港币（HKD）"],
  ["SGD", "新加坡币（SGD）"],
  ["TWD", "新台币（TWD）"],
  ["AUD", "澳元（AUD）"],
  ["KRW", "韩元（KRW）"],
  ["RUB", "卢布（RUB）"],
  ["CHF", "瑞士法郎（CHF）"],
];

// 现值不在选项表里就追加一条（标签写现值本身，空值写「未设置」），保证打开表单保存一遍
// 不会把老值换掉。
function withCurrent(options, current, emptyLabel = "未设置") {
  const list = options.map(([value, label]) => [value, label]);
  const value = String(current ?? "");
  if (!list.some(([known]) => known === value)) list.push([value, value || emptyLabel]);
  return list;
}

// 拖动排序的手柄：六点图标。用 pointer 事件拖动（HTML5 drag 在手机上根本不触发），
// 所以按住手柄时靠样式里的 touch-action: none 把页面滚动让开。
const DRAG_ICON =
  '<svg width="12" height="16" viewBox="0 0 12 16" fill="currentColor" aria-hidden="true"><circle cx="3" cy="3" r="1.4"/><circle cx="9" cy="3" r="1.4"/><circle cx="3" cy="8" r="1.4"/><circle cx="9" cy="8" r="1.4"/><circle cx="3" cy="13" r="1.4"/><circle cx="9" cy="13" r="1.4"/></svg>';
function dragHandle(n) {
  return `<button type="button" class="drag-handle" data-drag="${n.id}" title="按住拖动调整顺序" aria-label="拖动排序 ${esc(n.name)}">${DRAG_ICON}</button>`;
}
function nodeForm(existing) {
  const n = existing || structuredClone(defaults);
  // 新节点默认排在最后：默认值 0 会让它插到所有已排好的节点前面。
  if (!existing) n.order = Math.max(0, ...nodes.map((x) => x.order || 0)) + 1;
  const options = nodeRegions([n.region, n.display.route_region, n.display.latency_region]);
  // 卡片地区只给能对上的候选：内置目录与已建任务用到的地区（服务端保存时同样只认这些），
  // 外加节点现有的值。海外常用地区只留给「节点所在地区」，那一项不参与匹配。
  const cardOptions = [...new Set([...regions, n.display.route_region, n.display.latency_region].filter(Boolean))].sort((a, b) => a.localeCompare(b, "zh-Hans-CN"));
  // 免费机器存的周期是空值；取消勾选时周期从「月」起，不留一个「未设置」选项。
  const period = n.billing.free ? "月" : n.billing.price_period || "";
  const carriers = (prefix, list, chosen) =>
    `<div class="option-tiles">${list.map((carrier) => check(carrier, prefix + carrier, !chosen?.length || chosen.includes(carrier))).join("")}</div>`;
  // 四段对应节点配置的分组：基本信息、公开卡片（display）、流量与计费（billing）、线路与降速（line）。
  const d = modal(
    existing ? "编辑节点" : "添加节点",
    `<form id="node-form">
<div class="form-section"><h3>基本信息</h3><div class="form-grid">${field("节点名称", "name", n.name, "text", 'required maxlength="100"')}${field("分组", "group", n.group)}${regionCombobox("节点所在地区", "region", n.region, options)}${check("在公开页面展示", "public", n.public)}</div></div>
<div class="form-section"><h3>公开卡片</h3><p class="hint">卡片按这里的地区挑选探测结果，地区要和任务目标的地区一致才对得上。回程标签按实际经过的骨干命名（163 / CN2GIA / CN2GT / 9929 等），IPv4 与 IPv6 分开检测。</p><div class="form-grid">${regionCombobox("回程地区", "representative", n.display.route_region, cardOptions, { placeholder: "不指定（按任务地区匹配）" })}${regionCombobox("延迟地区（留空沿用回程地区）", "latency_region", n.display.latency_region, cardOptions, { placeholder: "沿用回程地区" })}</div><div class="option-groups card-carriers"><div class="option-group"><span class="option-label">显示延迟的运营商</span>${carriers("latency_carrier:", ["电信", "联通", "移动", "其他"], n.display.latency_carriers)}</div><div class="option-group"><span class="option-label">显示回程的运营商</span>${carriers("route_carrier:", ["电信", "联通", "移动"], n.display.route_carriers)}</div></div><p class="hint">只在这台机器实际有探测的运营商里筛选，每组至少勾一项；回程只有三网。</p></div>
<div class="form-section"><h3>流量与计费</h3><div class="form-grid">${field("流量额度（GB，0 为不设上限）", "quota", n.billing.traffic_limit / 1e9, "number", 'min="0" step="any"')}${select(
      "流量计费方式",
      "traffic_mode",
      [
        ["sum", "上传 + 下载"],
        ["max", "上传 / 下载取较大值"],
        ["in", "仅下载"],
        ["out", "仅上传"],
      ],
      n.billing.traffic_mode,
    )}${field("每月重置时间（UTC，精确到秒）", "billing_at", stamp(n.billing.reset_at) || monthStartStamp(), "text", 'required placeholder="例如 2026-09-01 00:00:00" pattern="\\d{4}-\\d{2}-\\d{2}[ T]\\d{2}:\\d{2}(:\\d{2})?"')}${field("到期时间（UTC，精确到秒）", "expires_at", stamp(n.billing.expires_at), "text", 'placeholder="留空表示不设，例如 2027-05-28 00:00:00" pattern="\\d{4}-\\d{2}-\\d{2}[ T]\\d{2}:\\d{2}(:\\d{2})?"')}</div><div class="form-grid price-grid">${check("免费机器（不计价）", "free", n.billing.free)}${field("价格", "price", n.billing.price, "number", 'min="0" step="any"')}${select("货币", "currency", withCurrent(CURRENCIES, n.billing.currency), n.billing.currency || "")}${select("价格周期", "price_period", withCurrent(PRICE_PERIODS.map((period) => [period, period]), period), period)}</div><p class="hint">重置时间按「日 + 时分秒」逐月顺延，当月没有该日时取最后一天；到期时间用于续费提醒。首次接入时建立流量基线，不补算历史用量。</p></div>
<div class="form-section"><h3>线路与降速</h3><div class="form-grid">${field("线路带宽（Mbit/s，0 为不标注）", "line_mbps", n.line.mbps, "number", 'min="0" max="1000000" placeholder="例如 1000"')}${select("降速触发条件", "throttle_on", [["bandwidth", "窗口平均带宽达到降速阈值"], ["quota", "本周期额度用满后限速"]], n.line.throttle_on || "bandwidth")}${field("降速阈值 / 用满后限速（Mbit/s，0 为不设）", "throttle_mbps", n.line.throttle_mbps, "number", 'min="0" max="1000000" placeholder="例如 100"')}${field("降速统计窗口（小时，默认 24）", "throttle_hours", n.line.throttle_hours || 24, "number", 'min="1" max="168" placeholder="默认 24"')}</div><p class="hint">降速阈值填 0 表示这条线路没有降速规则；按额度降速时不用统计窗口。</p><details class="more"><summary>降速规则说明</summary><p class="hint"><b>窗口平均带宽</b>：按 Agent 上报的网卡计数，取统计窗口内的平均速率，达到阈值即视为降速（常见口径：近 24 小时平均超过 100 Mbit/s 降到 100 Mbit/s），口径与本节点的流量计费方式一致。<b>额度用满后限速</b>：本周期用量达到额度即视为已降速，适合「用满后不计量但限速」的套餐，这时「线路带宽」填突发峰值。主机重启或网卡变化时会自动重建流量基线。</p></details></div>
<div class="form-section"><h3>提醒规则</h3><p class="hint">${existing ? `当前：${esc(alertSummary(n.alerts))}。` : "新节点默认不开任何提醒。"}提醒规则在「告警与通知 → 提醒规则」里设置，可以多台一起改。</p>${existing ? '<button type="button" id="goto-alerts">去设置提醒规则 →</button>' : ""}</div><div class="form-footer"><button type="submit" class="primary">保存节点</button></div></form>`,
  );
  // 规则未勾选时禁用它的阈值输入：既表明该值当前不生效，也让浏览器跳过它的 min 校验。
  const form = d.querySelector("form"),
    el = (name) => form.elements.namedItem(name);
  wireComboboxes(form);
  // 运营商筛选至少留一项：接口里空列表的意思是「不筛选、全部显示」，全部取消勾选存下去
  // 就成了全显示，正好和勾选框表达的相反。
  for (const prefix of ["latency_carrier:", "route_carrier:"]) {
    const boxes = [...form.querySelectorAll(`input[name^="${prefix}"]`)];
    const sync = () => boxes[0].setCustomValidity(boxes.some((b) => b.checked) ? "" : "至少勾选一个运营商");
    boxes.forEach((b) => b.addEventListener("change", sync));
    sync();
  }
  // 「去设置提醒规则」会关掉这张表单：改过东西时先在按钮上说清楚会丢掉修改，再点一次才走。
  // 弹窗是单例，没法在上面再叠一个确认框。
  let dirty = false;
  form.addEventListener("input", () => (dirty = true));
  form.addEventListener("change", () => (dirty = true));
  const goto = d.querySelector("#goto-alerts");
  if (goto)
    goto.onclick = guard(async () => {
      if (dirty && !goto.dataset.armed) {
        goto.dataset.armed = "1";
        goto.classList.add("danger");
        goto.textContent = "放弃未保存的修改并前往 →";
        return;
      }
      d.close();
      tab = "alerts";
      alertView = "rules";
      await render();
    });
  // 勾选免费时价格、货币与周期都不生效，一并禁用；保存时服务端也会把价格与周期清掉。
  const syncFree = () => {
    for (const name of ["price", "currency", "price_period"]) el(name).disabled = el("free").checked;
  };
  el("free").onchange = syncFree;
  syncFree();
  // 按额度降速时统计窗口不参与判断，同样禁用（值原样保留，切回窗口模式时还在）。
  const syncThrottle = () => (el("throttle_hours").disabled = el("throttle_on").value === "quota");
  el("throttle_on").addEventListener("change", syncThrottle);
  syncThrottle();
  // 重置 / 到期时间按 UTC 填写（服务端按 UTC 逐月顺延），公开面板却按访客本地时间显示。
  // 在输入框下面实时换算一行本地时间，免得填的人自己心算时差。
  for (const name of ["billing_at", "expires_at"]) {
    const input = el(name);
    const note = document.createElement("small");
    note.className = "local-time";
    input.after(note);
    const update = () => {
      let text = "";
      try {
        if (input.value.trim()) text = "本地时间 " + date(stampISO(input.value));
      } catch {
        text = "";
      }
      note.textContent = text;
    };
    input.addEventListener("input", update);
    update();
  }
  form.onsubmit = guard(async (e) => {
    e.preventDefault();
    const f = new FormData(e.target),
      get = (k) => String(f.get(k) || ""),
      // 从元素读取而非 FormData：禁用的阈值不出现在 FormData 里，
      // 但取消勾选不应清掉已设好的数值。
      num = (k) => Number(el(k).value);
    // 节点配置按关注点分组：卡片显示（display）、计费（billing）、线路（line）、提醒（alerts）。
    const value = {
      name: get("name"),
      group: get("group"),
      region: get("region"),
      // 顺序只由列表拖动决定（POST /admin/nodes/order），表单原样带回。
      order: n.order,
      public: f.has("public"),
      display: {
        route_region: get("representative"),
        latency_region: get("latency_region"),
        latency_carriers: ["电信", "联通", "移动", "其他"].filter((carrier) => f.has("latency_carrier:" + carrier)),
        route_carriers: ["电信", "联通", "移动"].filter((carrier) => f.has("route_carrier:" + carrier)),
      },
      billing: {
        reset_at: stampISO(get("billing_at")),
        traffic_limit: Math.round(num("quota") * 1e9),
        traffic_mode: get("traffic_mode"),
        free: f.has("free"),
        price: f.has("free") ? 0 : num("price"),
        // 货币从元素读：免费时它被禁用、不在 FormData 里，但不该因此清掉。
        currency: el("currency").value,
        price_period: f.has("free") ? "" : get("price_period"),
        expires_at: get("expires_at") ? stampISO(get("expires_at")) : "",
      },
      line: {
        mbps: Math.round(num("line_mbps") || 0),
        throttle_mbps: Math.round(num("throttle_mbps") || 0),
        throttle_hours: Math.round(num("throttle_hours") || 0),
        throttle_on: get("throttle_on"),
      },
      // 提醒规则不在这张表单里改（见告警页的规则总览），原样带回。
      alerts: n.alerts,
    };
    const result = await send(
      "/admin/nodes" + (existing ? "/" + existing.id : ""),
      existing ? "PUT" : "POST",
      value,
    );
    d.close();
    await render();
    // 新建返回一次性密钥；编辑（PUT）成功是 204，没有正文。
    if (result?.token) showToken(result.token);
    else toast("节点已保存");
  });
}
// 确认框写明对象：连着处理几台机器时，只写「删除节点」很容易点错行。
const nodeName = (id) => nodes.find((n) => n.id === id)?.name || id;
const taskName = (id) => tasks.find((t) => t.id === id)?.name || id;
function confirmAction(title, message, action) {
  const d = modal(
    title,
    `<p>${esc(message)}</p><div class="form-footer"><button class="danger" id="confirm">确认</button></div>`,
  );
  d.querySelector("#confirm").onclick = guard(async () => {
    d.close();
    await action();
  });
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
// 行内只留最常用的两个动作，其余收进「更多」：一行七个链接在窄一点的屏幕上就折成两行。
// 菜单展开时挪到 <body> 下再 fixed 定位：表格外层的 .table-scroll 会裁掉浮层，带
// backdrop-filter 的面板又会让 fixed 变成相对面板定位。收起时挪回原处。
function moreMenu(items) {
  return `<details class="row-more more"><summary>更多</summary><div class="row-menu" role="menu">${items}</div></details>`;
}
function wireMoreMenus(root) {
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
function renderTasks(health = {}) {
  content().innerHTML = `<section class="page-heading"><div><div class="eyebrow">NETWORK OBSERVABILITY</div><h1>网络探测<span>${tasks.length}</span></h1><p>每个任务独立配置目标、协议、频率和参与节点。</p></div><div class="heading-actions"><button id="bind-selected">批量绑定节点</button><button id="add-task" class="primary">＋ 创建任务</button></div></section><section class="panel"><div class="table-scroll"><table class="stack"><thead><tr><th class="select-col"><label class="checkbox select-all"><input id="select-tasks" type="checkbox" aria-label="选择全部任务"><span>全选</span></label></th><th>任务</th><th>状态</th><th>目标</th><th>地区 / 运营商</th><th>节点</th><th>最近执行</th><th>操作</th></tr></thead><tbody>${tasks.map((t) => { const s = taskSummary(t); const bound = t.node_ids?.filter((id) => nodes.some((n) => n.id === id)).length || 0; return `<tr><td data-label="选择"><input type="checkbox" data-select-task="${t.id}" aria-label="选择 ${esc(t.name)}"></td><td data-label="任务">${esc(t.name)}<small>${s.unlock ? "解锁检测 · " : ""}间隔 ${esc(s.intervals)}</small></td><td data-label="状态"><span class="pill ${t.enabled ? "good" : ""}">${t.enabled ? "已启用" : "已停用"}</span></td><td data-label="目标">${s.unlock ? "内置目录" : s.count + " 个地址"}<small>${esc(s.families)}</small></td><td data-label="地区 / 运营商" title="${esc(s.targetsFull)}">${esc(s.targets)}<small>${s.regions > 3 ? `共 ${s.regions} 个地区` : ""}</small></td><td data-label="节点">${bound} 个</td><td data-label="最近执行">${healthCell(t, health[t.id])}</td><td data-label="操作"><div class="row-actions"><button data-edit-task="${t.id}">编辑</button><button data-control="${t.enabled ? "pause" : "resume"}" data-task="${t.id}">${t.enabled ? "暂停" : "恢复"}</button>${moreMenu(`<button data-control="run" data-task="${t.id}" ${!t.enabled || !bound ? "disabled" : ""}>立即执行</button><button data-runs-task="${t.id}">执行记录</button><button data-copy-task="${t.id}">复制</button><button class="menu-danger" data-delete-task="${t.id}">删除</button>`)}</div></td></tr>`; }).join("") || '<tr><td colspan="8" class="empty-cell">暂无任务。可从内置三网目标目录选择，也可填写自定义目标。</td></tr>'}</tbody></table></div></section>`;
  wireMoreMenus(content());
  content().querySelector("#select-tasks").onchange = (e) => content().querySelectorAll("[data-select-task]").forEach((b) => b.checked = e.target.checked);
  content().querySelector("#bind-selected").onclick = () => {
    const ids = new Set([...content().querySelectorAll("[data-select-task]:checked")].map((b) => b.dataset.selectTask));
    if (!ids.size) { toast("请先勾选任务"); return; }
    bindTasks(tasks.filter((t) => ids.has(t.id)), nodes, render);
  };
  content().querySelectorAll("[data-control]").forEach((b) => b.onclick = guard(async () => {
    b.disabled = true;
    try { await controlTask(b.dataset.task, b.dataset.control, render); } finally { b.disabled = false; }
  }));
  content().querySelectorAll("[data-runs-task]").forEach((b) => b.onclick = guard(() => taskRuns(tasks.find((t) => t.id === b.dataset.runsTask))));
  content().querySelectorAll("[data-copy-task]").forEach((b) => b.onclick = guard(async () => {
    const copy = structuredClone(tasks.find((t) => t.id === b.dataset.copyTask));
    delete copy.id; copy.name = copy.name.slice(0, 20) + " 副本"; copy.enabled = false;
    await taskForm(copy);
  }));
  content().querySelector("#add-task").onclick = guard(() => taskForm());
  content()
    .querySelectorAll("[data-edit-task]")
    .forEach(
      (b) =>
        (b.onclick = guard(() =>
          taskForm(tasks.find((t) => t.id === b.dataset.editTask)),
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
          )),
    );
}
async function taskForm(existing) {
  if (!catalog) catalog = await api("/admin/catalog");
  taskEditor(existing, catalog, nodes, render);
}

// 提醒规则：告警页的规则总览与它的编辑弹窗共用这一张表（单台编辑与批量设置是同一张表单）。
// 每种规则一行：勾选即开启，参数是它自己的阈值 / 持续时间，外加它自己的重复提醒间隔。
// 接口里规则是列表（{kind, threshold, hold_seconds, repeat_seconds}），列出的就是开着的。
const RULE_SPECS = [
  {
    kind: "offline", label: "离线提醒", column: "离线",
    fields: [{ key: "hold_seconds", name: "offline_seconds", label: "离线后额外等待秒数（默认 60）", def: 60, attrs: 'min="0" max="86400"' }],
    cell: (r) => `等 ${r.hold_seconds || 0} 秒`,
  },
  {
    kind: "renewal", label: "续费提醒", column: "续费",
    fields: [{ key: "threshold", name: "renewal_days", label: "提前多少天提醒续费（默认 7）", def: 7, attrs: 'min="0" max="365" step="1"' }],
    cell: (r) => `提前 ${r.threshold || 0} 天`,
  },
  {
    kind: "traffic", label: "流量警告", column: "流量",
    fields: [{ key: "threshold", name: "traffic_percent", label: "流量达到额度百分比（默认 80）", def: 80, attrs: 'min="1" max="100"' }],
    cell: (r) => `${r.threshold}%`,
  },
  {
    kind: "throttle", label: "达量降速警告", column: "降速", fields: [],
    hint: "按节点编辑里的降速规则触发时提醒：窗口均值达到降速阈值，或本周期额度用满（节点需先填降速速率）。",
    cell: () => "开",
  },
  {
    kind: "cpu", label: "CPU 警告", column: "CPU",
    fields: [
      { key: "threshold", name: "cpu_percent", label: "CPU 阈值 %（默认 90）", def: 90, attrs: 'min="1" max="100"' },
      { key: "hold_seconds", name: "cpu_hold", label: "持续秒数（默认 300）", def: 300, attrs: 'min="30" max="86400"' },
    ],
    cell: (r) => `${r.threshold}% · ${r.hold_seconds} 秒`,
  },
  {
    kind: "memory", label: "内存警告", column: "内存",
    fields: [
      { key: "threshold", name: "memory_percent", label: "内存阈值 %（默认 90）", def: 90, attrs: 'min="1" max="100"' },
      { key: "hold_seconds", name: "memory_hold", label: "持续秒数（默认 300）", def: 300, attrs: 'min="30" max="86400"' },
    ],
    cell: (r) => `${r.threshold}% · ${r.hold_seconds} 秒`,
  },
];
const ruleOf = (list, kind) => (list || []).find((r) => r.kind === kind);
function alertFields(list) {
  const rows = RULE_SPECS.map((spec) => {
    const rule = ruleOf(list, spec.kind);
    // 开着的规则原样回填（接口省略 0，没写就是 0）；关着的回填默认值，勾上就能直接存。
    const value = (f) => (rule ? rule[f.key] ?? 0 : f.def);
    const inputs = spec.fields
      .map((f) => field(f.label, f.name, value(f), "number", `${f.attrs} placeholder="默认 ${f.def}"`))
      .join("");
    const repeat = field("重复提醒间隔（分钟，0 为只提醒一次）", spec.kind + "_repeat", rule ? (rule.repeat_seconds || 0) / 60 : 0, "number", 'min="0" max="10080" step="5" placeholder="默认 0（只提醒一次）"');
    return `<div class="rule-row">${check(spec.label, spec.kind, !!rule)}${inputs}${repeat}${spec.hint ? `<small class="hint">${spec.hint}</small>` : ""}</div>`;
  }).join("");
  return `<div class="form-section"><div class="form-grid rules-grid">${rows}</div><p class="hint">阈值默认值：离线等待 60 秒、续费提前 7 天、流量 80%、CPU / 内存 90% 并持续 300 秒、重复间隔 0（只提醒一次）。未勾选的规则不生效；节点 45 秒未上报即判定离线，恢复时通知一次；重复间隔至少 5 分钟，每条规则各自设置。</p></div>`;
}
// 规则未勾选时禁用它的输入：既表明这些值当前不生效，也让浏览器跳过它们的 min 校验。
function wireAlerts(form) {
  const el = (name) => form.elements.namedItem(name);
  const sync = () => {
    for (const spec of RULE_SPECS)
      [...spec.fields.map((f) => f.name), spec.kind + "_repeat"].forEach((name) => (el(name).disabled = !el(spec.kind).checked));
  };
  RULE_SPECS.forEach((spec) => (el(spec.kind).onchange = sync));
  sync();
}
function readAlerts(form) {
  const el = (name) => form.elements.namedItem(name);
  return RULE_SPECS.filter((spec) => el(spec.kind).checked).map((spec) => {
    const rule = { kind: spec.kind };
    for (const f of spec.fields) rule[f.key] = Number(el(f.name).value);
    rule.repeat_seconds = Math.round(Number(el(spec.kind + "_repeat").value) * 60);
    return rule;
  });
}
// 规则总览每格的文字：开着写参数（有重复间隔就附上），关着写「—」。
function alertCells(list) {
  const out = {};
  for (const spec of RULE_SPECS) {
    const rule = ruleOf(list, spec.kind);
    out[spec.kind] = rule ? spec.cell(rule) + (rule.repeat_seconds ? ` · 每 ${rule.repeat_seconds / 60} 分钟` : "") : "—";
  }
  return out;
}
function alertSummary(list) {
  const c = alertCells(list);
  const on = RULE_SPECS.filter((spec) => c[spec.kind] !== "—").map((spec) => `${spec.column} ${c[spec.kind]}`);
  return on.length ? on.join("，") : "未开启任何提醒";
}
// 编辑一台或一批节点的提醒规则：整份替换所选节点的规则列表，服务端一个事务里改完。
function alertsEditor(targets, refresh) {
  const one = targets.length === 1;
  const list = one ? targets[0].alerts : [];
  const d = modal(
    one ? `提醒规则 · ${targets[0].name}` : `批量设置提醒规则（${targets.length} 个节点）`,
    `<form id="alerts-form">${one ? "" : `<p class="hint">保存后，所选 ${targets.length} 个节点的提醒规则都会换成下面这一份：${esc(targets.map((n) => n.name).join("、"))}。</p>`}${alertFields(list)}<div class="form-footer"><button type="submit" class="primary">保存规则</button></div></form>`,
  );
  const form = d.querySelector("form");
  wireAlerts(form);
  form.onsubmit = guard(async (e) => {
    e.preventDefault();
    await send("/admin/nodes/alerts", "PUT", { node_ids: targets.map((n) => n.id), alerts: readAlerts(form) });
    d.close();
    await refresh();
    toast(one ? "提醒规则已保存" : `已更新 ${targets.length} 个节点的提醒规则`);
  });
}

// 事件保存 90 天，接口按 id 倒序分页（每页 100 条）：筛选在服务端做，「加载更多」把上一页
// 最后一条的 id 作为 before 续读。筛选条件跨刷新保留，换条件就从第一页重来。
let eventFilter = { node_id: "", kind: "" };
const EVENT_PAGE = 100;
function eventsURL(before = 0) {
  const q = new URLSearchParams({ limit: String(EVENT_PAGE) });
  if (eventFilter.node_id) q.set("node_id", eventFilter.node_id);
  if (eventFilter.kind) q.set("kind", eventFilter.kind);
  if (before) q.set("before", String(before));
  return "/admin/events?" + q;
}
function eventRows(events, configured) {
  return events.map((e) => `<tr><td data-label="时间">${date(e.at)}</td><td data-label="节点">${esc(e.node_name)}</td><td data-label="类型">${esc(kindName[e.kind] || e.kind)}</td><td data-label="状态"><span class="pill ${e.active ? "warn" : "good"}">${e.active ? "触发" : "解除"}</span></td><td data-label="内容">${esc(e.message)}</td><td data-label="通知">${e.delivered ? "已送达" : e.attempts >= 10 ? "发送失败" : Date.now() / 1000 - e.at > 86400 ? "超过 24 小时，不再发送" : configured ? "待发送 / 重试" : "未配置出口"}${e.attempts > 0 ? `<small>${e.attempts} 次尝试</small>` : ""}${e.last_error ? `<small>${esc(e.last_error)}</small>` : ""}</td></tr>`).join("");
}
// 规则总览：每台节点一行，每条规则一格；勾选若干行可以批量套同一份规则。
function rulesPanel() {
  const rows = nodes
    .map((n) => {
      const c = alertCells(n.alerts);
      const cells = RULE_SPECS.map((spec) => `<td data-label="${spec.column}">${c[spec.kind]}</td>`).join("");
      return `<tr><td data-label="选择"><input type="checkbox" data-select-node="${esc(n.id)}" aria-label="选择 ${esc(n.name)}"></td><td data-label="节点">${esc(n.name)}<small>${esc(n.group || "默认")}</small></td>${cells}<td data-label="操作"><div class="row-actions"><button data-alerts="${esc(n.id)}">编辑规则</button></div></td></tr>`;
    })
    .join("");
  const heads = RULE_SPECS.map((spec) => `<th>${spec.column}</th>`).join("");
  const span = RULE_SPECS.length + 3;
  return `<section class="panel"><div class="panel-head"><h3>提醒规则</h3><button id="bulk-alerts">批量设置</button></div><div class="table-scroll"><table class="stack"><thead><tr><th class="select-col"><label class="checkbox select-all"><input id="select-nodes" type="checkbox" aria-label="选择全部节点"><span>全选</span></label></th><th>节点</th>${heads}<th>操作</th></tr></thead><tbody>${rows || `<tr><td colspan="${span}" class="empty-cell">还没有节点。</td></tr>`}</tbody></table></div></section>`;
}
function wireRulesPanel() {
  const refresh = async () => {
    nodes = await api("/admin/nodes");
    await renderAlerts();
  };
  content().querySelector("#select-nodes").onchange = (e) =>
    content().querySelectorAll("[data-select-node]").forEach((b) => (b.checked = e.target.checked));
  content().querySelector("#bulk-alerts").onclick = () => {
    const ids = new Set([...content().querySelectorAll("[data-select-node]:checked")].map((b) => b.dataset.selectNode));
    if (!ids.size) {
      toast("请先勾选节点");
      return;
    }
    alertsEditor(nodes.filter((n) => ids.has(n.id)), refresh);
  };
  content().querySelectorAll("[data-alerts]").forEach(
    (b) => (b.onclick = () => alertsEditor([nodes.find((n) => n.id === b.dataset.alerts)], refresh)),
  );
}
// 告警页分三个子页：当前告警（正在告警的项目，进来先看这个）、事件记录（90 天流水）、
// 提醒规则（通知渠道 + 每台节点的规则）。以前三块摞在一页里，正在告警的项目淹没在流水里。
let alertView = "active";
const ALERT_VIEWS = [
  ["active", "当前告警"],
  ["events", "事件记录"],
  ["rules", "提醒规则"],
];
function eventTable(rows, empty) {
  return `<div class="table-scroll"><table class="stack"><thead><tr><th>时间</th><th>节点</th><th>类型</th><th>状态</th><th>内容</th><th>通知</th></tr></thead><tbody id="event-rows">${rows || `<tr><td colspan="6" class="empty-cell">${empty}</td></tr>`}</tbody></table></div>`;
}
async function renderAlerts() {
  const [settings, firing] = await Promise.all([
    api("/admin/notification"),
    api("/admin/events?active=1&limit=500"),
  ]);
  const configured = settings.channel !== "none";
  const channelName = settings.telegram_configured
    ? "Telegram 机器人"
    : settings.webhook_configured
      ? "Webhook"
      : "尚未配置";
  const views = ALERT_VIEWS.map(([id, label]) => `<button type="button" data-alert-view="${id}" class="${alertView === id ? "active" : ""}" aria-pressed="${alertView === id}">${label}${id === "active" && firing.length ? ` <b class="count-badge">${firing.length}</b>` : ""}</button>`).join("");
  const heading = `<section class="page-heading"><div><div class="eyebrow">ALERTS & DELIVERY</div><h1>告警与通知</h1><p>正在告警的项目、事件记录、通知渠道与每台节点的提醒规则。</p></div><button id="refresh-events" class="quiet">↻ 刷新</button></section><div class="segmented alert-views" role="group" aria-label="告警页内容">${views}</div>`;
  let body = "";
  let events = [];
  if (alertView === "active") {
    // 没配通知渠道时在这里也提一句：正在告警却没人收到，比没有告警更该知道。
    const warn = configured ? "" : `<div class="notice"><strong>通知渠道尚未配置</strong><span>下面的告警不会发到任何地方。到「提醒规则」页配置 Telegram 或 Webhook。</span></div>`;
    body = `${warn}<section class="panel"><div class="panel-head"><h3>正在告警</h3><span class="muted">每项显示最近一次触发或重复提醒</span></div>${eventTable(eventRows(firing, configured), "当前没有正在告警的项目。")}</section>`;
  } else if (alertView === "events") {
    events = await api(eventsURL());
    const filtered = eventFilter.node_id || eventFilter.kind;
    const nodeOptions = withCurrent(nodes.map((n) => [n.id, n.name]), eventFilter.node_id, "全部节点");
    const kindOptions = [["", "全部类型"], ...Object.entries(kindName).filter(([k]) => k !== "test")];
    body = `<section class="panel"><div class="panel-head"><h3>事件记录</h3><span class="muted">保存 90 天 · 每次加载 ${EVENT_PAGE} 条</span></div><div class="catalog-filters event-filters"><label>节点<select id="event-node"><option value="">全部节点</option>${nodeOptions.filter(([v]) => v).map(([v, l]) => `<option value="${esc(v)}"${v === eventFilter.node_id ? " selected" : ""}>${esc(l)}</option>`).join("")}</select></label><label>类型<select id="event-kind">${kindOptions.map(([v, l]) => `<option value="${esc(v)}"${v === eventFilter.kind ? " selected" : ""}>${esc(l)}</option>`).join("")}</select></label></div>${eventTable(eventRows(events, configured), filtered ? "没有符合筛选条件的事件。" : "暂无事件。提醒规则在「提醒规则」页开启。")}<div class="catalog-toolbar event-more"><button id="more-events" class="quiet" ${events.length < EVENT_PAGE ? "hidden" : ""}>加载更多</button></div></section>`;
  } else {
    body = `<section class="panel webhook-panel"><div><h3>通知渠道 · ${channelName}</h3><p>支持 Telegram 机器人或 Webhook，发送告警触发与恢复通知。</p><small>投递失败采用退避重试，最多 10 次；超过 24 小时的事件不再补发。接收端按 event.id 去重。</small></div><div class="webhook-actions"><button id="configure-webhook" class="primary">配置出口</button><button id="test-webhook" ${configured ? "" : "disabled"}>发送测试</button></div></section>${rulesPanel()}`;
  }
  content().innerHTML = heading + body;
  content().querySelectorAll("[data-alert-view]").forEach(
    (b) =>
      (b.onclick = guard(async () => {
        alertView = b.dataset.alertView;
        await renderAlerts();
      })),
  );
  content().querySelector("#refresh-events").onclick = guard(async () => {
    nodes = await api("/admin/nodes");
    fleetStatus();
    await renderAlerts();
  });
  if (alertView === "events") wireEvents(events, configured);
  if (alertView === "rules") wireRules(settings, configured);
}
function wireEvents(events, configured) {
  let last = events.at(-1)?.id || 0;
  const more = content().querySelector("#more-events");
  more.onclick = guard(async () => {
    more.disabled = true;
    try {
      const page = await api(eventsURL(last));
      content().querySelector("#event-rows").insertAdjacentHTML("beforeend", eventRows(page, configured));
      last = page.at(-1)?.id || last;
      more.hidden = page.length < EVENT_PAGE;
    } finally {
      more.disabled = false;
    }
  });
  for (const [id, key] of [["#event-node", "node_id"], ["#event-kind", "kind"]])
    content().querySelector(id).onchange = guard(async (e) => {
      eventFilter = { ...eventFilter, [key]: e.target.value };
      await renderAlerts();
    });
}
function wireRules(settings, configured) {
  wireRulesPanel();
  content().querySelector("#test-webhook").onclick = guard(async () => {
    await send("/admin/notification/test", "POST");
    toast("测试通知已送达");
  });
  content().querySelector("#configure-webhook").onclick = () => {
    const d = modal(
      "配置通知渠道",
      `<form id="notification-form">
      <label>通知渠道<select name="channel"><option value="telegram">Telegram 机器人</option><option value="webhook">Webhook</option><option value="none">关闭通知</option></select></label>
      <div id="telegram-fields" class="form-section"><label>Bot Token<input name="bot_token" type="password" autocomplete="new-password" placeholder="${settings.telegram_configured ? "留空保留当前 Token" : "从 @BotFather 获取"}"></label><label>Chat ID<input name="chat_id" value="${esc(settings.chat_id || "")}" placeholder="个人 / 群组数字 ID，或 @频道名" autocomplete="off"></label>
      <p class="hint">在 Telegram 中通过 @BotFather 创建机器人。私聊接收需先向机器人发送 /start；群组需先加入机器人，频道需授予发消息权限。填写目标会话的 Chat ID 后保存，再点击“发送测试”。</p></div>
      <div id="webhook-fields" class="form-section"><label>Webhook 地址<input name="webhook" type="url" placeholder="https://your-notifier.example/webhook" autocomplete="off"></label></div>
      <p class="hint">同一时间使用一个通知渠道。凭据仅保存在服务端，不会回显。重新开启后会继续投递尚未发送的历史事件。</p>
      <div class="form-footer"><button class="primary" type="submit">保存</button></div></form>`,
    );
    const form = d.querySelector("form");
    form.elements.channel.value = configured ? settings.channel : "telegram";
    const updateChannel = () => {
      const telegram = form.elements.channel.value === "telegram";
      const webhook = form.elements.channel.value === "webhook";
      d.querySelector("#telegram-fields").hidden = !telegram;
      d.querySelector("#webhook-fields").hidden = !webhook;
      form.elements.chat_id.required = telegram;
      form.elements.bot_token.required =
        telegram && !settings.telegram_configured;
      form.elements.webhook.required = webhook;
      form.elements.webhook.disabled = !webhook;
    };
    form.elements.channel.onchange = updateChannel;
    updateChannel();
    form.onsubmit = guard(async (e) => {
      e.preventDefault();
      const f = new FormData(form);
      await send("/admin/notification", "PUT", {
        channel: f.get("channel"),
        bot_token:
          f.get("channel") === "telegram" ? f.get("bot_token").trim() : "",
        chat_id: f.get("channel") === "telegram" ? f.get("chat_id").trim() : "",
        webhook: f.get("channel") === "webhook" ? f.get("webhook").trim() : "",
      });
      d.close();
      await renderAlerts();
      toast("通知配置已保存");
    });
  };
}

// 面板设置：公开面板的全站显示项（延迟色带时间窗、访客首屏外观）。以前挂在「告警与通知」页里，
// 但它们跟告警无关，单独一页。两项都走 /admin/panel 保存（见下方注释）。
async function renderSettings() {
  const settings = await api("/admin/panel");
  // 时间范围给几个常用档位（分钟是接口单位，档位只是界面）：
  // 探测结果保留 7 天，所以更长的一律取到保留上限，不另设选项。
  const windows = [
    [60, "1 小时"],
    [360, "6 小时"],
    [1440, "24 小时"],
    [4320, "3 天"],
    [10080, "7 天（上限）"],
  ];
  const windowNow = Number(settings.latency_window_minutes ?? 60);
  const windowOptions = (windows.some(([m]) => m === windowNow) ? windows : [...windows, [windowNow, `${windowNow} 分钟`]])
    .sort((a, b) => a[0] - b[0])
    .map(([m, label]) => `<option value="${m}"${m === windowNow ? " selected" : ""}>${label}</option>`)
    .join("");
  // 后台存的值直接亮在下拉框里；认不出来（未配置 / 手改过库）就显示内置默认。
  const themeNow = THEMES.some((t) => t.id === settings.default_theme) ? settings.default_theme : "auto";
  const skinNow = SKINS.some((s) => s.id === settings.default_skin) ? settings.default_skin : "glass";
  content().innerHTML = `<section class="page-heading"><div><div class="eyebrow">PUBLIC PANEL</div><h1>面板设置</h1><p>公开面板对所有访客生效的显示选项。</p></div></section><section class="panel webhook-panel"><div><h3>延迟色带</h3><p>公开卡片的延迟色带每格一次探测，固定画最近 10 次；早于这个范围的探测不再显示，那几格留空。</p><small>探测正常时色带总是满的，这一项只在探测间隔很长或节点停过一阵时看得出区别。探测结果保留 7 天。</small></div><div class="webhook-actions"><label class="window-field">只显示最近<select id="latency-window">${windowOptions}</select></label><button id="save-window" class="primary">保存</button></div></section><section class="panel webhook-panel"><div><h3>默认外观</h3><p>访客第一次打开公开面板时使用的明暗与风格。</p><small>访客在面板右上角切换过之后，以他自己的选择为准。</small></div><div class="webhook-actions"><label class="window-field">明暗<select id="default-theme">${THEMES.map((t) => `<option value="${t.id}"${t.id === themeNow ? " selected" : ""}>${t.name}</option>`).join("")}</select></label><label class="window-field">风格<select id="default-skin">${SKINS.map((s) => `<option value="${s.id}"${s.id === skinNow ? " selected" : ""}>${s.name}</option>`).join("")}</select></label><button id="save-look" class="primary">保存</button></div></section>`;
  // 面板显示项走 /admin/panel：通知那个出口的语义是「空值即清空」，
  // 用同一个 PUT 保存显示项会顺手把 webhook 或 Bot Token 清掉。
  content().querySelector("#save-window").onclick = guard(async () => {
    const minutes = Number(content().querySelector("#latency-window").value);
    await send("/admin/panel", "PUT", { latency_window_minutes: minutes });
    await renderSettings();
    const label = (content().querySelector("#latency-window").selectedOptions[0]?.textContent || `${minutes} 分钟`).replace(/（.*）/, "");
    toast(`延迟色带只显示最近 ${label}内的探测`);
  });
  // 前台默认外观：访客还没自己选过时的第一屏。保存后即写进 /theme-default.js，
  // 下一次打开（包括别的访客）就是新值；当前这份重渲染一遍让人当场看到。
  content().querySelector("#save-look").onclick = guard(async () => {
    const theme = content().querySelector("#default-theme").value;
    const skin = content().querySelector("#default-skin").value;
    await send("/admin/panel", "PUT", { default_theme: theme, default_skin: skin });
    await renderSettings();
    toast(`前台默认外观：${THEMES.find((t) => t.id === theme).name} · ${SKINS.find((s) => s.id === skin).name}`);
  });
}
