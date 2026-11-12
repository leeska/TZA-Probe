import { esc } from "./api.js";

export function pluginFields(plugin, current) {
  const values = current?.plugin_settings || {};
  const stored = current?.plugin_secrets || {};
  const fields = (plugin.settings || []).map(item => {
    const name = "ps_" + item.key;
    const secret = item.secret || item.type === "secret";
    const kept = secret && stored[item.key];
    const required = item.required && !kept ? " required" : "";
    const label = `<span>${esc(item.label)}${item.required ? "" : '<span class="muted">（选填）</span>'}</span>`;
    const help = item.help ? `<small class="field-help">${esc(item.help)}</small>` : "";
    let control;
    if (secret) {
      control = `<input name="${esc(name)}" type="password" autocomplete="new-password" placeholder="${esc(kept ? "已保存，留空沿用" : item.placeholder || "")}"${required}>`;
    } else if (item.type === "select") {
      const now = values[item.key] ?? item.default ?? "";
      control = `<select name="${esc(name)}"${required}>${item.required ? "" : '<option value="">不设置</option>'}${(item.options || []).map(option => `<option value="${esc(option.value)}"${option.value === now ? " selected" : ""}>${esc(option.label)}</option>`).join("")}</select>`;
    } else {
      const value = values[item.key] ?? item.default ?? "";
      control = `<input name="${esc(name)}" type="${item.type === "url" ? "url" : "text"}" value="${esc(value)}" placeholder="${esc(item.placeholder || "")}" autocomplete="off" spellcheck="false"${required}>`;
    }
    return `<label>${label}${control}${help}</label>`;
  }).join("");
  const permissions = (plugin.permissions || []).map(permission => `<li>${esc(permission)}</li>`).join("");
  return `${fields || '<p class="hint">这个插件不需要设置。</p>'}<p class="hint">${esc(plugin.hint || plugin.name)}${plugin.version ? ` · v${esc(plugin.version)}` : ""}${plugin.source === "market" ? " · 市场" : " · 本地上传"}</p>${permissions ? `<ul class="plugin-perms">${permissions}</ul>` : ""}`;
}

export function pluginSettings(form, plugin) {
  const data = new FormData(form);
  return Object.fromEntries((plugin.settings || []).map(item => [item.key, String(data.get("ps_" + item.key) ?? "").trim()]));
}
