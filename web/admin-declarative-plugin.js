import { api, send, esc, guard, toast } from "./api.js";
import { content, state, confirmAction } from "./admin-state.js";
import { pluginFields, pluginSettings } from "./notification-fields.js";
import { taskEditor } from "./task-editor.js";

export async function openDeclarativePlugin(id, { back } = {}) {
  const plugin = (await api("/admin/plugins")).find(item => item.id === id);
  if (!plugin || !["notify", "targets"].includes(plugin.kind)) throw new Error("插件已移除或类型不支持，请刷新列表");
  document.querySelector("#dialog")?.close();
  state.tab = "plugins";
  const root = content();
  root.innerHTML = `<section data-declarative-plugin="${esc(plugin.id)}"><div class="page-heading"><div><button class="quiet" data-plugin-back>← 返回插件</button><h1>${esc(plugin.name)}</h1><p>${esc(plugin.hint || (plugin.kind === "notify" ? "配置通知的接收位置。" : "使用目标集创建探测任务。"))}</p></div><button data-plugin-refresh>刷新</button></div><div data-plugin-body><p role="status">正在读取…</p></div></section>`;
  const page = root.querySelector("[data-declarative-plugin]");
  const refresh = () => openDeclarativePlugin(id, { back });
  const returnToList = back || (async () => (await import("./admin-plugins.js")).renderPlugins());
  page.querySelector("[data-plugin-back]").onclick = guard(returnToList);
  page.querySelector("[data-plugin-refresh]").onclick = guard(refresh);
  try {
    if (plugin.kind === "notify") await notificationPage(page, plugin, refresh);
    else await targetsPage(page, plugin, refresh);
  } catch (error) {
    if (page.isConnected) {
      page.querySelector("[data-plugin-body]").innerHTML = `<section class="panel"><p role="alert">加载失败：${esc(error.message)}</p><button data-plugin-retry>重新加载</button></section>`;
      page.querySelector("[data-plugin-retry]").onclick = guard(refresh);
    }
    return;
  }
  if (!page.isConnected) return;
  const removeArea = document.createElement("section");
  removeArea.className = "panel";
  removeArea.innerHTML = `<div class="panel-head"><div><h2>安装信息</h2><p>${esc(plugin.id)} · v${esc(plugin.version || "未标记")}</p></div><button class="danger" data-plugin-remove>移除插件</button></div><p class="plugin-content">${plugin.kind === "notify" ? "正在使用的通知渠道需先切换，移除后该插件不再出现在渠道列表中。" : "移除后目标集从目录中退出，已有任务使用已保存的目标快照，继续运行。"}</p>`;
  page.append(removeArea);
  removeArea.querySelector("[data-plugin-remove]").onclick = () => confirmAction(`移除插件 · ${plugin.name}`, plugin.kind === "notify"
    ? "移除这个通知插件。正在使用的通知渠道会阻止移除，请先切换渠道；历史事件保留。"
    : "从目标目录移除这个目标集；已经创建的任务与执行记录保留，继续使用其已保存的目标。", async () => {
    await send("/admin/plugins/" + encodeURIComponent(plugin.id), "DELETE");
    state.catalog = null;
    toast("插件已移除");
    await returnToList();
  }, { label: "移除插件" });
}

function channelName(settings) {
  if (settings.channel === "plugin") return settings.plugin_name || settings.plugin || "通知插件";
  return settings.channel === "telegram" ? "Telegram" : settings.channel === "webhook" ? "Webhook" : "尚未配置";
}

async function notificationPage(page, plugin, refresh) {
  const settings = await api("/admin/notification");
  if (!page.isConnected) return;
  const active = settings.channel === "plugin" && settings.plugin === plugin.id;
  page.querySelector("[data-plugin-body]").innerHTML = `<section class="panel"><div class="panel-head"><h2>通知渠道</h2><span class="pill${active ? " good" : ""}">${active ? "正在使用" : "未使用"}</span></div>
    <div class="plugin-content"><p>${active ? "告警与恢复通知由这个插件发送。" : `当前渠道：${esc(channelName(settings))}。保存并使用后，将由这个插件发送告警与恢复通知。`}</p>
    ${active && settings.plugin_error ? `<p role="alert" class="warn-text">${esc(settings.plugin_error)}</p>` : ""}
    <form data-plugin-notification class="form-section"><div class="form-grid">${pluginFields(plugin, active ? settings : null)}</div>
      <p class="hint">凭据仅保存在主控，密钥不回显。保存后继续投递尚未发送的历史事件。</p>
      <div class="form-footer"><button type="submit" class="primary">${active ? "保存设置" : "保存并使用"}</button><button type="button" data-plugin-test ${active && settings.plugin_configured && !settings.plugin_error ? "" : "disabled"}>发送测试</button></div>
    </form></div></section>`;
  const form = page.querySelector("[data-plugin-notification]");
  form.onsubmit = guard(async event => {
    event.preventDefault();
    if (!form.reportValidity()) return;
    const submit = form.querySelector('[type="submit"]');
    submit.disabled = true;
    try {
      await send("/admin/notification", "PUT", { channel: "plugin", plugin: plugin.id, plugin_settings: pluginSettings(form, plugin) });
      toast("通知设置已保存");
      await refresh();
    } finally { if (submit.isConnected) submit.disabled = false; }
  });
  page.querySelector("[data-plugin-test]").onclick = guard(async event => {
    const button = event.currentTarget;
    button.disabled = true;
    try {
      const current = await api("/admin/notification");
      if (current.channel !== "plugin" || current.plugin !== plugin.id || !current.plugin_configured || current.plugin_error) throw new Error("通知渠道已变化，请刷新后重试");
      await send("/admin/notification/test", "POST");
      toast("测试通知已送达");
    } finally { if (button.isConnected) button.disabled = false; }
  });
}

async function targetsPage(page, plugin, refresh) {
  const catalog = await api("/admin/catalog");
  if (!page.isConnected) return;
  const targets = (catalog.targets || []).filter(item => item.plugin === plugin.id);
  page.querySelector("[data-plugin-body]").innerHTML = `<section class="panel">
    <div class="panel-head"><div><h2>探测目标</h2><p>已加入目标目录，共 ${targets.length} 个地址。已有任务独立保存目标，更新目标集不会改写任务。</p></div><button class="primary" data-plugin-create-task ${targets.length ? "" : "disabled"}>用这些目标创建任务</button></div>
    <div class="plugin-content"><label>筛选目标<input type="search" data-plugin-target-query placeholder="地区、运营商或地址" autocomplete="off"></label>
    <div data-plugin-target-list></div></div></section>`;
  const list = page.querySelector("[data-plugin-target-list]");
  const draw = query => {
    const matching = targets.filter(item => [item.region, item.carrier, item.host, item.family].some(value => String(value || "").toLocaleLowerCase().includes(query)));
    list.innerHTML = `<div class="table-scroll"><table class="stack"><thead><tr><th>地区 / 运营商</th><th>地址</th><th>协议</th><th>端口</th></tr></thead><tbody>${matching.slice(0, 100).map(item => `<tr><td data-label="地区 / 运营商">${esc(item.region)} / ${esc(item.carrier)}</td><td data-label="地址" class="mono">${esc(item.host)}</td><td data-label="协议">${esc(item.family === "ipv4" ? "IPv4" : item.family === "ipv6" ? "IPv6" : item.family)}</td><td data-label="端口">${esc(item.port)}</td></tr>`).join("") || '<tr><td colspan="4" class="empty-cell">没有符合条件的目标。</td></tr>'}</tbody></table></div>${matching.length > 100 ? `<p class="hint">显示前 100 项，共 ${matching.length} 项。可缩小筛选范围。</p>` : ""}`;
  };
  draw("");
  page.querySelector("[data-plugin-target-query]").oninput = event => draw(event.target.value.trim().toLocaleLowerCase());
  page.querySelector("[data-plugin-create-task]").onclick = guard(async event => {
    const button = event.currentTarget;
    button.disabled = true;
    try {
      const [fresh, nodes] = await Promise.all([api("/admin/catalog"), api("/admin/nodes")]);
      if (!page.isConnected) return;
      const selected = fresh.targets.filter(item => item.plugin === plugin.id);
      if (!selected.length) throw new Error("目标集已变化或移除，请刷新后重试");
      taskEditor({
        name: plugin.name, type: "probe", enabled: false, node_ids: [],
        probe: { checks: ["tcp"], targets: selected.map(({ host, port, family, region, carrier }) => ({ host, port, family, region, carrier })), interval: 60, route_interval: 3600 },
      }, fresh, nodes, refresh);
    } finally { if (button.isConnected) button.disabled = false; }
  });
}
