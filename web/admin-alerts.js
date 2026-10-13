import { api, send, esc, toast, modal, guard } from "./api.js";
import { date, kindName } from "./format.js";
import { state, content, fleetStatus, field, select, withCurrent, nodeName, confirmAction } from "./admin-state.js";
// 提醒规则：每条规则一种类型、自己的参数与重复间隔，外加作用范围（全部节点 / 分组 / 指定节点）。
// 同一台节点同一种类型只生效一条：指定节点 > 分组 > 全部节点。节点列表里的 alerts 是服务端
// 按这个优先级算好的生效规则（带 id 与 scope），规则本身从 /admin/alert-rules 读。
const RULE_SPECS = [
  {
    kind: "offline", label: "离线提醒", column: "离线",
    fields: [{ key: "hold_seconds", label: "等待秒数", def: 60, attrs: 'min="0" max="86400"' }],
    hint: "节点 45 秒未上报即判定离线，「等待秒数」是判定之后再等多久才提醒；恢复时通知一次。新加的节点 15 分钟内还没接入不算离线。",
    cell: (r) => `等 ${r.hold_seconds || 0} 秒`,
  },
  {
    kind: "renewal", label: "续费提醒", column: "续费",
    fields: [{ key: "threshold", label: "提前天数", def: 7, attrs: 'min="0" max="365" step="1"' }],
    hint: "按节点的到期时间提醒；没填到期时间的节点不会触发。",
    cell: (r) => `提前 ${r.threshold || 0} 天`,
  },
  {
    kind: "traffic", label: "流量警告", column: "流量",
    fields: [{ key: "threshold", label: "额度百分比", def: 80, attrs: 'min="1" max="100" step="any"' }],
    hint: "本计费周期用量达到额度的这个比例时提醒；没填流量额度的节点不会触发。",
    cell: (r) => `${r.threshold}%`,
  },
  {
    kind: "throttle", label: "达量降速警告", column: "降速", fields: [],
    hint: "按节点自己的降速规则触发；没填降速速率的节点不会触发。",
    cell: () => "开",
  },
  {
    kind: "cpu", label: "CPU 警告", column: "CPU",
    fields: [
      { key: "threshold", label: "CPU %", def: 90, attrs: 'min="1" max="100" step="any"' },
      { key: "hold_seconds", label: "持续秒数", def: 300, attrs: 'min="30" max="86400"' },
    ],
    cell: (r) => `${r.threshold}% · ${r.hold_seconds} 秒`,
  },
  {
    kind: "memory", label: "内存警告", column: "内存",
    fields: [
      { key: "threshold", label: "内存 %", def: 90, attrs: 'min="1" max="100" step="any"' },
      { key: "hold_seconds", label: "持续秒数", def: 300, attrs: 'min="30" max="86400"' },
    ],
    cell: (r) => `${r.threshold}% · ${r.hold_seconds} 秒`,
  },
];
// 百分比与重复间隔的输入不卡步长（step="any"）：MCP / 接口可以存下 85.5%、7 分钟这样的值，
// 卡着步长时这样的规则在面板里打开就存不回去。取值范围仍由服务端把关。
const specOf = (kind) => RULE_SPECS.find((s) => s.kind === kind);
const ruleOf = (list, kind) => (list || []).find((r) => r.kind === kind);
const SCOPES = [
  ["all", "全部节点", "全部"],
  ["group", "按分组", "分组"],
  ["nodes", "指定节点", "单独"],
];
const scopeRank = (scope) => SCOPES.findIndex(([v]) => v === scope);
const groupLabel = (g) => g || "默认";
// 规则的参数（开着时附上重复间隔），总览格子与规则列表共用。
const ruleParams = (r) => specOf(r.kind).cell(r) + (r.repeat_seconds ? ` · 每 ${r.repeat_seconds / 60} 分钟` : "");
export function alertSummary(list) {
  const on = RULE_SPECS.map((spec) => [spec, ruleOf(list, spec.kind)]).filter(([, r]) => r).map(([spec, r]) => `${spec.column} ${ruleParams(r)}`);
  return on.length ? on.join("，") : "未开启任何提醒";
}
// 名字列表太长时只写前三个。
const names = (list) => (list.length > 3 ? `${list.slice(0, 3).join("、")} 等 ${list.length} 个` : list.join("、"));
function scopeText(r) {
  const exclude = r.exclude_node_ids?.length ? `，排除 ${names(r.exclude_node_ids.map(nodeName))}` : "";
  if (r.scope === "all") return "全部节点" + exclude;
  if (r.scope === "group") return `分组：${names((r.groups || []).map(groupLabel))}${exclude}`;
  return `节点：${names((r.node_ids || []).map(nodeName))}`;
}
// 规则覆盖的节点（不管有没有被更具体的同类规则盖过）。
function covered(r) {
  return state.nodes.filter((n) => {
    if (r.scope === "nodes") return (r.node_ids || []).includes(n.id);
    if ((r.exclude_node_ids || []).includes(n.id)) return false;
    return r.scope === "all" || (r.groups || []).includes(n.group || "");
  });
}
// 规则列表：按类型排，同类型里范围从宽到窄，正好是优先级从低到高的顺序。
function rulesPanel(rules) {
  const sorted = [...rules].sort((a, b) => RULE_SPECS.indexOf(specOf(a.kind)) - RULE_SPECS.indexOf(specOf(b.kind)) || scopeRank(a.scope) - scopeRank(b.scope));
  const rows = sorted
    .map((r) => {
      const hits = covered(r).length;
      const live = state.nodes.filter((n) => ruleOf(n.alerts, r.kind)?.id === r.id).length;
      const note = live < hits ? `<small>生效 ${live} 台，另 ${hits - live} 台以更具体的规则为准</small>` : `<small>生效 ${live} 台</small>`;
      return `<tr><td data-label="类型">${esc(specOf(r.kind).label)}</td><td data-label="条件">${esc(ruleParams(r))}</td><td data-label="范围">${esc(scopeText(r))}${note}</td><td data-label="操作"><div class="row-actions"><button data-edit-rule="${esc(r.id)}">编辑</button><button class="danger" data-delete-rule="${esc(r.id)}">删除</button></div></td></tr>`;
    })
    .join("");
  return `<section class="panel"><div class="panel-head"><div><h3>提醒规则</h3><span class="muted">同一台节点同一种提醒只生效一条：指定节点 &gt; 分组 &gt; 全部节点</span></div><button id="new-rule" class="primary">新建规则</button></div><div class="table-scroll"><table class="stack"><thead><tr><th>类型</th><th>条件</th><th>范围</th><th>操作</th></tr></thead><tbody>${rows || `<tr><td colspan="4" class="empty-cell">还没有提醒规则。新建一条并选好作用范围；选「全部节点」时，以后新加的节点也会自动套用。</td></tr>`}</tbody></table></div></section>`;
}
// 各节点生效情况：每台节点一行，每种提醒一格，写参数与它来自哪一级范围。
function overviewPanel() {
  const rows = state.nodes
    .map((n) => {
      const cells = RULE_SPECS.map((spec) => {
        const r = ruleOf(n.alerts, spec.kind);
        const from = r ? SCOPES.find(([v]) => v === r.scope)?.[2] : "";
        return `<td data-label="${spec.column}">${r ? `${esc(ruleParams(r))}<small>${from}</small>` : "—"}</td>`;
      }).join("");
      return `<tr><td data-label="节点">${esc(n.name)}<small>${esc(groupLabel(n.group))}</small></td>${cells}</tr>`;
    })
    .join("");
  const heads = RULE_SPECS.map((spec) => `<th>${spec.column}</th>`).join("");
  return `<section class="panel"><div class="panel-head"><h3>各节点生效情况</h3><span class="muted">格子下方写规则来自哪一级：全部 / 分组 / 单独</span></div><div class="table-scroll"><table class="stack"><thead><tr><th>节点</th>${heads}</tr></thead><tbody>${rows || `<tr><td colspan="${RULE_SPECS.length + 1}" class="empty-cell">还没有节点。</td></tr>`}</tbody></table></div></section>`;
}
const tick = (label, attr, value, checked) => `<label class="checkbox"><input type="checkbox" ${attr}="${esc(value)}" ${checked ? "checked" : ""}>${esc(label)}</label>`;
// 新建或编辑一条规则。范围切换时只显示那一种范围要填的列表；底部实时写出会生效在几台节点上。
function ruleEditor(rule, rules) {
  const r = rule || { kind: "offline", scope: "all", repeat_seconds: 0 };
  const groups = [...new Set([...state.nodes.map((n) => n.group || ""), ...(r.groups || [])])].sort();
  const nodeTicks = (attr, picked) => state.nodes.map((n) => tick(`${n.name}（${groupLabel(n.group)}）`, attr, n.id, picked.includes(n.id))).join("") || "<p>请先创建节点</p>";
  const d = modal(
    rule ? `编辑提醒规则 · ${specOf(r.kind).label}` : "新建提醒规则",
    `<form id="rule-form">
      <div class="form-section"><h3>提醒什么</h3><div class="form-grid">${select("类型", "kind", RULE_SPECS.map((s) => [s.kind, s.label]), r.kind)}<div id="rule-params" class="rule-params"></div>${field("重复间隔（分钟）", "repeat", (r.repeat_seconds || 0) / 60, "number", 'min="0" max="10080" step="any" placeholder="0 只提醒一次"')}</div><p class="hint" id="rule-hint"></p><p class="hint">重复间隔填 0 只在触发与恢复时各通知一次，否则至少 5 分钟。</p></div>
      <div class="form-section"><h3>作用于</h3>
        <div class="option-tiles" role="radiogroup">${SCOPES.map(([v, label]) => `<label class="checkbox"><input type="radio" name="scope" value="${v}" ${r.scope === v ? "checked" : ""}>${label}</label>`).join("")}</div>
        <div data-part="group"><span class="option-label">分组</span><div class="node-checkboxes">${groups.map((g) => tick(groupLabel(g), "data-group", g, (r.groups || []).includes(g))).join("")}</div></div>
        <div data-part="nodes"><div class="catalog-toolbar"><button type="button" data-pick="all">全选节点</button><button type="button" data-pick="none">清空节点</button></div><div class="node-checkboxes">${nodeTicks("data-node", r.node_ids || [])}</div></div>
        <details data-part="exclude" ${r.exclude_node_ids?.length ? "open" : ""}><summary>排除节点（可选）</summary><div class="node-checkboxes">${nodeTicks("data-exclude", r.exclude_node_ids || [])}</div></details>
        <p class="hint" id="rule-preview"></p>
      </div>
      <div class="form-footer"><button type="submit" class="primary">保存规则</button></div></form>`,
  );
  const form = d.querySelector("form");
  const picked = (attr) => [...form.querySelectorAll(`[${attr}]:checked`)].map((b) => b.getAttribute(attr));
  const params = () => {
    const spec = specOf(form.elements.kind.value);
    const same = rule && rule.kind === spec.kind;
    form.querySelector("#rule-params").innerHTML = spec.fields.map((f) => field(f.label, f.key, same ? (rule[f.key] ?? 0) : f.def, "number", `${f.attrs} required`)).join("");
    form.querySelector("#rule-hint").textContent = spec.hint || "";
  };
  const read = () => {
    const scope = form.elements.scope.value;
    const out = { kind: form.elements.kind.value, scope, repeat_seconds: Math.round(Number(form.elements.repeat.value) * 60) };
    for (const f of specOf(out.kind).fields) out[f.key] = Number(form.elements[f.key].value);
    if (scope === "group") out.groups = picked("data-group");
    if (scope === "nodes") out.node_ids = picked("data-node");
    if (scope !== "nodes" && picked("data-exclude").length) out.exclude_node_ids = picked("data-exclude");
    return out;
  };
  // 预览：这条规则覆盖几台，其中几台被更具体的同类规则盖过（保存后以那条为准）。
  const preview = () => {
    const next = read();
    form.querySelectorAll("[data-part]").forEach((el) => (el.hidden = el.dataset.part === "exclude" ? next.scope === "nodes" : el.dataset.part !== next.scope));
    const hits = covered(next);
    const narrower = hits.filter((n) => rules.some((o) => o.id !== rule?.id && o.kind === next.kind && scopeRank(o.scope) > scopeRank(next.scope) && covered(o).some((x) => x.id === n.id)));
    form.querySelector("#rule-preview").textContent = `覆盖 ${hits.length} 台节点` + (narrower.length ? `，其中 ${names(narrower.map((n) => n.name))} 另有更具体的${specOf(next.kind).column}规则，以那条为准。` : "。") + (next.scope === "all" ? "以后新加的节点也会自动套用。" : "");
  };
  form.elements.kind.onchange = () => {
    params();
    preview();
  };
  form.onchange = preview;
  form.querySelectorAll("[data-pick]").forEach((b) => (b.onclick = () => {
    form.querySelectorAll("[data-node]").forEach((x) => (x.checked = b.dataset.pick === "all"));
    preview();
  }));
  params();
  preview();
  form.onsubmit = guard(async (e) => {
    e.preventDefault();
    const body = read();
    if (body.scope === "group" && !body.groups.length) throw new Error("请至少选一个分组");
    if (body.scope === "nodes" && !body.node_ids.length) throw new Error("请至少选一台节点");
    if (rule) await send(`/admin/alert-rules/${encodeURIComponent(rule.id)}`, "PUT", body);
    else await send("/admin/alert-rules", "POST", body);
    d.close();
    await renderAlerts();
    toast(rule ? "提醒规则已保存" : "提醒规则已创建");
  });
}
// 保存、删除之后重画用 renderAlerts：它在规则页会连节点一起重读（生效台数来自节点的 alerts）。
function wireRulesPanel(rules) {
  content().querySelector("#new-rule").onclick = () => ruleEditor(null, rules);
  content().querySelectorAll("[data-edit-rule]").forEach(
    (b) => (b.onclick = () => ruleEditor(rules.find((r) => r.id === b.dataset.editRule), rules)),
  );
  content().querySelectorAll("[data-delete-rule]").forEach((b) => {
    const r = rules.find((x) => x.id === b.dataset.deleteRule);
    b.onclick = () =>
      confirmAction("删除提醒规则", `删除「${specOf(r.kind).label} · ${scopeText(r)}」？被它覆盖的节点改由范围更宽的同类规则接管，没有就不再提醒这一项。`, async () => {
        await send(`/admin/alert-rules/${encodeURIComponent(r.id)}`, "DELETE");
        await renderAlerts();
        toast("提醒规则已删除");
      }, { label: "删除" });
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
const ALERT_VIEWS = [
  ["active", "当前告警"],
  ["events", "事件记录"],
  ["rules", "提醒规则"],
];
function eventTable(rows, empty) {
  return `<div class="table-scroll"><table class="stack"><thead><tr><th>时间</th><th>节点</th><th>类型</th><th>状态</th><th>内容</th><th>通知</th></tr></thead><tbody id="event-rows">${rows || `<tr><td colspan="6" class="empty-cell">${empty}</td></tr>`}</tbody></table></div>`;
}
export async function renderAlerts() {
  // 规则页的「生效 N 台」与逐台表格读的是节点上服务端算好的 alerts，必须和规则同一时刻读：
  // 只重读规则的话，MCP 那边刚改过的规则会显示成生效 0 台，直到手动刷新。
  const rulesView = state.alertView === "rules";
  const [settings, firing, rules, nodes] = await Promise.all([
    api("/admin/notification"),
    api("/admin/events?active=1&limit=500"),
    rulesView ? api("/admin/alert-rules") : [],
    rulesView ? api("/admin/nodes") : null,
  ]);
  if (nodes) {
    state.nodes = nodes;
    fleetStatus();
  }
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
    body = `<section class="panel webhook-panel"><div><h3>通知渠道 · ${channelName}</h3><p>支持 Telegram 机器人或 Webhook，发送告警触发与恢复通知。</p><small>投递失败采用退避重试，最多 10 次；超过 24 小时的事件不再补发。接收端按 event.id 去重。</small></div><div class="webhook-actions"><button id="configure-webhook" class="primary">配置渠道</button><button id="test-webhook" ${configured ? "" : "disabled"}>发送测试</button></div></section>${rulesPanel(rules)}${overviewPanel()}`;
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
    // 规则页的 renderAlerts 自己会重读节点。
    if (!rulesView) {
      state.nodes = await api("/admin/nodes");
      fleetStatus();
    }
    await renderAlerts();
  });
  if (state.alertView === "events") wireEvents(events, configured);
  if (rulesView) wireRules(settings, configured, rules);
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
function wireRules(settings, configured, rules) {
  wireRulesPanel(rules);
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
