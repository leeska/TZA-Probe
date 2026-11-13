import { api, send, esc, guard, toast } from "./api.js";
import { environmentPicker, wireSearchPickers } from "./environment-picker.js";
import { locationPicker, wireLocationPicker } from "./location-picker.js";

const services = [{ id: "google", name: "Google" }, { id: "meta", name: "Meta" }];
const active = job => ["queued", "running"].includes(job.state);
const fresh = timestamp => timestamp && Date.now() / 1000 - timestamp < 45;
const exitAddresses = (node, family) => node.egress?.enabled && fresh(node.egress.received_at) ? (node.egress.allowed_ips || []).filter(ip => ip.includes(":") === (family === "ipv6")) : [];
const canSaveExit = (node, draft) => exitAddresses(node, draft.family).length > 0 || !draft.enabled && node.policy?.expected_ip && node.policy.expected_ip.includes(":") === (draft.family === "ipv6");
const sameEnvironment = (first, second) => first && second && ["expected_ip", "country", "locale", "timezone"].every(key => first[key] === second[key]) && ["latitude", "longitude", "country", "timezone"].every(key => first.position?.[key] === second.position?.[key]);
const reasons = { http_denied: "服务拒绝访问，未取得地区信息", http_forbidden: "服务拒绝此出口访问（403），未取得地区信息", challenge_detected: "遇到验证页面，暂时无法判断", origin_not_allowed: "页面跳转到未批准的地址", egress_mismatch: "出口 IP 与设置不一致", local_permission_required: "此节点尚未开通检测出口" };
const serverInstall = "curl -fsSL https://raw.githubusercontent.com/leeska/TZA-Probe/main/deploy/install.sh | sudo sh -s -- --with-browser";
const nodeEnable = () => `curl -fsSL ${location.origin}/install.sh | sh -s -- --endpoint ${location.origin} --ipguard`;
const commandBlock = command => `<div class="command-block"><pre>${esc(command)}</pre><button type="button" class="primary" data-copy="${esc(command)}">复制命令</button></div>`;

// What stands between this node and a check, in the order to fix it. Each state
// is a key so the panel is only rebuilt when it changes (polling keeps it stable).
export function setupState(data, node) {
  if (!data.worker_configured) return "install";
  if (!fresh(data.worker_seen)) return "worker";
  if (node && !(node.egress?.enabled && fresh(node.egress.received_at))) return "node";
  return "";
}

function setupPanel(state, node) {
  if (state === "install") return `<section class="panel region-setup" data-setup="install"><h2>先安装检测浏览器</h2><p>地区偏移检测由 Server 上一个隔离的浏览器完成：它没有自己的网络，只能经各节点的 Agent 访问 Google 与 Meta。浏览器约占 300 MB 内存、200 MB 磁盘，默认不安装。</p><p>在 Server 主机上执行（已安装的 Server 会原样保留并一起升级）：</p>${commandBlock(serverInstall)}<p class="hint">脚本会下载固定版本的 Chromium 并校验、安装缺少的系统库与字体，先在与正式服务相同的隔离里自检，通过后才启用。</p></section>`;
  if (state === "worker") return `<section class="panel region-setup" data-setup="worker"><h2>检测浏览器未连接</h2><p>Server 已开启浏览器接口，但浏览器服务最近没有连上。在 Server 主机上查看：</p>${commandBlock("systemctl status tza-browser")}${commandBlock("journalctl -u tza-browser -n 50 --no-pager")}<p class="hint">设置可以先保存，浏览器连上后按计划检测。</p></section>`;
  if (state === "node") return `<section class="panel region-setup" data-setup="node"><h2>开通「${esc(node.name)}」的检测出口</h2><p>节点默认不允许浏览器经它访问外部网站。在该节点上执行下面的命令（只更新 Agent 与出口许可，不改其他配置）：</p>${commandBlock(nodeEnable())}<p class="hint">自动识别本机公网出口；NAT 主机会一并写好本机源地址。只允许访问 Google、Meta 及出口回显服务。关闭：把 --ipguard 换成 --ipguard-off 再执行一次。</p></section>`;
  return "";
}

const countryLabel = code => {
  try { return `${new Intl.DisplayNames(["zh-CN"], { type: "region" }).of(code)}（${code}）`; }
  catch { return code; }
};

export function serviceReading(node, service, jobs) {
  if (!node.policy?.enabled) return { label: "未开启", tone: "", note: "开启节点检测后自动检查" };
  const terms = service === "google_terms";
  const latest = jobs.find(job => job.node_id === node.id && job.service === (terms ? "google" : service) && (!terms || job.phase !== "experiment") && sameEnvironment(job.policy, node.policy));
  if (!latest) return { label: "待检测", tone: "", note: "等待首次检测" };
  if (active(latest)) return { label: latest.phase === "experiment" ? "正在校正" : latest.phase === "followup" ? "正在复测" : "检测中", tone: "", note: "完成后自动更新" };
  const time = latest.finished_at ? new Date(latest.finished_at * 1000).toLocaleString() : "";
  if (Date.now() / 1000 - latest.finished_at > 86400) return { label: "已过期", tone: "", note: `上次检测 ${time}` };
  if (terms) {
    const observation = latest.result?.google_terms;
    if (!observation) return { label: "待检测", tone: "", note: "下次 Google 检测时同时读取条款地区" };
    const country = observation.page?.signal === "google_terms_country" && /^[A-Z]{2}$/.test(observation.page.value) ? observation.page.value : "";
    const comparison = country ? `实际：${countryLabel(country)} · 目标：${countryLabel(node.policy.country)} · ${time}` : "";
    if (observation.assessment === "target_observed" && country === node.policy.country) return { label: "正常", tone: "good", note: comparison };
    if (observation.assessment === "other_region" && country && country !== node.policy.country) return { label: "地区偏移", tone: "bad", note: comparison };
    return { label: "未知", tone: "", note: `${reasons[observation.reason] || "未取得条款适用地区"} · ${time}` };
  }
  if (latest.phase === "experiment") return { label: latest.state === "completed" ? "待复测" : "未知", tone: "", note: latest.state === "completed" ? "已尝试校正，等待独立复测" : "校正未完成，不能确认恢复" };
  const result = latest.result;
  const country = result?.page?.signal === (service === "google" ? "google_page_country" : "ads_library_country") && /^[A-Z]{2}$/.test(result.page.value) ? result.page.value : "";
  const comparison = country ? `实际：${countryLabel(country)} · 目标：${countryLabel(node.policy.country)} · ${time}` : "";
  if (latest.state === "completed" && result?.assessment === "target_observed" && country === node.policy.country) return { label: "正常", tone: "good", note: comparison };
  if (latest.state === "completed" && result?.assessment === "other_region" && country && country !== node.policy.country) return { label: "地区偏移", tone: "bad", note: comparison };
  if (["http_denied", "http_forbidden"].includes(result?.reason)) return { label: "访问受限", tone: "", note: `${reasons[result.reason]} · ${time}` };
  return { label: "未知", tone: "", note: `${reasons[result?.reason] || (result?.page?.signal === "homepage_hkredirect" ? "旧检测没有实际地区证据，等待重新检测" : "未取得实际地区，暂时无法与目标比较")} ${time}` };
}

// 从任务列表点「编辑」进来时先选中那一台（只用一次）。
let preferred = "";
export function preferRegionNode(id) { preferred = id; }

export async function renderIPGuard(root) {
  root.disposeRegionGuard?.();
  const controller = new AbortController();
  const generation = {};
  root.regionGeneration = generation;
  let timer, loading = false, saving = false, epoch = 0, data, disposeMap;
  const alive = () => root.isConnected && root.regionGeneration === generation && !controller.signal.aborted;
  const dispose = () => { clearTimeout(timer); controller.abort(); observer.disconnect(); disposeMap?.(); };
  const observer = new MutationObserver(() => { if (!alive() || data && !root.querySelector("[data-region-page]")) dispose(); });
  observer.observe(document.body, { childList: true, subtree: true });
  root.disposeRegionGuard = dispose;
  try { data = await api("/admin/ipguard", { signal: controller.signal }); }
  catch (error) { dispose(); throw error; }
  if (!alive()) { dispose(); return; }
  let nodeID = data.nodes.find(node => node.id === preferred)?.id || data.nodes.find(node => node.policy)?.id || data.nodes[0]?.id;
  preferred = "";
  const drafts = new Map();
  const selected = () => data.nodes.find(node => node.id === nodeID);
  const draftFor = node => {
    if (!drafts.has(node.id)) {
      const policy = node.policy;
      const addresses = node.egress?.allowed_ips || [];
      drafts.set(node.id, { revision: policy?.revision || 0, enabled: policy?.enabled || false, auto_recover: policy?.auto_recover || false, family: policy?.expected_ip ? policy.expected_ip.includes(":") ? "ipv6" : "ipv4" : addresses.some(ip => !ip.includes(":")) ? "ipv4" : "ipv6", country: policy?.country || "", position: policy?.position || null, services: policy?.services || ["google", "meta"], dirty: false, edits: 0, locationMessage: "", locationPending: false });
    }
    return drafts.get(node.id);
  };
  root.innerHTML = `<section class="region-page" data-region-page><div data-region-setup></div><header class="region-toolbar"><label class="region-node">节点<select data-region-node aria-label="节点">${data.nodes.map(node => `<option value="${esc(node.id)}" ${node.id === nodeID ? "selected" : ""}>${esc(node.name)}</option>`).join("")}</select></label></header><div data-region-form></div><p class="hint" data-region-notice role="status"></p><ul class="region-services" aria-label="服务检测状态">${services.map(service => `<li data-region-service="${service.id}"><strong>${service.name}</strong><div><span class="pill" data-reading></span><small data-note></small></div><button type="button" class="quiet" data-check="${service.id}">检测</button></li>`).join("")}</ul><p class="hint region-footnote">每 12 小时检查一次。正常只表示未发现页面地区异常，不代表服务商的 IP 归属或信誉已改变。</p></section>`;
  const googleReading = root.querySelector('[data-region-service="google"] > div');
  googleReading.insertAdjacentHTML("afterbegin", '<span class="region-signal-title">搜索地区</span>');
  googleReading.insertAdjacentHTML("beforeend", '<div class="region-terms-reading"><span class="region-signal-title">条款地区</span><span class="pill" data-terms-reading></span><small data-terms-note></small><small>条款适用地区，不代表搜索已恢复；不参与自动校正。</small></div>');
  let shownSetup = null;
  const patchSetup = node => {
    const state = setupState(data, node);
    const key = state + ":" + (state === "node" ? node.id : "");
    if (key !== shownSetup) {
      shownSetup = key;
      root.querySelector("[data-region-setup]").innerHTML = setupPanel(state, node);
    }
    // Without a browser the settings cannot do anything: show only the guide.
    for (const part of root.querySelectorAll(".region-toolbar, [data-region-form], [data-region-notice], .region-services, .region-footnote")) part.hidden = state === "install";
  };
  root.addEventListener("click", event => {
    const button = event.target.closest("[data-copy]");
    if (!button) return;
    navigator.clipboard?.writeText(button.dataset.copy).then(() => toast("已复制"), () => toast("复制失败，请手动选择命令"));
  });
  const patch = () => {
    if (!alive()) return;
    const node = selected();
    patchSetup(node);
    if (!node) { root.querySelector("[data-region-notice]").textContent = "先添加节点"; return; }
    const draft = draftFor(node);
    const ready = fresh(data.worker_seen) && node.egress?.enabled && fresh(node.egress.received_at);
    const addresses = exitAddresses(node, draft.family);
    const form = root.querySelector("form");
    form.querySelector('[type="submit"]').disabled = saving || draft.locationPending || draft.coordinatesDirty || !draft.dirty || !canSaveExit(node, draft) || !data.environments?.[draft.country];
    form.querySelector("[data-save-state]").textContent = saving ? "保存中…" : draft.dirty ? "有未保存的更改" : "已保存";
    form.querySelector("[data-environment]").textContent = data.environments?.[draft.country] ? `自动匹配 ${data.environments[draft.country].locale} · ${draft.position?.timezone || data.environments[draft.country].timezone}${draft.position ? "（定位时区）" : "（地区默认时区）"}` : "请选择支持的目标地区";
    form.querySelector("[data-location-status]").textContent = draft.locationPending ? "正在查询定位…" : draft.locationMessage || (draft.position ? `${draft.position.city || draft.position.country} · ${draft.position.latitude.toFixed(5)}, ${draft.position.longitude.toFixed(5)}` : "未设置精确定位，可按出口 IP 定位或搜索地点。");
    form.querySelector("[data-ip-location]").disabled = draft.locationPending || !addresses.length || saving;
    for (const option of form.querySelector('[name="family"]').options) {
      const disabled = !exitAddresses(node, option.value).length;
      const label = `${option.value === "ipv4" ? "IPv4" : "IPv6"}${disabled ? "（不可用）" : ""}`;
      if (option.disabled !== disabled) option.disabled = disabled;
      if (option.textContent !== label) option.textContent = label;
    }
    form.querySelector("[data-exit]").textContent = addresses.length ? `检测出口：${addresses.join("、")}` : node.egress?.enabled && fresh(node.egress.received_at) ? "所选出口尚未开通，请选择可用的 IPv4 或 IPv6。" : "节点检测出口未连接，连接后可选择 IPv4 或 IPv6。";
    const busy = data.jobs.some(job => job.node_id === nodeID && active(job));
    root.querySelector("[data-region-notice]").textContent = !ready ? reasons[node.egress?.reason] || "检测服务或节点出口暂未连接，结果不会被当作正常。" : "";
    for (const service of services) {
      const row = root.querySelector(`[data-region-service="${service.id}"]`);
      row.hidden = !draft.services.includes(service.id);
      const reading = serviceReading(node, service.id, data.jobs);
      row.querySelector("[data-reading]").textContent = reading.label;
      row.querySelector("[data-reading]").className = `pill ${reading.tone}`;
      row.querySelector("[data-note]").textContent = reading.note;
      if (service.id === "google") {
        const terms = serviceReading(node, "google_terms", data.jobs);
        row.querySelector("[data-terms-reading]").textContent = terms.label;
        row.querySelector("[data-terms-reading]").className = `pill ${terms.tone}`;
        row.querySelector("[data-terms-note]").textContent = terms.note;
      }
      row.querySelector("button").disabled = saving || draft.dirty || !ready || !addresses.length || busy || !node.policy?.enabled || !node.policy.services.includes(service.id);
    }
  };
  const renderForm = () => {
    disposeMap?.();
    const node = selected();
    if (!node) return;
    const draft = draftFor(node);
    root.querySelector("[data-region-form]").innerHTML = `<form class="region-inline"><div class="region-controls"><label class="region-toggle"><span><strong>地区偏移检测</strong><small>定时检查此节点的服务地区</small></span><input type="checkbox" role="switch" name="enabled" ${draft.enabled ? "checked" : ""} aria-label="地区偏移检测"></label><label class="region-toggle"><span><strong>偏移时尝试自动校正</strong><small>尝试一次，并独立复测确认</small></span><input type="checkbox" role="switch" name="auto_recover" ${draft.auto_recover ? "checked" : ""} aria-label="偏移时尝试自动校正"></label></div><div class="region-options">${environmentPicker("目标地区", "country", draft.country, "country", { allowedValues: Object.keys(data.environments || {}), allowCustom: false })}<label>出口地址族<select name="family"><option value="ipv4" ${draft.family === "ipv4" ? "selected" : ""}>IPv4</option><option value="ipv6" ${draft.family === "ipv6" ? "selected" : ""}>IPv6</option></select></label></div><p class="hint" data-environment></p><p class="hint" data-exit></p>${locationPicker(draft.position)}<fieldset class="region-service-options"><legend>检测服务</legend>${services.map(service => `<label class="checkbox"><input type="checkbox" name="service" value="${service.id}" ${draft.services.includes(service.id) ? "checked" : ""}>${service.name}</label>`).join("")}</fieldset><div class="region-save"><span class="hint" data-save-state role="status"></span><button type="submit" class="primary">保存</button></div></form>`;
    const form = root.querySelector("form");
    wireSearchPickers(form);
    let requestSerial = 0;
    const locate = async (coordinates, automatic = false) => {
      const serial = ++requestSerial, edits = draft.edits, family = draft.family;
      draft.locationPending = true; patch();
      try {
        const expected = node.policy?.expected_ip;
        const ip = exitAddresses(node, family).includes(expected) ? expected : exitAddresses(node, family)[0];
        const response = coordinates ? await send("/admin/ipguard/location", "POST", coordinates) : await api(`/admin/ipguard/location?${new URLSearchParams({ node: node.id, family, ip: ip || "" })}`, { signal: controller.signal });
        if (!alive() || !form.isConnected || serial !== requestSerial || draft.edits !== edits || draft.family !== family) return;
        const position = coordinates ? response : response.position;
        draft.position = position; draft.country = position.country; draft.dirty = true; draft.coordinatesDirty = false; draft.autoRegion = !coordinates; draft.edits++;
        draft.locationMessage = coordinates ? "已确认定位，保存后生效。" : `按出口 ${response.ip} 近似定位（ipwho.is），保存后生效。`;
        const country = form.querySelector('[name="country"]');
        country.value = position.country; country.setCustomValidity("");
        updateMap(position);
      } catch (error) {
        if (alive() && form.isConnected && serial === requestSerial) draft.locationMessage = `定位失败：${error.message}${automatic ? "，请手动选择地区或搜索地点。" : ""}`;
      } finally { if (serial === requestSerial) { draft.locationPending = false; if (form.isConnected) patch(); } }
    };
    const updateMap = wireLocationPicker(form, draft.position, position => locate(position), query => api(`/admin/ipguard/places?q=${encodeURIComponent(query)}`, { signal: controller.signal }));
    disposeMap = updateMap.dispose;
    form.querySelector("[data-ip-location]").onclick = () => locate(null);
    const edit = event => {
      if (["latitude", "longitude"].includes(event.target.name)) { draft.coordinatesDirty = true; draft.edits++; draft.locationMessage = "经纬度已修改，请先确认定位。"; patch(); return; }
      if (event.target.type === "search") return;
      const values = new FormData(form);
      const previousCountry = draft.country, previousFamily = draft.family;
      const next = { country: String(values.get("country")).trim(), family: values.get("family"), enabled: values.has("enabled"), auto_recover: values.has("auto_recover"), services: values.getAll("service") };
      if (["country", "family", "enabled", "auto_recover"].every(key => next[key] === draft[key]) && JSON.stringify(next.services) === JSON.stringify(draft.services)) return;
      Object.assign(draft, next, { dirty: true });
      if (draft.country !== previousCountry || draft.family !== previousFamily) { draft.position = null; draft.coordinatesDirty = false; draft.locationMessage = "地区或出口已改变，请重新定位。"; updateMap(null); }
      if (draft.country !== previousCountry) { draft.autoRegion = false; requestSerial++; draft.locationPending = false; }
      draft.edits++;
      patch();
      if (draft.family !== previousFamily && !node.policy && (draft.autoRegion || !draft.country)) locate(null, true);
    };
    form.addEventListener("input", edit);
    form.addEventListener("change", edit);
    form.onsubmit = guard(async event => {
      event.preventDefault();
      if (saving || draft.locationPending || draft.coordinatesDirty || !data.environments?.[draft.country]) return;
      if (!draft.services.length) { toast("至少选择一个服务"); return; }
      if (!canSaveExit(selected(), draft)) { toast("请先选择可用的检测出口"); return; }
      saving = true; epoch++;
      const settings = Object.fromEntries(["revision", "enabled", "auto_recover", "country", "family", "services", "position"].map(key => [key, draft[key]]));
      const edits = draft.edits;
      patch();
      try {
        const updated = await send(`/admin/ipguard/nodes/${encodeURIComponent(node.id)}`, "PUT", settings);
        if (alive()) {
          data.nodes.find(item => item.id === node.id).policy = updated;
          draft.revision = updated.revision;
          draft.dirty = draft.edits !== edits;
          toast("已保存，语言和时区按目标地区自动匹配");
        }
      } finally { saving = false; patch(); }
    });
    patch();
    if (!node.policy && !draft.country && !draft.dirty && exitAddresses(node, draft.family).length) locate(null, true);
  };
  const refresh = async () => {
    if (!alive() || loading || saving) return;
    loading = true;
    const started = epoch;
    try {
      const next = await api("/admin/ipguard", { signal: controller.signal });
      if (alive() && started === epoch && !saving) { data = next; patch(); }
    } catch (error) { if (alive()) root.querySelector("[data-region-notice]").textContent = `状态更新失败：${error.message}，稍后重试`; }
    finally { loading = false; }
  };
  root.querySelector("[data-region-node]").onchange = event => { nodeID = event.target.value; renderForm(); };
  root.querySelectorAll("[data-check]").forEach(button => { button.onclick = guard(async () => {
    saving = true; epoch++; patch();
    try { await send("/admin/ipguard/jobs", "POST", { node_id: nodeID, service: button.dataset.check }); }
    finally { saving = false; patch(); }
    await refresh();
  }); });
  const poll = async () => { await refresh(); if (alive()) timer = setTimeout(poll, 5000); };
  renderForm(); patch();
  timer = setTimeout(poll, 5000);
}
