import { api, send, toast, guard, esc, modal } from "./api.js";
import { customThemeDialog } from "./admin-theme-dialog.js";
import { registerThemes } from "./skins.js";
import { state, content, confirmAction } from "./admin-state.js";
import { addonMarket, addonScope, catalogPanel, loadMarket, marketPill, pager, sourceBits, subTabs, swatch, wireMarket } from "./admin-market.js";
const THEME_DOC_URL = "https://github.com/leeska/TZA-Probe/blob/main/docs/THEMES.md";
// 主题：内置的现代极简 + 已安装的主题包，下面是主题市场。官方的其它外观也是主题包，
// 和别人做的主题一样从市场装。CSS 主题沿用核心组件；独立前台主题在隔离文档里运行自己的 HTML / JavaScript。
//
// 每个主题旁边的「设置」打开它自己的设置弹窗：菜单项由主题开发者在 theme.json 的 settings 里按
// docs/THEMES.md「主题设置」的规范声明（开关、单选、附加包开关），这里只负责照着画、存到服务端，
// 对所有访客生效。附加包（"type": "addon"）不是外观，只给 for 写的那套主题添装饰项，所以列在那套主题
// 下面、在它的设置菜单里开关；那套主题没装时它们不生效，单独成一组提示一句。
export async function renderThemes(nodesReady) {
  const view = state.themesView === "market" ? "market" : "installed";
  // 本页不用节点列表；切页时外壳传来的节点请求一起等，顶栏在线计数随之刷新。页内重画不传。
  const [, themes, panel] = await Promise.all([nodesReady, api("/admin/themes"), view === "installed" ? api("/admin/panel").catch(() => ({})) : {}]);
  registerThemes(themes);
  // 设置菜单里有附加包开关的主题，各带一块自己的附加包市场；市场子页只取主题目录。
  const hosts = themes.filter((t) => t.type !== "addon" && (t.settings || []).some((st) => st.type === "addons"));
  const scopes = view === "market" ? ["themes"] : hosts.map((t) => addonScope(t.id));
  const updates = await loadMarket(() => renderThemes(), themes, scopes);
  if (state.tab !== "themes") return;
  const body = view === "market" ? catalogPanel("themes") : installedPanel(themes, updates, panel.default_skin);
  content().innerHTML = `<section data-part="page-heading" class="page-heading"><div><div class="eyebrow">THEMES</div><h1>主题</h1><p>访客在公开面板右上角能选的外观。只内置现代极简（默认），液态玻璃等主题从市场安装或上传 zip。</p></div><div class="heading-actions">${marketPill()}</div></section>${subTabs(view, "主题分类")}${body}`;
  content().querySelectorAll("[data-subview]").forEach((b) =>
    (b.onclick = guard(async () => {
      state.themesView = b.dataset.subview;
      await renderThemes();
    })),
  );
  wireMarket();
  if (view === "installed") {
    wireInstalled(themes, updates);
    for (const id of opened) {
      const t = themes.find((x) => x.id === id);
      if (t) showSettings(t, themes, updates);
    }
  }
}

// 展开着的设置菜单：页内重画（保存、装 / 删包）之后保持展开。
const opened = new Set();
// 已安装列表每页几套主题（设置菜单跟着它的主题走；附加包的两组各算一项，排在最后）。
const PER_PAGE = 8;
let installedPage = 1;

function installedPanel(list, updates, defaultSkin) {
  const skins = list.filter((t) => t.type !== "addon");
  const addons = list.filter((t) => t.type === "addon");
  const rows = skins.map((t) => skinRow(t, updates, defaultSkin));
  // 依附的主题已装但它的设置里没有附加包开关：附加包照样生效，列在一组里方便更新与删除。
  const loose = addons.filter((a) => skins.some((t) => t.id === a.for && !(t.settings || []).some((st) => st.type === "addons")));
  // 依附的主题被删了、附加包还在：留着它们（再装那套主题就恢复），但写明现在不生效。
  const orphans = addons.filter((a) => !skins.some((t) => t.id === a.for));
  if (loose.length) rows.push(`<div class="theme-group">${loose.map((t) => addonRow(t, updates)).join("")}</div>`);
  if (orphans.length) rows.push(`<div class="theme-group"><p class="hint">下面的附加包依附的主题没装，暂不生效；装回那套主题即恢复。</p>${orphans.map((t) => addonRow(t, updates)).join("")}</div>`);
  const pages = Math.max(1, Math.ceil(rows.length / PER_PAGE));
  installedPage = Math.min(Math.max(1, installedPage), pages);
  const shown = rows.slice((installedPage - 1) * PER_PAGE, installedPage * PER_PAGE).join("");
  return `<section data-part="panel" class="panel theme-panel"><div class="panel-head"><div><h3>已安装</h3><span class="muted">「市场」是从 TZA-CC 装的，有新版时在这里提示；「本地」是手动上传的 zip。</span></div><div class="panel-actions"><label class="button primary theme-upload">上传主题包 / 附加包<input id="theme-file" type="file" accept=".zip,application/zip" hidden></label></div></div><div class="theme-list">${shown}</div>${pager(installedPage, installedPage < pages, "installed-page", pages)}<p class="hint">主题包格式与可用的颜色变量见 <a href="${THEME_DOC_URL}" target="_blank" rel="noopener noreferrer">docs/THEMES.md</a>。同一个 id 再次上传即覆盖升级（之后按本地包对待，不再提示更新）。主题包存在数据目录的 themes/ 下，不在数据库备份里；前台默认外观在「面板设置」里选。</p></section>`;
}

function skinRow(t, updates, defaultSkin) {
  const meta = t.builtin ? t.hint || "" : [t.hint, [t.id, t.version && "v" + t.version, t.author].filter(Boolean).join(" · ")].filter(Boolean).join("\n");
  const bits = t.builtin ? { pills: '<span class="pill">内置</span>', actions: "" } : sourceBits(t, updates);
  const isDefault = t.id === defaultSkin ? '<span class="pill good">前台默认</span>' : "";
  const remove = t.builtin ? "" : `<button type="button" class="danger" data-remove-theme="${esc(t.id)}">删除</button>`;
  const open = opened.has(t.id);
  const gear = (t.settings?.length || t.settings_ui)
    ? `<button type="button" class="theme-gear${open ? " on" : ""}" data-theme-settings="${esc(t.id)}" aria-haspopup="dialog" title="${esc(t.name)}的主题设置">${GEAR}<span>设置</span></button>`
    : "";
  return `<div class="theme-row">${swatch(t.swatch)}<span class="theme-copy"><b>${esc(t.name)}</b>${t.frontend ? '<small>独立前台 · HTML / JavaScript</small>' : ""}<small>${esc(meta).replace("\n", "<br>")}</small></span>${isDefault}${bits.pills}${bits.actions}${gear}${remove}</div>`;
}

const GEAR = `<svg viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" aria-hidden="true"><circle cx="10" cy="10" r="2.6"/><path d="M10 2.8v1.8M10 15.4v1.8M2.8 10h1.8M15.4 10h1.8M4.9 4.9l1.3 1.3M13.8 13.8l1.3 1.3M4.9 15.1l1.3-1.3M13.8 6.2l1.3-1.3"/></svg>`;

// 一个开关：点它即把 value 存给 key（addons 由点击处理换算成整张关闭列表）。
const toggle = (skin, key, value, on, label) =>
  `<button type="button" class="opt-switch${on ? " on" : ""}" role="switch" aria-checked="${on}" aria-label="${esc(label)}" data-opt-skin="${esc(skin)}" data-opt-key="${esc(key)}" data-opt-value="${esc(value)}"><i></i></button>`;

// 主题的设置菜单：缩进一级挂在主题下面，按 settings 的顺序逐项画。值以服务端发来的 options 为准，
// 没存过的用 theme.json 里的 default。
function settingsMenu(t, addons, updates) {
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
    if (st.type === "addons") {
      const off = new Set(Array.isArray(opts[st.id]) ? opts[st.id] : []);
      const rows = addons.map((p) => addonRow(p, updates, toggle(t.id, st.id, p.id, !off.has(p.id), `启用 ${p.name}`))).join("");
      // 主题自己也带装饰项时多一行开关（值是主题自己的 id）；它的 default 项总是最后的兜底，关不掉。
      const own = t.decorations?.length
        ? `<div class="theme-row is-pack"><span class="theme-copy"><b>${esc(t.name)}自带</b><small>${esc(decoSummary(t))}；关掉后只用附加包里的，都匹配不上时仍由自带的默认项兜底</small></span>${toggle(t.id, st.id, t.id, !off.has(t.id), `启用${t.name}自带的装饰项`)}</div>`
        : "";
      return `<div class="opt-group"><div class="opt-row">${head}</div>${own}${rows || '<p class="hint">还没有附加包：从下面的市场添加，也可以上传自己做的附加包。</p>'}${addonMarket(t.id)}</div>`;
    }
    return "";
  };
  return `<div class="theme-group is-nested theme-settings" id="theme-settings-${esc(t.id)}" aria-label="${esc(t.name)}的主题设置">${t.settings.map(item).join("")}</div>`;
}

// 「N 个装饰项：名字（匹配的地区）、……」
const decoSummary = (t) =>
  `${(t.decorations || []).length} 个装饰项：${(t.decorations || []).map((d) => d.name + ((d.regions || []).length ? `（${d.regions.join(" / ")}）` : d.default ? "（默认）" : "")).join("、")}`;

function addonRow(t, updates, enable = "") {
  // 缩略图：前四个装饰项各自的第一张图。
  const firsts = (t.decorations || []).map((d) => Object.values(d.images || {})[0]).filter(Boolean).slice(0, 4);
  const thumbs = `<span class="pack-thumbs" aria-hidden="true">${firsts.map((f) => `<img src="/themes/${esc(t.id)}/${esc(f)}?v=${esc(t.rev || "")}" alt="" loading="lazy">`).join("")}</span>`;
  const meta = [t.id, t.version && "v" + t.version, t.author].filter(Boolean).join(" · ");
  const bits = sourceBits(t, updates);
  return `<div class="theme-row is-pack">${thumbs}<span class="theme-copy"><b>${esc(t.name)}</b><small>${esc(decoSummary(t))}<br>${esc(meta)}</small></span><span class="pill">附加包</span>${bits.pills}${bits.actions}<button type="button" class="danger" data-remove-theme="${esc(t.id)}">删除</button>${enable}</div>`;
}

function wireInstalled(list, updates, root = content()) {
  root.querySelectorAll("[data-installed-page]").forEach((b) =>
    (b.onclick = guard(async () => {
      installedPage = Number(b.dataset.installedPage);
      await renderThemes();
    })),
  );
  const input = root.querySelector("#theme-file");
  if (input) input.onchange = guard(async () => {
    const file = input.files[0];
    input.value = "";
    if (!file) return;
    const m = await api("/admin/themes", { method: "POST", body: file, headers: { "Content-Type": "application/zip" } });
    await renderThemes();
    const host = (window.__tzaThemes || []).find((t) => t.id === m.for && t.type !== "addon");
    toast(m.type === "addon" ? `附加包已安装：${m.name}${host ? `（「${host.name}」里按节点名 / 地区自动用上，也可以在节点设置里指定）` : `（要装好主题「${m.for}」才生效）`}` : `主题包已安装：${m.name}（右上角外观菜单里可以选它）`);
  });
  root.querySelectorAll("[data-theme-settings]").forEach((b) =>
    (b.onclick = () => {
      const t = list.find((x) => x.id === b.dataset.themeSettings);
      if (t?.settings_ui) customThemeDialog(t, () => renderThemes());
      else if (t) { opened.add(t.id); showSettings(t, list, updates); }
    }),
  );
  root.querySelectorAll("[data-opt-skin]").forEach((b) =>
    (b.onclick = guard(async () => {
      const { optSkin: skin, optKey: key, optValue: value } = b.dataset;
      const t = list.find((x) => x.id === skin);
      const st = t?.settings?.find((x) => x.id === key);
      let next = value;
      if (st?.type === "addons") {
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
  root.querySelectorAll("[data-remove-theme]").forEach((b) =>
    (b.onclick = () => {
      const t = list.find((s) => s.id === b.dataset.removeTheme);
      const addons = list.filter((a) => a.type === "addon" && a.for === t?.id).length;
      const message = t?.type === "addon"
        ? "删除后用到这些装饰项的节点回到自动匹配。"
        : `删除后正在用它的访客会回到默认皮肤；它若是前台默认皮肤，默认也回到现代极简。${addons ? `依附它的 ${addons} 个附加包会保留，但要等再装上这套主题才生效。` : ""}`;
      confirmAction(
        `删除${t?.type === "addon" ? "附加包" : "主题"} · ${t?.name || b.dataset.removeTheme}`,
        message,
        async () => {
          await send("/admin/themes/" + encodeURIComponent(b.dataset.removeTheme), "DELETE");
          await renderThemes();
        },
        { label: t?.type === "addon" ? "删除附加包" : "删除主题" },
      );
    }),
  );
}

function showSettings(t, list, updates) {
  const d = modal(`${t.name} · 设置`, settingsMenu(t, list.filter((x) => x.type === "addon" && x.for === t.id), updates));
  d.classList.add("theme-settings-dialog");
  d.addEventListener("close", () => { opened.delete(t.id); d.classList.remove("theme-settings-dialog"); }, { once: true });
  wireInstalled(list, updates, d);
  wireMarket(d);
}
