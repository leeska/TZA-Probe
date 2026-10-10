import { controlTask, bindTasks, taskRuns } from "./task-controls.js";
import { themeButton, THEMES } from "./theme.js";
import { skinButton, SKINS } from "./skins.js";
import { showToken } from "./agent-setup.js";
import { taskEditor, taskSummary } from "./task-editor.js";
import { api, send, esc, toast, modal, guard } from "./api.js";
import { bytes, date, days, kindName, stamp, stampISO } from "./format.js";
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
  representative: "",
  latency_region: "",
  latency_carriers: [],
  traffic_limit: 0,
  traffic_mode: "sum",
  billing_at: "",
  price: 0,
  currency: "CNY",
  price_period: "月",
  expires_at: "",
  line_mbps: 0,
  throttle_mbps: 0,
  throttle_hours: 24,
  throttle_on: "bandwidth",
  alerts: {
    offline: false,
    offline_seconds: 60,
    repeat_seconds: 0,
    renewal: false,
    renewal_days: 7,
    traffic: false,
    traffic_percent: 80,
    throttle: false,
    load: false,
    cpu_percent: 90,
    memory_percent: 90,
    hold_seconds: 300,
  },
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
  container.innerHTML = `<div class="admin-layout"><aside class="sidebar">${brandHTML}<div class="sidebar-caption">WORKSPACE</div><nav><button data-tab="nodes"><svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="3" y="3" width="6" height="6" rx="1.5"/><rect x="11" y="3" width="6" height="6" rx="1.5"/><rect x="3" y="11" width="6" height="6" rx="1.5"/><rect x="11" y="11" width="6" height="6" rx="1.5"/></svg><span>节点管理</span></button><button data-tab="tasks"><svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="10" cy="10" r="7"/><circle cx="10" cy="10" r="3"/><circle cx="10" cy="10" r="0.8" fill="currentColor"/></svg><span>网络探测</span></button><button data-tab="alerts"><svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="10" cy="10" r="7"/><polyline points="10 6 10 10 13 12"/></svg><span>告警与通知</span></button></nav><div class="sidebar-bottom"><a href="/">公开面板 ↗</a>${skinButton("sidebar-skin")}${themeButton("sidebar-theme")}<button id="logout">退出登录</button><small>独立运行 · TZA PROBE V2</small></div></aside><main class="admin-main"><header class="admin-top"><span>控制台 <span class="muted">/</span> <b id="breadcrumb"></b></span><span class="pill">● 系统运行中</span></header><div id="admin-content"></div></main></div>`;
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
  container
    .querySelectorAll("[data-tab]")
    .forEach((b) => b.classList.toggle("active", b.dataset.tab === tab));
  container.querySelector("#breadcrumb").textContent = {
    nodes: "节点管理",
    tasks: "网络探测",
    alerts: "告警与通知",
  }[tab];
  nodes = await api("/admin/nodes");
  if (tab === "nodes") {
    regions = await knownRegions();
    renderNodes();
  }
  if (tab === "tasks") {
    tasks = await api("/admin/tasks");
    renderTasks();
  }
  if (tab === "alerts") await renderAlerts();
}
const content = () => container.querySelector("#admin-content");
function renderNodes() {
  content().innerHTML = `<section class="page-heading"><div><div class="eyebrow">INVENTORY</div><h1>节点管理<span>${nodes.length}</span></h1><p>管理接入、展示、计费周期和每台主机的提醒规则。</p></div><button id="add-node" class="primary">＋ 添加节点</button></section><div class="admin-stats"><div><span>在线</span><strong>${nodes.filter((n) => n.online).length}</strong></div><div><span>公开展示</span><strong>${nodes.filter((n) => n.public).length}</strong></div><div><span>7 天内到期或过期</span><strong>${nodes.filter((n) => n.expires_at && days(n.expires_at) <= 7).length}</strong></div></div><section class="panel"><div class="panel-head"><h3>全部节点</h3><button id="refresh-nodes" class="quiet">↻ 刷新</button></div><div class="table-scroll"><table class="stack"><thead><tr><th class="drag-col" title="按住手柄拖动调整顺序">顺序</th><th>主机</th><th>状态</th><th>分组 / 地区</th><th>周期用量</th><th>到期时间</th><th>操作</th></tr></thead><tbody>${nodes.map((n) => `<tr data-node="${n.id}"><td class="drag-cell" data-label="顺序">${dragHandle(n)}</td><td data-label="主机"><button class="text-button" data-inspect="${n.id}">${esc(n.name)}</button><small>${n.public ? "公开" : "私有"} · 代表地区 ${esc(n.representative || "未设置")}</small></td><td data-label="状态"><span class="pill ${n.online ? "good" : ""}">${n.online ? "在线" : "离线"}</span><small>Agent ${n.metrics ? esc(n.metrics.version) + " · " + esc(n.metrics.arch || "") : "未接入"}</small><small>上报 ${n.last_seen ? date(n.last_seen) : "从未"}</small></td><td data-label="分组 / 地区">${esc(n.group || "默认")}<small>${esc(n.region || "—")}</small></td><td data-label="周期用量">${bytes(n.traffic_used)}<small>${n.traffic_limit ? "/ " + bytes(n.traffic_limit) : "未设置额度"}</small></td><td data-label="到期时间">${n.expires_at ? esc(n.expires_at.slice(0, 10)) : "—"}</td><td data-label="操作"><div class="row-actions"><button data-edit="${n.id}">编辑</button><button data-token="${n.id}">接入</button><button data-billing="${n.id}">用量校正</button><button data-delete="${n.id}" class="danger">删除</button></div></td></tr>`).join("") || '<tr><td colspan="7" class="empty-cell">还没有节点。添加后下载接入配置，启动 Agent。</td></tr>'}</tbody></table></div></section>`;
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
            "重新生成节点密钥",
            "旧 Agent 密钥会立即失效。生成后请更新该节点配置。",
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
            "删除节点",
            "删除节点及其历史、绑定和探测结果。此操作无法撤销。",
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
      for (const target of task.targets || []) add(target.region);
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
// 既有数据不必迁移；选项表里没有的现值——例如免费机的「免费」、没填过货币的空值——会作为
// 一条额外选项追加在末尾，编辑不会被静默改掉（「免费」也直接列进周期表，新建免费机时可选）。
const PRICE_PERIODS = ["周", "月", "季", "半年", "年", "两年", "三年", "免费"];
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
  const n = existing || structuredClone(defaults),
    a = n.alerts;
  // 未启用的规则允许存 0（服务端只在启用时要求有效阈值），但 0 不满足输入框的 min，
  // 直接回填会让这个节点再也保存不了。对必须 ≥1 的阈值退回默认值。
  const threshold = (key) => a[key] || defaults.alerts[key];
  const options = nodeRegions([n.region, n.representative, n.latency_region]);
  const d = modal(
    existing ? "编辑节点" : "添加节点",
    `<form id="node-form"><div class="form-section"><h3>基本信息</h3><div class="form-grid">${field("节点名称", "name", n.name, "text", 'required maxlength="100"')}${field("分组", "group", n.group)}${regionCombobox("节点所在地区", "region", n.region, options)}${regionCombobox("卡片代表地区（与回程任务一致）", "representative", n.representative, options, { placeholder: "不指定（按任务地区匹配）" })}${regionCombobox("卡片延迟地区（留空沿用代表地区）", "latency_region", n.latency_region, options, { placeholder: "沿用代表地区" })}<div class="field-wide"><span class="field-label">公开卡片显示延迟</span><div class="checkbox-row">${["电信", "联通", "移动", "其他"].map((carrier) => check(carrier, "latency_carrier:" + carrier, !n.latency_carriers?.length || n.latency_carriers.includes(carrier))).join("")}</div><small class="hint">空配置会显示全部延迟分组。</small></div>${field("排序（小的在前）", "order", n.order, "number")}${check("在公开页面展示", "public", n.public)}</div></div><div class="form-section"><h3>计费与续费</h3><div class="form-grid">${field("流量额度（GB，0 为不设上限）", "quota", n.traffic_limit / 1e9, "number", 'min="0" step="any"')}${select(
      "流量计费方式",
      "traffic_mode",
      [
        ["sum", "上传 + 下载"],
        ["max", "上传 / 下载取较大值"],
        ["in", "仅下载"],
        ["out", "仅上传"],
      ],
      n.traffic_mode,
    )}${field("每月重置时间（UTC，精确到秒）", "billing_at", stamp(n.billing_at) || monthStartStamp(), "text", 'required placeholder="例如 2026-09-01 00:00:00" pattern="\\d{4}-\\d{2}-\\d{2}[ T]\\d{2}:\\d{2}(:\\d{2})?"')}${field("价格", "price", n.price, "number", 'min="0" step="any"')}${select("货币", "currency", withCurrent(CURRENCIES, n.currency), n.currency || "")}${select("价格周期", "price_period", withCurrent(PRICE_PERIODS.map((period) => [period, period]), n.price_period), n.price_period || "")}${field("到期时间（UTC，精确到秒）", "expires_at", stamp(n.expires_at), "text", 'placeholder="留空表示不设，例如 2027-05-28 00:00:00" pattern="\\d{4}-\\d{2}-\\d{2}[ T]\\d{2}:\\d{2}(:\\d{2})?"')}${field("线路带宽（Mbit/s，0 为不标注）", "line_mbps", n.line_mbps, "number", 'min="0" max="1000000" placeholder="例如 1000"')}${select("降速触发条件", "throttle_on", [["bandwidth", "窗口平均带宽达到降速阈值"], ["quota", "本周期额度用满后限速"]], n.throttle_on || "bandwidth")}${field("降速阈值 / 用满后限速（Mbit/s，0 为不设）", "throttle_mbps", n.throttle_mbps, "number", 'min="0" max="1000000" placeholder="例如 100"')}${field("降速统计窗口（小时，默认 24；按额度降速时不用）", "throttle_hours", n.throttle_hours || 24, "number", 'min="1" max="168" placeholder="默认 24"')}</div><p class="hint">重置时间只取这一时刻的「日 + 时分秒」，逐月顺延；当月没有该日时取当月最后一天。到期时间同样精确到秒，续费提醒按它计算。首次接入建立流量基线，不补算历史用量；主机重启或网卡集合变化时自动重建基线。达量降速按线路自己的规则判：<b>窗口平均带宽</b>用 Agent 上报的网卡计数按小时累计取平均（德国商家常见口径：近 24 小时平均超过 100 Mbit/s 降速到 100 Mbit/s），平均口径与本节点的流量统计方式一致；<b>额度用满后限速</b>用本周期用量比额度，用满即视为已降速（DMIT T1 这类「1000 GB 取高值 @ 4 Gbps 突发，用满后不计量但限速到 50/100 Mbit/s」——这时「线路带宽」填突发峰值）。填 0 表示这条线路没有这种规则，面板与告警都不显示。</p></div><div class="form-section"><h3>提醒规则</h3><div class="form-grid rules-grid"><div class="rule-row">${check("离线提醒", "offline", a.offline)}${field("离线后额外等待秒数（默认 60）", "offline_seconds", a.offline_seconds || 0, "number", 'min="0" max="86400" placeholder="默认 60"')}</div><div class="rule-row">${check("续费提醒", "renewal", a.renewal)}${field("提前多少天提醒续费（默认 7）", "renewal_days", a.renewal_days, "number", 'min="0" max="365" placeholder="默认 7"')}</div><div class="rule-row">${check("流量警告", "traffic", a.traffic)}${field("流量达到额度百分比（默认 80）", "traffic_percent", threshold("traffic_percent"), "number", 'min="1" max="100" placeholder="默认 80"')}</div><div class="rule-row">${check("达量降速警告", "throttle", a.throttle)}<small class="hint">按上面的规则触发时提醒：窗口均值达到降速阈值，或本周期额度用满（需先填速率）。</small></div><div class="rule-row">${check("负载警告（CPU 或内存）", "load", a.load)}${field("CPU 阈值 %（默认 90）", "cpu_percent", threshold("cpu_percent"), "number", 'min="1" max="100" placeholder="默认 90"')}${field("内存阈值 %（默认 90）", "memory_percent", threshold("memory_percent"), "number", 'min="1" max="100" placeholder="默认 90"')}${field("负载持续秒数（默认 300）", "hold_seconds", threshold("hold_seconds"), "number", 'min="30" max="86400" placeholder="默认 300"')}</div></div>${field("持续告警重复提醒间隔（分钟，0 为只提醒一次；默认 0）", "repeat_minutes", (a.repeat_seconds || 0) / 60, "number", 'min="0" max="10080" step="5" placeholder="默认 0（只提醒一次）"')}<p class="hint">新建节点的默认值：离线等待 60 秒、续费提前 7 天、流量 80%、CPU/内存 90%、负载持续 300 秒、重复间隔 0（只提醒一次）；未勾选的规则不生效。45 秒未上报判定离线，再按设置等待后通知；恢复通知一次。重复间隔至少 5 分钟。通知出口在“告警与通知”配置。</p></div><div class="form-footer"><button type="submit" class="primary">保存节点</button></div></form>`,
  );
  // 规则未勾选时禁用它的阈值输入：既表明该值当前不生效，也让浏览器跳过它的 min 校验。
  const form = d.querySelector("form"),
    el = (name) => form.elements.namedItem(name);
  wireComboboxes(form);
  const rules = {
    offline: ["offline_seconds"],
    renewal: ["renewal_days"],
    traffic: ["traffic_percent"],
    load: ["cpu_percent", "memory_percent", "hold_seconds"],
  };
  const syncRules = () => {
    for (const [rule, names] of Object.entries(rules))
      names.forEach((name) => (el(name).disabled = !el(rule).checked));
  };
  Object.keys(rules).forEach((rule) => (el(rule).onchange = syncRules));
  syncRules();
  form.onsubmit = guard(async (e) => {
    e.preventDefault();
    const f = new FormData(e.target),
      get = (k) => String(f.get(k) || ""),
      // 从元素读取而非 FormData：禁用的阈值不出现在 FormData 里，
      // 但取消勾选不应清掉已设好的数值。
      num = (k) => Number(el(k).value);
    const value = {
      ...defaults,
      name: get("name"),
      group: get("group"),
      region: get("region"),
      representative: get("representative"),
      latency_region: get("latency_region"),
      latency_carriers: ["电信", "联通", "移动", "其他"].filter((carrier) => f.has("latency_carrier:" + carrier)),
      order: num("order"),
      public: f.has("public"),
      traffic_limit: Math.round(num("quota") * 1e9),
      traffic_mode: get("traffic_mode"),
      billing_at: stampISO(get("billing_at")),
      line_mbps: Math.round(num("line_mbps") || 0),
      throttle_mbps: Math.round(num("throttle_mbps") || 0),
      throttle_hours: Math.round(num("throttle_hours") || 0),
      throttle_on: get("throttle_on"),
      price: num("price"),
      currency: get("currency"),
      price_period: get("price_period"),
      expires_at: get("expires_at") ? stampISO(get("expires_at")) : "",
      alerts: {
        offline: f.has("offline"),
        offline_seconds: num("offline_seconds"),
        repeat_seconds: num("repeat_minutes") * 60,
        renewal: f.has("renewal"),
        renewal_days: num("renewal_days"),
        traffic: f.has("traffic"),
        traffic_percent: num("traffic_percent"),
        throttle: f.has("throttle"),
        load: f.has("load"),
        cpu_percent: num("cpu_percent"),
        memory_percent: num("memory_percent"),
        hold_seconds: num("hold_seconds"),
      },
    };
    const result = await send(
      "/admin/nodes" + (existing ? "/" + existing.id : ""),
      existing ? "PUT" : "POST",
      value,
    );
    d.close();
    await render();
    if (result.token) showToken(result.token);
    else toast("节点已保存");
  });
}
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
function renderTasks() {
  content().innerHTML = `<section class="page-heading"><div><div class="eyebrow">NETWORK OBSERVABILITY</div><h1>网络探测<span>${tasks.length}</span></h1><p>每个任务独立配置目标、协议、频率和参与节点。</p></div><div class="heading-actions"><button id="bind-selected">批量绑定节点</button><button id="add-task" class="primary">＋ 创建任务</button></div></section><div class="notice"><strong>回程标签由实际路径决定</strong><span>卡片使用节点指定的代表地区。标签按实际经过的骨干命名，如 163 / CN2GIA / CN2GT / 9929；IPv4 / IPv6 独立检测；空节点绑定不会执行。</span></div><section class="panel"><div class="table-scroll"><table class="stack"><thead><tr><th><input id="select-tasks" type="checkbox" aria-label="选择全部任务"></th><th>任务</th><th>目标</th><th>地区 / 运营商</th><th>间隔</th><th>节点</th><th>操作</th></tr></thead><tbody>${tasks.map((t) => { const s = taskSummary(t); return `<tr><td data-label="选择"><input type="checkbox" data-select-task="${t.id}" aria-label="选择 ${esc(t.name)}"></td><td data-label="任务">${esc(t.name)}<small>${t.enabled ? "已启用" : "已停用"} · ${esc(s.checks)}</small></td><td data-label="目标">${s.unlock ? "内置目录" : s.count + " 个地址"}<small>${esc(s.families)}</small></td><td data-label="地区 / 运营商" title="${esc(s.targetsFull)}">${esc(s.targets)}<small>${s.regions > 3 ? `共 ${s.regions} 个地区` : ""}</small></td><td data-label="间隔">${esc(s.intervals)}</td><td data-label="节点">${t.node_ids?.filter((id) => nodes.some((n) => n.id === id)).length || 0} 个</td><td data-label="操作"><div class="row-actions"><button data-edit-task="${t.id}">编辑</button><button data-control="${t.enabled ? "pause" : "resume"}" data-task="${t.id}">${t.enabled ? "暂停" : "恢复"}</button><button data-control="run" data-task="${t.id}" ${!t.enabled || !t.node_ids?.length ? "disabled" : ""}>立即执行</button><button data-copy-task="${t.id}">复制</button><button data-bind-task="${t.id}">绑定</button><button data-runs-task="${t.id}">执行记录</button><button class="danger" data-delete-task="${t.id}">删除</button></div></td></tr>`; }).join("") || '<tr><td colspan="7" class="empty-cell">暂无任务。可从内置三网目标目录选择，也可填写自定义目标。</td></tr>'}</tbody></table></div></section>`;
  content().querySelector("#select-tasks").onchange = (e) => content().querySelectorAll("[data-select-task]").forEach((b) => b.checked = e.target.checked);
  content().querySelector("#bind-selected").onclick = () => {
    const ids = new Set([...content().querySelectorAll("[data-select-task]:checked")].map((b) => b.dataset.selectTask));
    if (!ids.size) { toast("请先选择任务"); return; }
    bindTasks(tasks.filter((t) => ids.has(t.id)), nodes, render);
  };
  content().querySelectorAll("[data-control]").forEach((b) => b.onclick = guard(async () => {
    b.disabled = true;
    try { await controlTask(b.dataset.task, b.dataset.control, render); } finally { b.disabled = false; }
  }));
  content().querySelectorAll("[data-bind-task]").forEach((b) => b.onclick = () => bindTasks([tasks.find((t) => t.id === b.dataset.bindTask)], nodes, render));
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
            "删除任务",
            "将同时清除此任务的探测结果。",
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

async function renderAlerts() {
  const [events, settings] = await Promise.all([
    api("/admin/events"),
    api("/admin/settings"),
  ]);
  const configured = settings.channel !== "none";
  const channelName = settings.telegram_configured
    ? "Telegram 机器人"
    : settings.webhook_configured
      ? "Webhook"
      : "尚未配置";
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
  content().innerHTML = `<section class="page-heading"><div><div class="eyebrow">ALERTS & DELIVERY</div><h1>告警与通知</h1><p>按节点配置提醒阈值，在这里管理统一通知出口和事件记录。</p></div><button id="refresh-events" class="quiet">↻ 刷新</button></section><section class="panel webhook-panel"><div><h3>通知渠道 · ${channelName}</h3><p>支持 Telegram 机器人或 Webhook，发送告警触发与恢复通知。</p><small>投递失败采用退避重试，最多 10 次；超过 24 小时的事件不再补发。接收端按 event.id 去重。</small></div><div class="webhook-actions"><button id="configure-webhook" class="primary">配置出口</button><button id="test-webhook" ${configured ? "" : "disabled"}>发送测试</button></div></section><section class="panel webhook-panel"><div><h3>面板显示</h3><p>公开面板的延迟色带每格代表一次探测，这里决定往回取多长的历史。</p><small>探测结果保留 7 天，所以一个月、三个月与「全部」目前都取到 7 天。窗口越大取数越慢：本机 17 台实测 1 小时 0.01 秒、24 小时 0.4 秒、7 天 1.2 秒（色带每组最多 20 格，密采目标 1 小时就满，选大只对稀疏目标有意义）。</small></div><div class="webhook-actions"><label class="window-field">延迟色带时间范围<select id="latency-window">${windowOptions}</select></label><button id="save-window" class="primary">保存</button></div></section><section class="panel webhook-panel"><div><h3>前台默认外观</h3><p>访客第一次打开面板时按这里的明暗与风格渲染；他自己点过右上角的切换之后，就按他选的那份来。</p><small>默认值在样式表之前就已落到 &lt;html&gt; 上，所以首屏不会先闪一下内置默认再跳到这套。</small></div><div class="webhook-actions"><label class="window-field">明暗<select id="default-theme">${THEMES.map((t) => `<option value="${t.id}"${t.id === themeNow ? " selected" : ""}>${t.name}</option>`).join("")}</select></label><label class="window-field">风格<select id="default-skin">${SKINS.map((s) => `<option value="${s.id}"${s.id === skinNow ? " selected" : ""}>${s.name}</option>`).join("")}</select></label><button id="save-look" class="primary">保存</button></div></section><section class="panel"><div class="panel-head"><h3>最近事件</h3><span class="muted">最多 100 条 · 保存 90 天</span></div><div class="table-scroll"><table class="stack"><thead><tr><th>时间</th><th>节点</th><th>类型</th><th>状态</th><th>内容</th><th>通知</th></tr></thead><tbody>${events.map((e) => `<tr><td data-label="时间">${date(e.at)}</td><td data-label="节点">${esc(e.node_name)}</td><td data-label="类型">${esc(kindName[e.kind] || e.kind)}</td><td data-label="状态"><span class="pill ${e.active ? "warn" : "good"}">${e.active ? "触发" : "解除"}</span></td><td data-label="内容">${esc(e.message)}</td><td data-label="通知">${e.delivered ? "已送达" : e.attempts >= 10 ? "发送失败" : Date.now() / 1000 - e.at > 86400 ? "超过 24 小时，不再发送" : configured ? "待发送 / 重试" : "未配置出口"}<small>${e.attempts} 次尝试</small>${e.last_error ? `<small>${esc(e.last_error)}</small>` : ""}</td></tr>`).join("") || '<tr><td colspan="6" class="empty-cell">暂无事件。提醒规则可在节点编辑页单独开启。</td></tr>'}</tbody></table></div></section>`;
  content().querySelector("#refresh-events").onclick = guard(renderAlerts);
  // 面板显示项走 /admin/panel：通知那个出口的语义是「空值即清空」，
  // 用同一个 PUT 保存显示项会顺手把 webhook 或 Bot Token 清掉。
  content().querySelector("#save-window").onclick = guard(async () => {
    const minutes = Number(content().querySelector("#latency-window").value);
    await send("/admin/panel", "PUT", { latency_window_minutes: minutes });
    await renderAlerts();
    toast(`延迟色带时间窗已设为 ${minutes} 分钟`);
  });
  // 前台默认外观：访客还没自己选过时的第一屏。保存后即写进 /theme-default.js，
  // 下一次打开（包括别的访客）就是新值；当前这份重渲染一遍让人当场看到。
  content().querySelector("#save-look").onclick = guard(async () => {
    const theme = content().querySelector("#default-theme").value;
    const skin = content().querySelector("#default-skin").value;
    await send("/admin/panel", "PUT", { default_theme: theme, default_skin: skin });
    await renderAlerts();
    toast(`前台默认外观：${THEMES.find((t) => t.id === theme).name} · ${SKINS.find((s) => s.id === skin).name}`);
  });
  content().querySelector("#test-webhook").onclick = guard(async () => {
    await send("/admin/settings/test", "POST");
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
      await send("/admin/settings", "PUT", {
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
