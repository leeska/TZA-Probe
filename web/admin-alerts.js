import { api, send, esc, toast, modal, guard } from "./api.js";
import { date, kindName } from "./format.js";
import { state, content, fleetStatus, field, check, withCurrent } from "./admin-state.js";
// 提醒规则：告警页的规则总览与它的编辑弹窗共用这一张表（单台编辑与批量设置是同一张表单）。
// 每种规则一行：勾选即开启，参数是它自己的阈值 / 持续时间，外加它自己的重复提醒间隔。
// 接口里规则是列表（{kind, threshold, hold_seconds, repeat_seconds}），列出的就是开着的。
const RULE_SPECS = [
  {
    kind: "offline", label: "离线提醒", column: "离线",
    fields: [{ key: "hold_seconds", col: "hold", name: "offline_seconds", label: "等待秒数（默认 60）", def: 60, attrs: 'min="0" max="86400"' }],
    cell: (r) => `等 ${r.hold_seconds || 0} 秒`,
  },
  {
    kind: "renewal", label: "续费提醒", column: "续费",
    fields: [{ key: "threshold", col: "threshold", name: "renewal_days", label: "提前天数（默认 7）", def: 7, attrs: 'min="0" max="365" step="1"' }],
    cell: (r) => `提前 ${r.threshold || 0} 天`,
  },
  {
    kind: "traffic", label: "流量警告", column: "流量",
    fields: [{ key: "threshold", col: "threshold", name: "traffic_percent", label: "额度百分比（默认 80）", def: 80, attrs: 'min="1" max="100"' }],
    cell: (r) => `${r.threshold}%`,
  },
  {
    kind: "throttle", label: "达量降速警告", column: "降速", fields: [],
    hint: "按节点的降速规则触发（节点需先填降速速率）。",
    cell: () => "开",
  },
  {
    kind: "cpu", label: "CPU 警告", column: "CPU",
    fields: [
      { key: "threshold", col: "threshold", name: "cpu_percent", label: "CPU %（默认 90）", def: 90, attrs: 'min="1" max="100"' },
      { key: "hold_seconds", col: "hold", name: "cpu_hold", label: "持续秒数（默认 300）", def: 300, attrs: 'min="30" max="86400"' },
    ],
    cell: (r) => `${r.threshold}% · ${r.hold_seconds} 秒`,
  },
  {
    kind: "memory", label: "内存警告", column: "内存",
    fields: [
      { key: "threshold", col: "threshold", name: "memory_percent", label: "内存 %（默认 90）", def: 90, attrs: 'min="1" max="100"' },
      { key: "hold_seconds", col: "hold", name: "memory_hold", label: "持续秒数（默认 300）", def: 300, attrs: 'min="30" max="86400"' },
    ],
    cell: (r) => `${r.threshold}% · ${r.hold_seconds} 秒`,
  },
];
const ruleOf = (list, kind) => (list || []).find((r) => r.kind === kind);
// 每条规则一行、固定四列：规则 | 阈值 | 持续 / 等待 | 重复提醒。规则没有的那一列留一个占位，
// 同一种输入在每行都落在同一列——以前按各自的输入个数排，「重复提醒」在每行的位置都不一样。
function alertFields(list) {
  const rows = RULE_SPECS.map((spec) => {
    const rule = ruleOf(list, spec.kind);
    // 开着的规则原样回填（接口省略 0，没写就是 0）；关着的回填默认值，勾上就能直接存。
    const value = (f) => (rule ? rule[f.key] ?? 0 : f.def);
    const slot = (col) => {
      const f = spec.fields.find((x) => x.col === col);
      return f ? field(f.label, f.name, value(f), "number", `${f.attrs} placeholder="默认 ${f.def}"`) : '<span class="rule-empty" aria-hidden="true">—</span>';
    };
    const repeat = field("重复间隔（分钟）", spec.kind + "_repeat", rule ? (rule.repeat_seconds || 0) / 60 : 0, "number", 'min="0" max="10080" step="5" placeholder="默认 0（只提醒一次）"');
    return `<div class="rule-row">${check(spec.label, spec.kind, !!rule)}${slot("threshold")}${slot("hold")}${repeat}${spec.hint ? `<small class="hint rule-hint">${spec.hint}</small>` : ""}</div>`;
  }).join("");
  return `<div class="form-section"><div class="rule-head" aria-hidden="true"><span>规则</span><span>阈值</span><span>持续 / 等待</span><span>重复提醒</span></div><div class="rules-list">${rows}</div><p class="hint">未勾选的规则不生效。节点 45 秒未上报即判定离线，「等待秒数」是判定之后再等多久才提醒；恢复时通知一次。重复间隔填 0 只提醒一次，否则至少 5 分钟。</p></div>`;
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
export function alertSummary(list) {
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
  return events.map((e) => `<tr><td data-label="时间">${date(e.at)}</td><td data-label="节点">${esc(e.node_name)}</td><td data-label="类型">${esc(kindName[e.kind] || e.kind)}</td><td data-label="状态"><span class="pill ${e.active ? "warn" : "good"}">${e.active ? "触发" : "解除"}</span></td><td data-label="内容">${esc(e.message)}</td><td data-label="通知">${e.delivered ? "已送达" : e.attempts >= 10 ? "发送失败" : Date.now() / 1000 - e.at > 86400 ? "超过 24 小时，不再发送" : configured ? "待发送 / 重试" : "未配置渠道"}${e.attempts > 0 ? `<small>${e.attempts} 次尝试</small>` : ""}${e.last_error ? `<small>${esc(e.last_error)}</small>` : ""}</td></tr>`).join("");
}
// 规则总览：每台节点一行，每条规则一格；勾选若干行可以批量套同一份规则。
function rulesPanel() {
  const rows = state.nodes
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
    state.nodes = await api("/admin/nodes");
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
    alertsEditor(state.nodes.filter((n) => ids.has(n.id)), refresh);
  };
  content().querySelectorAll("[data-alerts]").forEach(
    (b) => (b.onclick = () => alertsEditor([state.nodes.find((n) => n.id === b.dataset.alerts)], refresh)),
  );
}
const ALERT_VIEWS = [
  ["active", "当前告警"],
  ["events", "事件记录"],
  ["rules", "提醒规则"],
];
function eventTable(rows, empty) {
  return `<div class="table-scroll"><table class="stack"><thead><tr><th>时间</th><th>节点</th><th>类型</th><th>状态</th><th>内容</th><th>通知</th></tr></thead><tbody id="event-rows">${rows || `<tr><td colspan="6" class="empty-cell">${empty}</td></tr>`}</tbody></table></div>`;
}
export async function renderAlerts() {
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
  const views = ALERT_VIEWS.map(([id, label]) => `<button type="button" data-alert-view="${id}" class="${state.alertView === id ? "active" : ""}" aria-pressed="${state.alertView === id}">${label}${id === "active" && firing.length ? ` <b class="count-badge">${firing.length}</b>` : ""}</button>`).join("");
  const heading = `<section class="page-heading"><div><div class="eyebrow">ALERTS & DELIVERY</div><h1>告警与通知</h1><p>正在告警的项目、事件记录、通知渠道与每台节点的提醒规则。</p></div><button id="refresh-events" class="quiet">↻ 刷新</button></section><div class="segmented alert-views" role="group" aria-label="告警页内容">${views}</div>`;
  let body = "";
  let events = [];
  if (state.alertView === "active") {
    // 没配通知渠道时在这里也提一句：正在告警却没人收到，比没有告警更该知道。
    const warn = configured ? "" : `<div class="notice"><strong>通知渠道尚未配置</strong><span>下面的告警不会发到任何地方。到「提醒规则」页配置 Telegram 或 Webhook。</span></div>`;
    body = `${warn}<section class="panel"><div class="panel-head"><h3>正在告警</h3><span class="muted">每项显示最近一次触发或重复提醒</span></div>${eventTable(eventRows(firing, configured), "当前没有正在告警的项目。")}</section>`;
  } else if (state.alertView === "events") {
    events = await api(eventsURL());
    const filtered = eventFilter.node_id || eventFilter.kind;
    const nodeOptions = withCurrent(state.nodes.map((n) => [n.id, n.name]), eventFilter.node_id, "全部节点");
    const kindOptions = [["", "全部类型"], ...Object.entries(kindName).filter(([k]) => k !== "test")];
    body = `<section class="panel"><div class="panel-head"><h3>事件记录</h3><span class="muted">保存 90 天 · 每次加载 ${EVENT_PAGE} 条</span></div><div class="catalog-filters event-filters"><label>节点<select id="event-node"><option value="">全部节点</option>${nodeOptions.filter(([v]) => v).map(([v, l]) => `<option value="${esc(v)}"${v === eventFilter.node_id ? " selected" : ""}>${esc(l)}</option>`).join("")}</select></label><label>类型<select id="event-kind">${kindOptions.map(([v, l]) => `<option value="${esc(v)}"${v === eventFilter.kind ? " selected" : ""}>${esc(l)}</option>`).join("")}</select></label></div>${eventTable(eventRows(events, configured), filtered ? "没有符合筛选条件的事件。" : "暂无事件。提醒规则在「提醒规则」页开启。")}<div class="catalog-toolbar event-more"><button id="more-events" class="quiet" ${events.length < EVENT_PAGE ? "hidden" : ""}>加载更多</button></div></section>`;
  } else {
    body = `<section class="panel webhook-panel"><div><h3>通知渠道 · ${channelName}</h3><p>支持 Telegram 机器人或 Webhook，发送告警触发与恢复通知。</p><small>投递失败采用退避重试，最多 10 次；超过 24 小时的事件不再补发。接收端按 event.id 去重。</small></div><div class="webhook-actions"><button id="configure-webhook" class="primary">配置渠道</button><button id="test-webhook" ${configured ? "" : "disabled"}>发送测试</button></div></section>${rulesPanel()}`;
  }
  content().innerHTML = heading + body;
  content().querySelectorAll("[data-alert-view]").forEach(
    (b) =>
      (b.onclick = guard(async () => {
        state.alertView = b.dataset.alertView;
        await renderAlerts();
      })),
  );
  content().querySelector("#refresh-events").onclick = guard(async () => {
    state.nodes = await api("/admin/nodes");
    fleetStatus();
    await renderAlerts();
  });
  if (state.alertView === "events") wireEvents(events, configured);
  if (state.alertView === "rules") wireRules(settings, configured);
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
