import { api, send, modal, guard, toast } from "./api.js";
import { registerThemes } from "./skins.js";

// The frame may only edit this theme's declared options, add-ons and node decorations.
export function customThemeDialog(theme, refresh) {
  const d = modal(`${theme.name} · 设置`, '<p class="hint" id="theme-settings-status" role="status">正在加载主题设置…</p><iframe id="theme-settings-frame" title="主题自定义设置" sandbox="allow-scripts"></iframe>');
  d.classList.add("theme-settings-dialog");
  const frame = d.querySelector("iframe"), status = d.querySelector("#theme-settings-status");
  let alive = true, busy = false, cancelPicker;
  const pickAddon = () => new Promise((resolve, reject) => {
    if (!navigator.userActivation.isActive) { reject(new Error("请点击添加角色包按钮选择文件")); return; }
    const input = document.createElement("input");
    input.type = "file"; input.accept = ".zip,application/zip"; input.hidden = true;
    const finish = (file) => { input.remove(); cancelPicker = null; file ? resolve(file) : reject(new Error("已取消选择角色包")); };
    input.onchange = () => finish(input.files[0]);
    input.oncancel = () => finish();
    cancelPicker = () => finish();
    d.append(input); input.click();
  });
  const base = `/admin/themes/${encodeURIComponent(theme.id)}`;
  const post = (value) => { if (alive) frame.contentWindow?.postMessage({ protocol: "tza-theme-settings", version: 1, ...value }, "*"); };
  const context = async () => {
    const [themes, nodes] = await Promise.all([api("/admin/themes"), api("/admin/nodes")]);
    if (!alive) return;
    registerThemes(themes);
    const current = themes.find((t) => t.id === theme.id && t.type !== "addon");
    if (!current) throw new Error("主题已被删除");
    post({ event: "context", value: { theme: current, addons: themes.filter((t) => t.type === "addon" && t.for === theme.id), nodes: nodes.map(({ id, name, region, decoration }) => ({ id, name, region, decoration })), mode: document.documentElement.dataset.theme } });
    status.hidden = true; clearTimeout(deadline);
  };
  const receive = async (event) => {
    const data = event.data;
    if (!alive || event.source !== frame.contentWindow || event.origin !== "null" || data?.protocol !== "tza-theme-settings" || data.version !== 1) return;
    if (busy) { if (data.id) post({ id: data.id, error: "上一项设置正在保存" }); return; }
    busy = true;
    try {
      if (data.event === "hello") { await context(); return; }
      if (typeof data.id !== "string" || data.id.length > 64) return;
      const args = data.args || {};
      switch (data.method) {
        case "save": await send(base + "/settings", "PUT", args.patch); break;
        case "addAddon":
        case "uploadAddon": {
          const file = data.method === "addAddon" ? await pickAddon() : args.file;
          if (!(file instanceof Blob) || file.size > 20 * 1024 * 1024) throw new Error("请选择不超过 20 MB 的角色包 zip");
          await api(base + "/addons", { method: "POST", body: file, headers: { "Content-Type": "application/zip" } }); break;
        }
        case "bindNode":
          if (typeof args.node !== "string" || typeof args.decoration !== "string") throw new Error("节点或角色无效");
          await send(base + "/bindings/" + encodeURIComponent(args.node), "PUT", { decoration: args.decoration }); break;
        default: throw new Error("不支持的主题设置方法");
      }
      if (!alive) return;
      await context(); post({ id: data.id, value: null }); toast("主题设置已保存");
    } catch (error) {
      if (data.id) post({ id: data.id, error: error.message });
      else { status.hidden = false; status.textContent = error.message; }
    } finally { busy = false; }
  };
  const deadline = setTimeout(() => { if (alive) { status.hidden = false; status.textContent = "设置界面未连接，请检查主题的 settings_ui 入口与 SDK"; } }, 12000);
  addEventListener("message", receive);
  d.addEventListener("close", () => { alive = false; cancelPicker?.(); clearTimeout(deadline); removeEventListener("message", receive); frame.remove(); d.classList.remove("theme-settings-dialog"); guard(refresh)(); }, { once: true });
  frame.src = `/themes/${theme.id}/${theme.settings_ui.entry}?v=${encodeURIComponent(theme.rev || "")}`;
}
