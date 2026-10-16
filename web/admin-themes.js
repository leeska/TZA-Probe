import { api, send, toast, guard, esc } from "./api.js";
import { ANIME, registerThemes } from "./skins.js";
import { content, confirmAction } from "./admin-state.js";
import { catalogPanel, characterMarket, loadMarket, marketPill, sourceBits, swatch, wireMarket } from "./admin-market.js";
const THEME_DOC_URL = "https://github.com/leeska/TZA-Probe/blob/main/docs/THEMES.md";
// 主题：内置的现代极简、液态玻璃 + 已安装的主题包，下面是市场（主题 / 角色包）。复古印刷、二次元是
// 官方主题包，和别人做的主题一样从市场装。主题包与角色包都只含样式表、图片和字体，不能带脚本。
//
// 每个主题旁边的「设置」展开它自己的设置菜单：菜单项由主题开发者在 theme.json 的 settings 里按
// docs/THEMES.md「主题设置」的规范声明（开关、单选，二次元还有「角色包」），这里只负责照着画、
// 存到服务端，对所有访客生效。角色包不是外观，只给二次元添角色，所以在二次元的设置菜单里开关；
// 二次元没装时它们不生效，单独成一组提示一句。
export async function renderThemes(nodesReady) {
  // 本页不用节点列表；切页时外壳传来的节点请求一起等，顶栏在线计数随之刷新。页内重画不传。
  const [, themes, panel] = await Promise.all([nodesReady, api("/admin/themes"), api("/admin/panel").catch(() => ({}))]);
  registerThemes(themes);
  const anime = themes.some((t) => t.id === ANIME && t.type !== "characters");
  const updates = await loadMarket(() => renderThemes(), themes, anime ? ["themes", "characters"] : "themes");
  content().innerHTML = `<section class="page-heading"><div><div class="eyebrow">THEMES</div><h1>主题</h1><p>访客在公开面板右上角能选的外观。内置现代极简（默认）与液态玻璃，其余从市场安装或上传 zip。</p></div><div class="heading-actions">${marketPill()}</div></section>${installedPanel(themes, updates, panel.default_skin)}${catalogPanel("themes")}`;
  wireMarket();
  wireInstalled(themes);
}

// 展开着的设置菜单：页内重画（保存、装 / 删包）之后保持展开。
const opened = new Set();

function installedPanel(list, updates, defaultSkin) {
  const skins = list.filter((t) => t.type !== "characters");
  const packs = list.filter((t) => t.type === "characters");
  const anime = skins.some((t) => t.id === ANIME);
  const rows = skins.map((t) => skinRow(t, updates, defaultSkin) + settingsMenu(t, packs, updates)).join("");
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
  const open = opened.has(t.id);
  const gear = t.settings?.length
    ? `<button type="button" class="theme-gear${open ? " on" : ""}" data-theme-settings="${esc(t.id)}" aria-expanded="${open}" aria-controls="theme-settings-${esc(t.id)}" title="${esc(t.name)}的主题设置">${GEAR}<span>设置</span></button>`
    : "";
  return `<div class="theme-row">${swatch(t.swatch)}<span class="theme-copy"><b>${esc(t.name)}</b><small>${esc(meta).replace("\n", "<br>")}</small></span>${isDefault}${bits.pills}${bits.actions}${gear}${remove}</div>`;
}

const GEAR = `<svg viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" aria-hidden="true"><circle cx="10" cy="10" r="2.6"/><path d="M10 2.8v1.8M10 15.4v1.8M2.8 10h1.8M15.4 10h1.8M4.9 4.9l1.3 1.3M13.8 13.8l1.3 1.3M4.9 15.1l1.3-1.3M13.8 6.2l1.3-1.3"/></svg>`;

// 一个开关：点它即把 value 存给 key（characters 由点击处理换算成整张关闭列表）。
const toggle = (skin, key, value, on, label) =>
  `<button type="button" class="opt-switch${on ? " on" : ""}" role="switch" aria-checked="${on}" aria-label="${esc(label)}" data-opt-skin="${esc(skin)}" data-opt-key="${esc(key)}" data-opt-value="${esc(value)}"><i></i></button>`;

// 主题的设置菜单：缩进一级挂在主题下面，按 settings 的顺序逐项画。值以服务端发来的 options 为准，
// 没存过的用 theme.json 里的 default。
function settingsMenu(t, packs, updates) {
  if (!t.settings?.length) return "";
  const opts = t.options || {};
  const item = (st) => {
    const head = `<span class="theme-copy"><b>${esc(st.label)}</b>${st.hint ? `<small>${esc(st.hint)}</small>` : ""}</span>`;
    if (st.type === "toggle") {
      const on = (opts[st.id] === "on" || opts[st.id] === "off" ? opts[st.id] : st.default || "off") === "on";
      return `<div class="opt-row">${head}${toggle(t.id, st.id, on ? "off" : "on", on, st.label)}</div>`;
    }
    if (st.type === "choice") {
      const v = (st.options || []).some((o) => o.value === opts[st.id]) ? opts[st.id] : st.default || st.options?.[0]?.value;
      return `<div class="opt-row">${head}<div class="segmented" role="radiogroup" aria-label="${esc(st.label)}">${(st.options || [])
        .map((o) => `<button type="button" role="radio" aria-checked="${o.value === v}" class="${o.value === v ? "active" : ""}" data-opt-skin="${esc(t.id)}" data-opt-key="${esc(st.id)}" data-opt-value="${esc(o.value)}">${esc(o.label)}</button>`)
        .join("")}</div></div>`;
    }
    if (st.type === "characters") {
      const off = new Set(Array.isArray(opts[st.id]) ? opts[st.id] : []);
      const rows = packs.map((p) => packRow(p, updates, toggle(t.id, st.id, p.id, !off.has(p.id), `启用 ${p.name}`))).join("");
      return `<div class="opt-group"><div class="opt-row">${head}</div><div class="theme-row is-pack"><span class="theme-copy"><b>内置角色</b><small>主题自带的线路机专属立绘；关掉后这些节点也按地区 / 默认挑角色，最后落到通用角色</small></span>${toggle(t.id, st.id, "builtin", !off.has("builtin"), "启用内置角色")}</div>${rows || '<p class="hint">还没有角色包：从下面的市场添加，也可以上传自己做的角色包。</p>'}${characterMarket()}</div>`;
    }
    return "";
  };
  return `<div class="theme-group is-nested theme-settings" id="theme-settings-${esc(t.id)}" aria-label="${esc(t.name)}的主题设置"${opened.has(t.id) ? "" : " hidden"}>${t.settings.map(item).join("")}</div>`;
}

function packRow(t, updates, enable = "") {
  const thumbs = `<span class="pack-thumbs" aria-hidden="true">${(t.characters || []).slice(0, 4).map((c) => `<img src="/themes/${esc(t.id)}/${esc(c.art)}?v=${esc(t.rev || "")}" alt="" loading="lazy">`).join("")}</span>`;
  const who = `${(t.characters || []).length} 个角色：${(t.characters || []).map((c) => c.name + ((c.regions || []).length ? `（${c.regions.join(" / ")}）` : c.default ? "（默认）" : "")).join("、")}`;
  const meta = [t.id, t.version && "v" + t.version, t.author].filter(Boolean).join(" · ");
  const bits = sourceBits(t, updates);
  return `<div class="theme-row is-pack">${thumbs}<span class="theme-copy"><b>${esc(t.name)}</b><small>${esc(who)}<br>${esc(meta)}</small></span><span class="pill">角色包</span>${bits.pills}${bits.actions}<button type="button" class="danger" data-remove-theme="${esc(t.id)}">删除</button>${enable}</div>`;
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
  content().querySelectorAll("[data-theme-settings]").forEach((b) =>
    (b.onclick = () => {
      const id = b.dataset.themeSettings;
      const open = !opened.has(id);
      if (open) opened.add(id);
      else opened.delete(id);
      document.getElementById(`theme-settings-${id}`).hidden = !open;
      b.classList.toggle("on", open);
      b.setAttribute("aria-expanded", String(open));
    }),
  );
  content().querySelectorAll("[data-opt-skin]").forEach((b) =>
    (b.onclick = guard(async () => {
      const { optSkin: skin, optKey: key, optValue: value } = b.dataset;
      const t = list.find((x) => x.id === skin);
      const st = t?.settings?.find((x) => x.id === key);
      let next = value;
      if (st?.type === "characters") {
        const off = new Set(Array.isArray(t.options?.[key]) ? t.options[key] : []);
        if (off.has(value)) off.delete(value);
        else off.add(value);
        next = [...off];
      }
      await send(`/admin/themes/${encodeURIComponent(skin)}/settings`, "PUT", { [key]: next });
      await renderThemes();
      toast(`已保存「${t?.name || skin}」的设置，访客刷新页面后生效`);
    })),
  );
  const packs = list.filter((t) => t.type === "characters").length;
  content().querySelectorAll("[data-remove-theme]").forEach((b) =>
    (b.onclick = () => {
      const t = list.find((s) => s.id === b.dataset.removeTheme);
      const message = t?.type === "characters"
        ? "删除后用到这些角色的节点回到自动匹配（多半是通用角色）。"
        : `删除后正在用它的访客会回到默认皮肤；它若是前台默认皮肤，默认也回到现代极简。${t?.id === ANIME && packs ? `已装的 ${packs} 个角色包会保留，但要等再装上二次元才生效。` : ""}`;
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
