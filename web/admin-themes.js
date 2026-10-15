import { api, send, toast, guard, esc } from "./api.js";
import { ANIME, registerThemes } from "./skins.js";
import { content, confirmAction } from "./admin-state.js";
import { catalogPanel, loadMarket, marketPill, sourceBits, swatch, wireMarket } from "./admin-market.js";
const THEME_DOC_URL = "https://github.com/leeska/TZA-Probe/blob/main/docs/THEMES.md";
// 主题：内置的液态玻璃 + 已安装的主题包，下面是市场（主题 / 角色包）。复古印刷、现代极简、二次元是
// 官方主题包，和别人做的主题一样从市场装。角色包不是外观，只给二次元主题添角色，所以挂在二次元下面：
// 二次元没装时它们不生效，单独成一组提示一句。主题包与角色包都只含样式表、图片和字体，不能带脚本。
export async function renderThemes(nodesReady) {
  // 本页不用节点列表；切页时外壳传来的节点请求一起等，顶栏在线计数随之刷新。页内重画不传。
  const [, themes, panel] = await Promise.all([nodesReady, api("/admin/themes"), api("/admin/panel").catch(() => ({}))]);
  registerThemes(themes);
  const updates = await loadMarket(() => renderThemes(), themes, "themes");
  content().innerHTML = `<section class="page-heading"><div><div class="eyebrow">THEMES</div><h1>主题</h1><p>访客在公开面板右上角能选的外观。内置液态玻璃，其余从市场安装或上传 zip。</p></div><div class="heading-actions">${marketPill()}</div></section>${installedPanel(themes, updates, panel.default_skin)}${catalogPanel("themes")}`;
  wireMarket();
  wireInstalled(themes);
}

function installedPanel(list, updates, defaultSkin) {
  const skins = list.filter((t) => t.type !== "characters");
  const packs = list.filter((t) => t.type === "characters");
  const anime = skins.some((t) => t.id === ANIME);
  const rows = skins.map((t) => skinRow(t, updates, defaultSkin) + (t.id === ANIME ? animePacks(packs, updates) : "")).join("");
  // 二次元被删了、角色包还在：留着它们（再装二次元就恢复），但写明现在不生效。
  const orphans = !anime && packs.length
    ? `<div class="theme-group"><p class="hint">下面的角色包只给二次元主题添角色，二次元没装时不生效；装回二次元即恢复。</p>${packs.map((t) => packRow(t, updates)).join("")}</div>`
    : "";
  return `<section class="panel theme-panel"><div class="panel-head"><div><h3>已安装</h3><span class="muted">「市场」是从 TZA-CC 装的，有新版时在这里提示；「本地」是手动上传的 zip。</span></div><div class="panel-actions"><label class="button primary theme-upload">上传主题包 / 角色包<input id="theme-file" type="file" accept=".zip,application/zip" hidden></label></div></div><div class="theme-list">${rows}${orphans}</div><p class="hint">主题包格式与可用的颜色变量见 <a href="${THEME_DOC_URL}" target="_blank" rel="noopener noreferrer">docs/THEMES.md</a>。同一个 id 再次上传即覆盖升级（之后按本地包对待，不再提示更新）。主题包存在数据目录的 themes/ 下，不在数据库备份里；前台默认外观在「面板设置」里选。</p></section>`;
}

function skinRow(t, updates, defaultSkin) {
  const meta = t.builtin ? t.hint || "" : [t.hint, [t.id, t.version && "v" + t.version, t.author].filter(Boolean).join(" · ")].filter(Boolean).join("\n");
  const bits = t.builtin ? { pills: '<span class="pill">内置</span>', actions: "" } : sourceBits(t, updates);
  const isDefault = t.id === defaultSkin ? '<span class="pill good">前台默认</span>' : "";
  const remove = t.builtin ? "" : `<button type="button" class="danger" data-remove-theme="${esc(t.id)}">删除</button>`;
  return `<div class="theme-row">${swatch(t.swatch)}<span class="theme-copy"><b>${esc(t.name)}</b><small>${esc(meta).replace("\n", "<br>")}</small></span>${isDefault}${bits.pills}${bits.actions}${remove}</div>`;
}

// 二次元下面的角色包：缩进一级，没有时提示去市场的「角色包」里找。
function animePacks(packs, updates) {
  const rows = packs.map((t) => packRow(t, updates)).join("");
  return `<div class="theme-group is-nested" aria-label="二次元的角色包">${rows || '<p class="hint">二次元自带一组通用角色；更多角色在下方市场的「角色包」里，也可以上传自己做的角色包。</p>'}</div>`;
}

function packRow(t, updates) {
  const thumbs = `<span class="pack-thumbs" aria-hidden="true">${(t.characters || []).slice(0, 4).map((c) => `<img src="/themes/${esc(t.id)}/${esc(c.art)}?v=${esc(t.rev || "")}" alt="" loading="lazy">`).join("")}</span>`;
  const who = `${(t.characters || []).length} 个角色：${(t.characters || []).map((c) => c.name + ((c.regions || []).length ? `（${c.regions.join(" / ")}）` : c.default ? "（默认）" : "")).join("、")}`;
  const meta = [t.id, t.version && "v" + t.version, t.author].filter(Boolean).join(" · ");
  const bits = sourceBits(t, updates);
  return `<div class="theme-row is-pack">${thumbs}<span class="theme-copy"><b>${esc(t.name)}</b><small>${esc(who)}<br>${esc(meta)}</small></span><span class="pill">角色包</span>${bits.pills}${bits.actions}<button type="button" class="danger" data-remove-theme="${esc(t.id)}">删除</button></div>`;
}

function wireInstalled(list) {
  const input = content().querySelector("#theme-file");
  input.onchange = guard(async () => {
    const file = input.files[0];
    input.value = "";
    if (!file) return;
    const m = await api("/admin/themes", { method: "POST", body: file, headers: { "Content-Type": "application/zip" } });
    await renderThemes();
    const anime = (window.__tzaThemes || []).some((t) => t.id === ANIME);
    toast(m.type === "characters" ? `角色包已安装：${m.name}${anime ? "（二次元主题里按节点名 / 地区自动用上，也可以在节点设置里指定）" : "（要装好二次元主题才生效）"}` : `主题包已安装：${m.name}（右上角外观菜单里可以选它）`);
  });
  const packs = list.filter((t) => t.type === "characters").length;
  content().querySelectorAll("[data-remove-theme]").forEach((b) =>
    (b.onclick = () => {
      const t = list.find((s) => s.id === b.dataset.removeTheme);
      const message = t?.type === "characters"
        ? "删除后用到这些角色的节点回到自动匹配（多半是通用角色）。"
        : `删除后正在用它的访客会回到默认皮肤；它若是前台默认皮肤，默认也回到液态玻璃。${t?.id === ANIME && packs ? `已装的 ${packs} 个角色包会保留，但要等再装上二次元才生效。` : ""}`;
      confirmAction(
        `删除${t?.type === "characters" ? "角色包" : "主题"} · ${t?.name || b.dataset.removeTheme}`,
        message,
        async () => {
          await send("/admin/themes/" + encodeURIComponent(b.dataset.removeTheme), "DELETE");
          await renderThemes();
        },
        { label: t?.type === "characters" ? "删除角色包" : "删除主题" },
      );
    }),
  );
}
