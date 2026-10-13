import { showToken } from "./agent-setup.js";
import { api, send, esc, toast, modal, guard } from "./api.js";
import { bytes, date, days, kindName, stamp, stampISO, statusLabel } from "./format.js";
import { detail } from "./detail.js";
import { regionOptions } from "./regions.js";
import { regionCombobox, wireComboboxes } from "./region-picker.js";
import { alertSummary } from "./admin-alerts.js";
import { state, content, field, check, select, withCurrent, nodeName, confirmAction, render, go } from "./admin-state.js";
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
export async function nodesPage() {
  // 「正在告警」的数字读不到时只少这一格的数，不挡住节点列表。
  const [known, firing] = await Promise.all([knownRegions(), api("/admin/events?active=1&limit=500").catch(() => null)]);
  state.regions = known;
  renderNodes(firing);
}
// 到期时间：已过期标红、7 天内到期标橙（与公开卡片的到期胶囊同一套阈值），扫一眼就知道该续哪台。
function dueCell(expiresAt) {
  if (!expiresAt) return "—";
  const left = days(expiresAt);
  const cls = left < 0 ? "due expired" : left <= 7 ? "due soon" : "due";
  const note = left < 0 ? `<small>已过期 ${-left} 天</small>` : left === 0 ? "<small>今天到期</small>" : left <= 7 ? `<small>剩余 ${left} 天</small>` : "";
  return `<span class="${cls}">${esc(expiresAt.slice(0, 10))}</span>${note}`;
}
// 这台机器正在告警就在状态旁挂一枚橙色胶囊，悬停看是哪几类（详情在告警页的「当前告警」）。
function firingPill(n, firing) {
  const kinds = (firing || []).filter((e) => e.node_id === n.id).map((e) => kindName[e.kind] || e.kind);
  return kinds.length ? ` <span class="pill warn" title="${esc(kinds.join("、"))}">告警 ${kinds.length}</span>` : "";
}
// Agent 版本与服务端不一致：服务端内置的安装包就是它自己的版本，在那台机器上不带 --token
// 重新执行一次安装命令即可升级（命令见「接入 / 重置密钥」弹窗，升级不必换密钥）。
function agentStale(n) {
  const v = n.metrics?.version;
  if (!v || !state.serverVersion || v === state.serverVersion) return "";
  return ` <span class="agent-stale" title="面板版本 ${esc(state.serverVersion)}。在这台机器上不带 --token 重新执行安装命令即可升级，不用换密钥。">需更新</span>`;
}
// 节点绑了探测任务，但没有一项落在卡片的回程 / 延迟地区里：公开卡片只会写「该地区暂无探测任务」，
// 后台原来看不出来。判据与 app.js 的卡片一致——地区留空算全匹配。
function cardRegionMiss(n) {
  const plan = (n.probe_targets || []).filter((t) => t.kind === "tcp" || t.kind === "route");
  if (!plan.length) return "";
  const route = n.display.route_region;
  const latency = n.display.latency_region || route;
  const hit = plan.some((t) => {
    const want = t.kind === "tcp" ? latency : route;
    return !want || t.region === want;
  });
  if (hit) return "";
  const where = [...new Set(plan.map((t) => t.region || "未设地区"))].join("、");
  return ` <span class="region-miss" title="绑定的探测任务目标在「${esc(where)}」，与卡片地区「${esc(latency === route ? route : `${route} / ${latency}`)}」对不上，公开卡片不显示延迟和回程。在「编辑」里改卡片地区，或给任务加上这个地区的目标。">卡片无探测</span>`;
}
function renderNodes(firing) {
  content().innerHTML = `<section class="page-heading"><div><div class="eyebrow">INVENTORY</div><h1>节点管理<span>${state.nodes.length}</span></h1><p>管理接入、展示、计费周期和每台主机的提醒规则。</p></div><button id="add-node" class="primary">＋ 添加节点</button></section><div class="admin-stats"><div><span>在线</span><strong>${state.nodes.filter((n) => n.online).length}</strong></div><div><span>公开展示</span><strong>${state.nodes.filter((n) => n.public).length}</strong></div><div><span>7 天内到期或过期</span><strong>${state.nodes.filter((n) => n.billing.expires_at && days(n.billing.expires_at) <= 7).length}</strong></div><div id="goto-firing" class="stat-link${firing?.length ? " stat-warn" : ""}" role="button" tabindex="0" title="查看当前告警"><span>正在告警</span><strong>${firing ? firing.length : "—"}</strong></div></div><section class="panel"><div class="panel-head"><h3>全部节点</h3><button id="refresh-nodes" class="quiet">↻ 刷新</button></div><div class="table-scroll"><table class="stack"><thead><tr><th class="drag-col" title="按住手柄拖动调整顺序">顺序</th><th>主机</th><th>状态</th><th>分组 / 地区</th><th>周期用量</th><th>到期时间</th><th>操作</th></tr></thead><tbody>${state.nodes.map((n) => `<tr data-node="${n.id}"><td class="drag-cell" data-label="顺序">${dragHandle(n)}</td><td data-label="主机"><button class="text-button" data-inspect="${n.id}">${esc(n.name)}</button><small>${n.public ? "公开" : "私有"} · 回程地区 ${esc(n.display.route_region || "未设置")}${cardRegionMiss(n)}</small></td><td data-label="状态"><span class="pill ${n.online ? "good" : ""}">${statusLabel(n)}</span>${firingPill(n, firing)}<small>Agent ${n.metrics ? [n.metrics.version, n.metrics.arch].filter(Boolean).map(esc).join(" · ") : "未接入"}${agentStale(n)}</small><small>上报 ${n.last_seen ? date(n.last_seen) : "从未"}</small></td><td data-label="分组 / 地区">${esc(n.group || "默认")}<small>${esc(n.region || "—")}</small></td><td data-label="周期用量">${bytes(n.traffic_used)}<small>${n.billing.traffic_limit ? "/ " + bytes(n.billing.traffic_limit) : "未设置额度"}</small></td><td data-label="到期时间">${dueCell(n.billing.expires_at)}</td><td data-label="操作"><div class="row-actions"><button data-edit="${n.id}">编辑</button><button data-token="${n.id}" title="${n.last_seen ? "生成新密钥并给出安装命令，旧密钥立即失效" : "生成密钥并给出安装命令"}">${n.last_seen ? "重置密钥" : "接入"}</button><button data-billing="${n.id}">用量校正</button><button data-delete="${n.id}" class="danger">删除</button></div></td></tr>`).join("") || '<tr><td colspan="7" class="empty-cell">还没有节点。添加后下载接入配置，启动 Agent。</td></tr>'}</tbody></table></div></section>`;
  content().querySelectorAll("[data-billing]").forEach((b) => {
    b.onclick = () => {
      const n = state.nodes.find((x) => x.id === b.dataset.billing);
      const d = modal(`校正本周期用量 · ${n.name}`, `<form><p>将当前周期总用量校正为指定值；后续采集继续累加，下个周期自动失效。</p>${field("实际已用流量（GB）", "used", n.traffic_used / 1e9, "number", 'min="0" max="10000000" step="any" required')}<p class="hint">当前周期：${date(n.cycle_start)} — ${date(n.cycle_end)}。修改计费方向或重置日会清除校正。</p><div class="form-footer"><button class="primary" type="submit">保存校正</button></div></form>`);
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
    await go("alerts", "active");
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
          nodeForm(state.nodes.find((n) => n.id === b.dataset.edit)))),
    );
  content()
    .querySelectorAll("[data-inspect]")
    .forEach((b) => (b.onclick = guard(() => detail(b.dataset.inspect))));
  content()
    .querySelectorAll("[data-token]")
    .forEach(
      (b) =>
        (b.onclick = () =>
          tokenAction(state.nodes.find((n) => n.id === b.dataset.token))),
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
            { label: "删除节点" },
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
// 新节点的默认重置时间：当月 1 日 00:00:00 UTC，与旧默认「每月 1 日」一致。
function monthStartStamp() {
  const now = new Date();
  return stamp(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), 1) / 1000);
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
    if (!state.catalog) state.catalog = await api("/admin/catalog");
    for (const target of state.catalog?.targets || []) add(target.region);
  } catch {}
  try {
    const list = state.tasks.length ? state.tasks : await api("/admin/tasks");
    for (const task of list)
      for (const target of task.probe?.targets || []) add(target.region);
  } catch {}
  return [...found].sort((a, b) => a.localeCompare(b, "zh-Hans-CN"));
}
// 候选列表在 render() 里预取，弹窗才能同步打开；节点自身的历史值再并进来，
// 保证目录外或已废弃的地区不会因为换控件而被清掉。海外地区由 regions.js 统一补上。
function nodeRegions(values) {
  return regionOptions(state.regions, values);
}
// 价格周期与货币都固定成选项：周期是「周、月、季、半年、年、两年、三年」，
// 货币是常见几种。落库的仍是原来那个自由文本字段（服务端只限长度），
// 选项表里没有的现值——例如没填过货币的空值——会作为一条额外选项追加在末尾，编辑不会被
// 静默改掉。「免费」不是周期，是单独的 free 勾选。
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
  if (!existing) n.order = Math.max(0, ...state.nodes.map((x) => x.order || 0)) + 1;
  const options = nodeRegions([n.region, n.display.route_region, n.display.latency_region]);
  // 卡片地区只给能对上的候选：内置目录与已建任务用到的地区（服务端保存时同样只认这些），
  // 外加节点现有的值。海外常用地区只留给「节点所在地区」，那一项不参与匹配。
  const cardOptions = [...new Set([...state.regions, n.display.route_region, n.display.latency_region].filter(Boolean))].sort((a, b) => a.localeCompare(b, "zh-Hans-CN"));
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
      await go("alerts", "rules");
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
    if (result?.token) showToken(result.token, value.name);
    else toast("节点已保存");
  });
}
// 「接入」与「重置密钥」是同一个动作（生成新密钥），但对一台从没上报过的机器不该说「旧 Agent
// 会失效」——那里没有在跑的 Agent，失效的只是创建时给出的那条安装命令。所以按有没有上报过分开说。
function tokenAction(n) {
  const fresh = !n.last_seen;
  confirmAction(
    `${fresh ? "接入" : "重置密钥"} · ${n.name}`,
    fresh
      ? `为「${n.name}」生成新的安装命令。这台机器还没接入过，不影响任何在跑的 Agent；之前给出的安装命令会随之失效。`
      : `「${n.name}」的旧 Agent 密钥会立即失效，生成后请用新的安装命令更新这台机器。`,
    async () => {
      const result = await send(`/admin/nodes/${n.id}/token`, "POST");
      showToken(result.token, n.name);
    },
    fresh ? { label: "生成安装命令", danger: false } : { label: "重置密钥" },
  );
}
