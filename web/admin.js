import { taskEditor, taskSummary } from "./task-editor.js";
import { api, send, esc, toast, modal, guard } from "./api.js";
import { bytes, date, days, kindName } from "./format.js";
import { detail } from "./detail.js";
let container,
  brandHTML,
  tab = "nodes",
  nodes = [],
  tasks = [],
  catalog;
const defaults = {
  name: "",
  group: "",
  region: "",
  order: 0,
  public: true,
  representative: "",
  traffic_limit: 0,
  traffic_mode: "sum",
  billing_day: 1,
  price: 0,
  currency: "CNY",
  price_period: "月",
  expires_at: "",
  alerts: {
    offline: false,
    renewal: false,
    renewal_days: 7,
    traffic: false,
    traffic_percent: 80,
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
  container.innerHTML = `<div class="login-layout"><section class="login-story">${brandHTML}<div><div class="eyebrow">SMALL STACK. CLEAR CONTROL.</div><h1>少一点复杂，<br>多一点掌控。</h1><p>主机、网络、用量与提醒。<br>一套属于自己的基础设施观测工具。</p></div><small>TZA PROBE / CONTROL PLANE</small></section><main class="login-panel"><form id="login"><div class="eyebrow">WELCOME BACK</div><h2>进入管理控制台</h2><p>使用部署时生成的管理密钥登录。</p><label>管理密钥<input type="password" name="token" required minlength="32" autocomplete="current-password" autofocus></label><p class="hint">密钥保存在服务器数据目录的 admin-token 文件中。</p><button class="primary" type="submit">登录控制台 →</button><a href="/">← 返回公开面板</a></form></main></div>`;
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
  container.innerHTML = `<div class="admin-layout"><aside class="sidebar">${brandHTML}<div class="sidebar-caption">WORKSPACE</div><nav><button data-tab="nodes"><svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="3" y="3" width="6" height="6" rx="1.5"/><rect x="11" y="3" width="6" height="6" rx="1.5"/><rect x="3" y="11" width="6" height="6" rx="1.5"/><rect x="11" y="11" width="6" height="6" rx="1.5"/></svg><span>节点管理</span></button><button data-tab="tasks"><svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="10" cy="10" r="7"/><circle cx="10" cy="10" r="3"/><circle cx="10" cy="10" r="0.8" fill="currentColor"/></svg><span>网络探测</span></button><button data-tab="alerts"><svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="10" cy="10" r="7"/><polyline points="10 6 10 10 13 12"/></svg><span>告警与通知</span></button></nav><div class="sidebar-bottom"><a href="/">公开面板 ↗</a><button id="logout">退出登录</button><small>独立运行 · TZA PROBE V2</small></div></aside><main class="admin-main"><header class="admin-top"><span>控制台 <span class="muted">/</span> <b id="breadcrumb"></b></span><span class="pill">● 系统运行中</span></header><div id="admin-content"></div></main></div>`;
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
  if (tab === "nodes") renderNodes();
  if (tab === "tasks") {
    tasks = await api("/admin/tasks");
    renderTasks();
  }
  if (tab === "alerts") await renderAlerts();
}
const content = () => container.querySelector("#admin-content");
function renderNodes() {
  content().innerHTML = `<section class="page-heading"><div><div class="eyebrow">INVENTORY</div><h1>节点管理<span>${nodes.length}</span></h1><p>管理接入、展示、计费周期和每台主机的提醒规则。</p></div><button id="add-node" class="primary">＋ 添加节点</button></section><div class="admin-stats"><div><span>在线</span><strong>${nodes.filter((n) => n.online).length}</strong></div><div><span>公开展示</span><strong>${nodes.filter((n) => n.public).length}</strong></div><div><span>7 天内到期或过期</span><strong>${nodes.filter((n) => n.expires_at && days(n.expires_at) <= 7).length}</strong></div></div><section class="panel"><div class="panel-head"><h3>全部节点</h3><button id="refresh-nodes" class="quiet">↻ 刷新</button></div><div class="table-scroll"><table><thead><tr><th>主机</th><th>状态</th><th>分组 / 地区</th><th>周期用量</th><th>到期时间</th><th>操作</th></tr></thead><tbody>${nodes.map((n) => `<tr><td><button class="text-button" data-inspect="${n.id}">${esc(n.name)}</button><small>${n.public ? "公开" : "私有"} · 代表地区 ${esc(n.representative || "未设置")}</small></td><td><span class="pill ${n.online ? "good" : ""}">${n.online ? "在线" : "离线"}</span></td><td>${esc(n.group || "默认")}<small>${esc(n.region || "—")}</small></td><td>${bytes(n.traffic_used)}<small>${n.traffic_limit ? "/ " + bytes(n.traffic_limit) : "未设置额度"}</small></td><td>${n.expires_at ? esc(n.expires_at.slice(0, 10)) : "—"}</td><td><div class="row-actions"><button data-edit="${n.id}">编辑</button><button data-token="${n.id}">接入</button><button data-delete="${n.id}" class="danger">删除</button></div></td></tr>`).join("") || '<tr><td colspan="6" class="empty-cell">还没有节点。添加后下载接入配置，启动 Agent。</td></tr>'}</tbody></table></div></section>`;
  content().querySelector("#add-node").onclick = () => nodeForm();
  content().querySelector("#refresh-nodes").onclick = guard(render);
  content()
    .querySelectorAll("[data-edit]")
    .forEach(
      (b) =>
        (b.onclick = () =>
          nodeForm(nodes.find((n) => n.id === b.dataset.edit))),
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
}
function field(label, name, value, type = "text", extra = "") {
  return `<label>${label}<input name="${name}" type="${type}" value="${esc(value)}" ${extra}></label>`;
}
function check(label, name, value) {
  return `<label class="checkbox"><input type="checkbox" name="${name}" ${value ? "checked" : ""}>${label}</label>`;
}
function select(label, name, options, value) {
  return `<label>${label}<select name="${name}">${options.map(([v, l]) => `<option value="${v}" ${v === value ? "selected" : ""}>${l}</option>`).join("")}</select></label>`;
}
function nodeForm(existing) {
  const n = existing || structuredClone(defaults),
    a = n.alerts;
  const d = modal(
    existing ? "编辑节点" : "添加节点",
    `<form id="node-form"><div class="form-section"><h3>基本信息</h3><div class="form-grid">${field("节点名称", "name", n.name, "text", 'required maxlength="100"')}${field("分组", "group", n.group)}${field("节点所在地区", "region", n.region)}${field("卡片代表地区（与回程任务一致）", "representative", n.representative)}${field("排序（小的在前）", "order", n.order, "number")}${check("在公开页面展示", "public", n.public)}</div></div><div class="form-section"><h3>计费与续费</h3><div class="form-grid">${field("流量额度（GB，0 为不设上限）", "quota", n.traffic_limit / 1e9, "number", 'min="0" step="0.1"')}${select(
      "流量计费方式",
      "traffic_mode",
      [
        ["sum", "上传 + 下载"],
        ["max", "上传 / 下载取较大值"],
        ["in", "仅下载"],
        ["out", "仅上传"],
      ],
      n.traffic_mode,
    )}${field("每月重置日（UTC）", "billing_day", n.billing_day, "number", 'min="1" max="31" required')}${field("价格", "price", n.price, "number", 'min="0" step="0.01"')}${field("货币", "currency", n.currency, "text", 'maxlength="12"')}${field("价格周期", "price_period", n.price_period, "text", 'maxlength="30"')}${field("到期日期（UTC 当日 00:00）", "expires", n.expires_at?.slice(0, 10) || "", "date")}</div><p class="hint">月底不足指定日期时取当月最后一天。首次接入建立流量基线，不补算历史用量；主机重启或网卡集合变化时自动重建基线。</p></div><div class="form-section"><h3>提醒规则</h3><div class="form-grid">${check("离线提醒", "offline", a.offline)}${check("续费提醒", "renewal", a.renewal)}${field("提前多少天提醒续费", "renewal_days", a.renewal_days, "number", 'min="0" max="365"')}${check("流量警告", "traffic", a.traffic)}${field("流量达到额度百分比", "traffic_percent", a.traffic_percent, "number", 'min="1" max="100"')}${check("负载警告（CPU 或内存）", "load", a.load)}${field("CPU 阈值 %", "cpu_percent", a.cpu_percent, "number", 'min="1" max="100"')}${field("内存阈值 %", "memory_percent", a.memory_percent, "number", 'min="1" max="100"')}${field("负载持续秒数", "hold_seconds", a.hold_seconds, "number", 'min="30" max="86400"')}</div><p class="hint">触发和恢复各记录一次。通知出口在“告警与通知”配置。</p></div><div class="form-footer"><button type="submit" class="primary">保存节点</button></div></form>`,
  );
  d.querySelector("form").onsubmit = guard(async (e) => {
    e.preventDefault();
    const f = new FormData(e.target),
      get = (k) => String(f.get(k) || ""),
      num = (k) => Number(f.get(k));
    const value = {
      ...defaults,
      name: get("name"),
      group: get("group"),
      region: get("region"),
      representative: get("representative"),
      order: num("order"),
      public: f.has("public"),
      traffic_limit: Math.round(num("quota") * 1e9),
      traffic_mode: get("traffic_mode"),
      billing_day: num("billing_day"),
      price: num("price"),
      currency: get("currency"),
      price_period: get("price_period"),
      expires_at: get("expires") ? get("expires") + "T00:00:00Z" : "",
      alerts: {
        offline: f.has("offline"),
        renewal: f.has("renewal"),
        renewal_days: num("renewal_days"),
        traffic: f.has("traffic"),
        traffic_percent: num("traffic_percent"),
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
function showToken(token) {
  const config = {
    endpoint: location.origin,
    token,
    interfaces: [],
    mount: "/",
    nexttrace: "",
    allow_http: false,
  };
  const d = modal(
    "节点接入配置",
    `<p>密钥仅在此处显示。将配置保存到 Agent 主机，文件权限设为 600。</p><pre id="agent-config">${esc(JSON.stringify(config, null, 2))}</pre><p class="hint">填写需要统计的公网网卡；三网回程需安装 NextTrace 并填写其绝对路径。生产环境使用 HTTPS。</p><div class="form-footer"><button id="copy-config">复制配置</button><button id="download-config" class="primary">下载 agent.json</button></div>`,
  );
  d.querySelector("#copy-config").onclick = guard(async () => {
    await navigator.clipboard.writeText(JSON.stringify(config, null, 2));
    toast("配置已复制");
  });
  d.querySelector("#download-config").onclick = () => {
    const url = URL.createObjectURL(
      new Blob([JSON.stringify(config, null, 2)], { type: "application/json" }),
    );
    const a = document.createElement("a");
    a.href = url;
    a.download = "agent.json";
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  };
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
  content().innerHTML = `<section class="page-heading"><div><div class="eyebrow">NETWORK OBSERVABILITY</div><h1>网络探测<span>${tasks.length}</span></h1><p>每个任务独立配置目标、协议、频率和参与节点。</p></div><button id="add-task" class="primary">＋ 创建任务</button></section><div class="notice"><strong>回程标签由实际路径决定</strong><span>卡片使用节点指定的代表地区。IPv4 / IPv6 独立检测；空节点绑定不会执行。</span></div><section class="panel"><div class="table-scroll"><table><thead><tr><th>任务</th><th>目标</th><th>地区 / 运营商</th><th>间隔</th><th>节点</th><th>操作</th></tr></thead><tbody>${tasks.map((t) => `<tr><td>${esc(t.name)}<small>${t.enabled ? "已启用" : "已停用"} · ${esc(taskSummary(t).checks)}</small></td><td>${taskSummary(t).count} 个地址<small>${esc(taskSummary(t).families)}</small></td><td>${esc(taskSummary(t).targets)}<small></small></td><td>${esc(taskSummary(t).intervals)}</td><td>${t.node_ids?.filter((id) => nodes.some((n) => n.id === id)).length || 0} 个</td><td><div class="row-actions"><button data-edit-task="${t.id}">编辑</button><button class="danger" data-delete-task="${t.id}">删除</button></div></td></tr>`).join("") || '<tr><td colspan="6" class="empty-cell">暂无任务。可从内置三网目标目录选择，也可填写自定义目标。</td></tr>'}</tbody></table></div></section>`;
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
  content().innerHTML = `<section class="page-heading"><div><div class="eyebrow">ALERTS & DELIVERY</div><h1>告警与通知</h1><p>按节点配置提醒阈值，在这里管理统一通知出口和事件记录。</p></div><button id="refresh-events" class="quiet">↻ 刷新</button></section><section class="panel webhook-panel"><div><h3>通知渠道 · ${channelName}</h3><p>支持 Telegram 机器人或 Webhook，发送告警触发与恢复通知。</p><small>投递失败采用退避重试，最多 10 次。接收端按 event.id 去重。</small></div><div class="webhook-actions"><button id="configure-webhook" class="primary">配置出口</button><button id="test-webhook" ${configured ? "" : "disabled"}>发送测试</button></div></section><section class="panel"><div class="panel-head"><h3>最近事件</h3><span class="muted">最多 100 条 · 保存 90 天</span></div><div class="table-scroll"><table><thead><tr><th>时间</th><th>节点</th><th>类型</th><th>状态</th><th>内容</th><th>通知</th></tr></thead><tbody>${events.map((e) => `<tr><td>${date(e.at)}</td><td>${esc(e.node_name)}</td><td>${esc(kindName[e.kind] || e.kind)}</td><td><span class="pill ${e.active ? "warn" : "good"}">${e.active ? "触发" : "解除"}</span></td><td>${esc(e.message)}</td><td>${e.delivered ? "已送达" : e.attempts >= 10 ? "发送失败" : configured ? "待发送 / 重试" : "未配置出口"}<small>${e.attempts} 次尝试</small></td></tr>`).join("") || '<tr><td colspan="6" class="empty-cell">暂无事件。提醒规则可在节点编辑页单独开启。</td></tr>'}</tbody></table></div></section>`;
  content().querySelector("#refresh-events").onclick = guard(renderAlerts);
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
