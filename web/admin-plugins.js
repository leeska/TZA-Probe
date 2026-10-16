import { api, send, toast, guard, esc } from "./api.js";
import { state, content, confirmAction } from "./admin-state.js";
import { catalogPanel, confirmPlugin, glyph, kindName, loadMarket, marketPill, pluginToast, sourceBits, wireMarket } from "./admin-market.js";
const PLUGIN_DOC_URL = "https://github.com/leeska/TZA-Probe/blob/main/docs/PLUGINS.md";
// 插件：声明式的通知渠道与探测目标集。已安装的在上面（本地上传、删除、市场装的有更新时提示），
// 下面是市场。插件只含配置与模板，由 Probe 解释执行，装之前先列出它能做的事请管理员确认。
export async function renderPlugins(nodesReady) {
  const [, plugins] = await Promise.all([nodesReady, api("/admin/plugins").catch(() => [])]);
  const updates = await loadMarket(() => renderPlugins(), plugins, "plugins");
  content().innerHTML = `<section data-part="page-heading" class="page-heading"><div><div class="eyebrow">PLUGINS</div><h1>插件</h1><p>通知渠道（飞书、钉钉、ntfy 等）与探测目标集。通知插件装好后在「告警与通知 → 配置出口」选用；目标集出现在新建任务的目标目录里。</p></div><div class="heading-actions">${marketPill()}</div></section>${installedPanel(plugins, updates)}${catalogPanel("plugins")}`;
  wireMarket();
  wireInstalled();
}

function installedPanel(list, updates) {
  const rows = list.map((p) => {
    const bits = sourceBits(p, updates);
    const meta = [p.id, p.version && "v" + p.version, p.author, kindName(p.kind)].filter(Boolean).join(" · ");
    return `<div class="theme-row">${glyph(p.kind)}<span class="theme-copy"><b>${esc(p.name)}</b><small>${esc(p.hint || "")}${p.hint ? "<br>" : ""}${esc(meta)}</small></span>${bits.pills}${bits.actions}<button type="button" class="danger" data-remove-plugin="${esc(p.id)}">删除</button></div>`;
  }).join("");
  return `<section data-part="panel" class="panel theme-panel"><div class="panel-head"><div><h3>已安装</h3><span class="muted">「市场」是从 TZA-CC 装的，有新版时在这里提示；「本地」是手动上传的 zip。</span></div><div class="panel-actions"><label class="button primary theme-upload">上传插件<input id="plugin-file" type="file" accept=".zip,application/zip" hidden></label></div></div><div class="theme-list">${rows || '<p class="muted">还没有安装插件。</p>'}</div><p class="hint">插件格式见 <a href="${PLUGIN_DOC_URL}" target="_blank" rel="noopener noreferrer">docs/PLUGINS.md</a>。本地上传同一个 id 会替换市场装的插件（之后按本地包对待，不再提示更新）；正被用作通知渠道的插件要先在「告警与通知」换掉渠道才能删除。</p></section>`;
}

function wireInstalled() {
  const file = content().querySelector("#plugin-file");
  file.onchange = guard(async () => {
    const zip = file.files[0];
    file.value = "";
    if (!zip) return;
    const upload = (path) => api(path, { method: "POST", body: zip, headers: { "Content-Type": "application/zip" } });
    const preview = await upload("/admin/plugins/inspect");
    confirmPlugin(preview, async () => {
      const m = await upload("/admin/plugins");
      document.querySelector("#dialog")?.close();
      await renderPlugins();
      toast(pluginToast(m));
    }, "确认安装");
  });
  content().querySelectorAll("[data-remove-plugin]").forEach((b) =>
    (b.onclick = () =>
      confirmAction(`删除插件 · ${b.dataset.removePlugin}`, "删除后通知插件不再出现在渠道列表里，目标集的目标从任务目录里消失（已经建好的任务照常运行）。", async () => {
        await send("/admin/plugins/" + encodeURIComponent(b.dataset.removePlugin), "DELETE");
        state.catalog = null;
        await renderPlugins();
        toast("插件已删除");
      }, { label: "删除插件" })),
  );
}
