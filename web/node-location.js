// 节点编辑表单里的「所在地区与定位」：节点所在地区（可按 Agent 连接的公网 IP 自动检测），以及
// 地区偏移检测要用的目标地区、出口地址族与精确定位（每台节点不同的那部分；检测哪些服务、
// 是否启用在「网络探测」的地区偏移检测任务里）。节点设置存在 /admin/ipguard/node-settings/{id}，
// 跟着节点表单一起保存。
import { api, send, esc } from "./api.js";
import { environmentPicker, wireSearchPickers } from "./environment-picker.js";
import { locationPicker, wireLocationPicker } from "./location-picker.js";
import { serverInstall, nodeEnable } from "./admin-ip-guard.js";

const fresh = (timestamp) => timestamp && Date.now() / 1000 - timestamp < 45;
const exitAddresses = (entry, family) => (entry?.egress?.enabled && fresh(entry.egress.received_at) ? (entry.egress.allowed_ips || []).filter((ip) => ip.includes(":") === (family === "ipv6")) : []);
const sameSettings = (a, b) => JSON.stringify(a || null) === JSON.stringify(b || null);

// 控件本身（不含「节点所在地区」输入框，那一格由节点表单自己画在同一节里）。
export const autoLocateHTML = (existing) =>
  `<div class="locate-row"><button type="button" class="quiet" data-auto-locate ${existing ? "" : "disabled"}>自动检测</button><small data-locate-status>${existing ? "按这台节点 Agent 连接的公网 IP 查询（ipwho.is）" : "节点接入并上报后可自动检测"}</small></div>`;
export const driftHTML = () => '<div class="node-drift" data-node-drift><p class="hint">正在读取地区偏移设置…</p></div>';

export function wireNodeLocation(form, node) {
  const box = form.querySelector("[data-node-drift]");
  const regionInput = form.elements.namedItem("region");
  const locateButton = form.querySelector("[data-auto-locate]");
  const locateStatus = form.querySelector("[data-locate-status]");
  let original = null, data = null, position = null, coordinatesPending = false, disposeMap = null, updateMap = null, loaded = !node;
  const field = (name) => box.querySelector(`[name="${name}"]`);
  const status = (text) => { const el = box.querySelector("[data-location-status]"); if (el) el.textContent = text; };
  const describe = (p) => (p ? `${p.city || p.country} · ${p.latitude.toFixed(5)}, ${p.longitude.toFixed(5)}` : "尚未设置精确定位：可在地图选点、搜索地点或按出口 IP 定位，留空按目标地区的默认时区。");

  const render = () => {
    const entry = data.nodes.find((n) => n.id === node.id);
    const task = (data.tasks || []).find((t) => t.node_ids.includes(node.id));
    const waiting = data.pending?.[node.id];
    original = data.node_settings?.[node.id] || null;
    position = original?.position || null;
    const family = original?.family || ((entry?.egress?.allowed_ips || []).some((ip) => !ip.includes(":")) || !entry?.egress?.allowed_ips?.length ? "ipv4" : "ipv6");
    const membership = task ? `所属地区偏移检测任务：<b>${esc(task.name)}</b>${task.enabled ? "" : "（已停用）"}${waiting && original ? ` · 暂未生效：${esc(waiting)}` : ""}` : "还没有加入地区偏移检测任务，在「网络探测 → 创建任务 → 地区偏移检测」里选上这台节点后才会检测。";
    const setup = !data.worker_configured ? `<p class="hint">检测浏览器还没安装，设置可以先保存。在 Server 主机上执行：<code>${esc(serverInstall)}</code></p>` : !(entry?.egress?.enabled && fresh(entry.egress.received_at)) ? `<p class="hint">这台节点的检测出口还没开通。在节点上执行：<code>${esc(nodeEnable())}</code></p>` : "";
    box.innerHTML = `<h4>地区偏移检测</h4><p class="hint">${membership}</p>${setup}<div class="form-grid">${environmentPicker("目标地区（留空不检测）", "drift_country", original?.country || "", "country", { allowedValues: Object.keys(data.environments || {}), allowCustom: false, required: false })}<label>出口地址族<select name="drift_family"><option value="ipv4" ${family === "ipv4" ? "selected" : ""}>IPv4</option><option value="ipv6" ${family === "ipv6" ? "selected" : ""}>IPv6</option></select></label></div><p class="hint" data-drift-exit></p>${locationPicker(position)}`;
    wireSearchPickers(box);
    status(describe(position));
    const exitHint = () => {
      const addresses = exitAddresses(entry, field("drift_family").value);
      box.querySelector("[data-drift-exit]").textContent = addresses.length ? `检测出口：${addresses.join("、")}` : entry?.egress?.enabled && fresh(entry.egress.received_at) ? "所选地址族的检测出口尚未开通；可以先保存，开通后自动生效。" : "节点检测出口未连接（节点上执行带 --ipguard 的接入命令开通）；可以先保存，连接后自动生效。";
      box.querySelector("[data-ip-location]").disabled = !addresses.length;
    };
    const confirm = async (coordinates) => {
      status("正在查询定位…");
      try {
        const p = await send("/admin/ipguard/location", "POST", coordinates);
        setPosition(p, "已确认定位，随节点一起保存。");
      } catch (error) { status(`定位失败：${error.message}`); }
    };
    updateMap = wireLocationPicker(box, position, confirm, (query) => api(`/admin/ipguard/places?q=${encodeURIComponent(query)}`));
    disposeMap = updateMap.dispose;
    box.querySelector("[data-ip-location]").onclick = async () => {
      const familyValue = field("drift_family").value;
      const ip = exitAddresses(entry, familyValue)[0] || "";
      status("正在按出口 IP 定位…");
      try {
        const out = await api(`/admin/ipguard/location?${new URLSearchParams({ node: node.id, family: familyValue, ip })}`);
        setPosition(out.position, `按出口 ${out.ip} 近似定位（ipwho.is），随节点一起保存。`);
      } catch (error) { status(`定位失败：${error.message}`); }
    };
    for (const name of ["latitude", "longitude"]) field(name).addEventListener("input", () => { coordinatesPending = true; });
    field("drift_country").addEventListener("change", () => { if (position && position.country !== field("drift_country").value) { position = null; updateMap(null); status("目标地区已改变，定位已清空。"); } });
    field("drift_family").addEventListener("change", exitHint);
    exitHint();
  };

  const setPosition = (p, message) => {
    position = p;
    coordinatesPending = false;
    const country = field("drift_country");
    country.value = p.country;
    country.setCustomValidity("");
    country.dispatchEvent(new Event("input", { bubbles: true }));
    updateMap?.(p);
    status(`${describe(p)} · ${message}`);
  };

  if (node) {
    api("/admin/ipguard")
      .then((value) => { data = value; loaded = true; if (box.isConnected) render(); })
      .catch((error) => { box.innerHTML = `<h4>地区偏移检测</h4><p class="hint">读取失败：${esc(error.message)}。这次保存不会改动地区偏移设置。</p>`; });
  } else {
    box.innerHTML = '<h4>地区偏移检测</h4><p class="hint">节点接入后，可以在这里设置地区偏移检测的目标地区与定位。</p>';
  }

  // 自动检测：填「节点所在地区」；地区偏移的目标地区还空着且这个国家可以作为目标时，一并填上目标地区与定位。
  if (locateButton && node) locateButton.onclick = async () => {
    locateButton.disabled = true;
    locateStatus.textContent = "正在检测…";
    try {
      const place = await api(`/admin/nodes/${encodeURIComponent(node.id)}/locate`);
      regionInput.value = place.region;
      regionInput.dispatchEvent(new Event("input", { bubbles: true }));
      regionInput.dispatchEvent(new Event("change", { bubbles: true }));
      const where = [place.country, place.province, place.city].filter((x, i, all) => x && all.indexOf(x) === i).join(" · ");
      let extra = "";
      const country = data && field("drift_country");
      if (country && !country.value && place.supported) {
        setPosition({ latitude: place.latitude, longitude: place.longitude, country: place.country_code, city: place.city, timezone: place.timezone }, "按节点公网 IP 近似定位，随节点一起保存。");
        extra = "，并填上了地区偏移的目标地区与定位";
      }
      locateStatus.textContent = `${place.ip} 位于 ${where}：已填为「${place.region}」${extra}。保存后生效。`;
    } catch (error) {
      locateStatus.textContent = `检测失败：${error.message}`;
    } finally { locateButton.disabled = false; }
  };

  return {
    // 保存前检查：手填了经纬度却没点「确认定位」时拦下。
    check() {
      if (coordinatesPending) return "地区偏移的经纬度改过但还没点「确认定位」";
      return "";
    },
    // 节点保存成功后调用：有变化才写，清空目标地区即删除设置。回一句提示（没有就空串）。
    async save(id) {
      if (!loaded || !data) return "";
      const country = field("drift_country")?.value.trim() || "";
      const next = country ? { country, family: field("drift_family").value, ...(position && position.country === country ? { position } : {}) } : null;
      const before = original ? { country: original.country, family: original.family, ...(original.position ? { position: original.position } : {}) } : null;
      if (sameSettings(next, before)) return "";
      if (!next) {
        await send(`/admin/ipguard/node-settings/${encodeURIComponent(id)}`, "DELETE");
        return "已清除地区偏移设置";
      }
      const out = await send(`/admin/ipguard/node-settings/${encodeURIComponent(id)}`, "PUT", next);
      return out?.pending ? `地区偏移设置已保存，暂未生效：${out.pending}` : "";
    },
    dispose() { disposeMap?.(); },
  };
}
