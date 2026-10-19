import { api, send, toast, guard, esc } from "./api.js";
import { state, content, confirmAction } from "./admin-state.js";
import { catalogPanel, confirmPlugin, glyph, kindName, loadMarket, marketPill, pager, pluginToast, sourceBits, subTabs, wireMarket } from "./admin-market.js";
const PLUGIN_DOC_URL = "https://github.com/leeska/TZA-Probe/blob/main/docs/PLUGINS.md";
// 插件：声明式的通知渠道与探测目标集。分两个子页：已安装（本地上传、删除、市场装的有更新时提示）
// 与市场。插件只含配置与模板，由 Probe 解释执行，装之前先列出它能做的事请管理员确认。
export async function renderPlugins(nodesReady) {
  const view = state.pluginsView === "market" ? "market" : "installed";
  const [, plugins] = await Promise.all([nodesReady, api("/admin/plugins").catch(() => [])]);
  const updates = await loadMarket(() => renderPlugins(), plugins, view === "market" ? "plugins" : "");
  const body = view === "market" ? catalogPanel("plugins") : installedPanel(plugins, updates);
  content().innerHTML = `<section data-part="page-heading" class="page-heading"><div><div class="eyebrow">PLUGINS</div><h1>插件</h1><p>扩展告警通知渠道，或为探测任务添加目标。到市场挑选插件，在已安装中管理。</p></div><div class="heading-actions">${marketPill()}</div></section>${subTabs(view, "插件分类")}${body}`;
  content().querySelectorAll("[data-subview]").forEach((b) =>
    (b.onclick = guard(async () => {
      state.pluginsView = b.dataset.subview;
      await renderPlugins();
    })),
  );
  wireMarket();
  if (view === "installed") wireInstalled();
}

// 已安装列表每页几个插件。
const PER_PAGE = 10;
let installedPage = 1;

function installedPanel(list, updates) {
  const pages = Math.max(1, Math.ceil(list.length / PER_PAGE));
  installedPage = Math.min(Math.max(1, installedPage), pages);
  const rows = list.slice((installedPage - 1) * PER_PAGE, installedPage * PER_PAGE).map((p) => {
    const bits = sourceBits(p, updates);
    const meta = [p.id, p.version && "v" + p.version, p.author, kindName(p.kind)].filter(Boolean).join(" · ");
    return `<div class="theme-row">${glyph(p.kind)}<span class="theme-copy"><b>${esc(p.name)}</b><small>${esc(p.hint || "")}${p.hint ? "<br>" : ""}${esc(meta)}</small></span>${bits.pills}${bits.actions}<button type="button" class="danger" data-remove-plugin="${esc(p.id)}">删除</button></div>`;
  }).join("");
  return `<section data-part="panel" class="panel theme-panel"><div class="panel-head"><div><h3>已安装</h3><span class="muted">管理本机插件，市场安装的插件有新版时会在这里提示。</span></div><div class="panel-actions"><label class="button theme-upload">上传插件<input id="plugin-file" type="file" accept=".zip,application/zip" hidden></label></div></div><div class="theme-list">${rows || '<div class="market-empty"><b>还没有安装插件</b><p>添加通知渠道，或为任务添加探测目标。</p><button type="button" class="primary" data-subview="market">浏览插件市场</button></div>'}</div>${pager(installedPage, installedPage < pages, "installed-page", pages)}<details class="market-upload-help"><summary>手动上传与开发文档</summary><p class="hint">插件格式见 <a href="${PLUGIN_DOC_URL}" target="_blank" rel="noopener noreferrer">docs/PLUGINS.md</a>。本地上传同一个 id 会替换市场装的插件（之后按本地包对待，不再提示更新）；正被用作通知渠道的插件要先在「告警与通知」换掉渠道才能删除。</p></details></section>`;
}

function wireInstalled() {
  content().querySelectorAll("[data-installed-page]").forEach((b) =>
    (b.onclick = guard(async () => {
      installedPage = Number(b.dataset.installedPage);
      await renderPlugins();
    })),
  );
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
