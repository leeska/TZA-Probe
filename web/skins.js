// 外观风格（皮肤）：内置的现代极简（默认）、液态玻璃，加上已安装的主题包（官方的其它外观也是
// 主题包，从后台「主题」页的市场安装；也可以上传别的主题包）。
// 明暗（data-theme）仍由 theme.js 决定，风格（data-skin）由这里决定，两者正交：
// 同一套皮肤在浅色与深色下各有一份令牌。偏好存 localStorage 的 tza-skin，
// theme-boot.js 在样式表生效前已把 data-skin 写到 <html> 上，避免首屏闪一下默认皮肤。
//
// 清单的唯一来源是服务端（internal/theme）：/theme-default.js 把它放在 window.__tzaThemes 上，
// 后台装 / 删主题包之后由 registerThemes() 换成新清单。没有清单时（jsdom 测试、旧缓存）只认内置的两套。
const KEY = "tza-skin";

// 顺序即菜单顺序，第一套是内置默认（与 internal/theme 的 Builtins、model.DefaultPanelSkin 一致）。
const BUILTIN = [
  { id: "shadcn", name: "现代极简", hint: "中性灰 · 发丝描边 · 小圆角", builtin: true, chrome: { light: "#ffffff", dark: "#0a0a0a" }, swatch: ["#f5f5f5", "#ffffff", "#171717"] },
  { id: "glass", name: "液态玻璃", hint: "毛玻璃 · 圆角 · 柔和阴影", builtin: true, chrome: { light: "#edf2fb", dark: "#0f141c" }, swatch: ["#eef3fc", "#ffffff", "#0071e3"] },
];

// 附加包（type: "addon"）不是可选的外观，它只给 for 写的那套主题添装饰项（decorations.js 读它们）。
const registry = (list) => {
  const skins = Array.isArray(list) ? list.filter((t) => t.type !== "addon") : [];
  return skins.length ? skins : BUILTIN;
};

// 导出的是活绑定：registerThemes() 换掉之后，import 它的模块（后台设置页）读到的就是新清单。
export let SKINS = registry(window.__tzaThemes);

export function registerThemes(list) {
  SKINS = registry(list);
  // 装饰项的挑选（decorations.js）现读 window.__tzaThemes，一起换成新清单。
  if (Array.isArray(list)) window.__tzaThemes = list;
  // 后台改了主题设置会重新登记清单：当前主题的 data-opt-* 跟着换。
  applyOptions();
  for (const b of document.querySelectorAll("[data-skin-toggle]")) b.hidden = SKINS.length < 2;
}

const byId = (id) => SKINS.find((s) => s.id === id);
// 主题包的名字与说明来自上传的 theme.json：拼进 HTML 之前一律转义。
const esc = (v) =>
  String(v ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);

// 后台设的默认皮肤（/theme-default.js 写在 window.__tzaDefault 上）；认不出来就是内置默认。
const defaultSkin = () => (byId(window.__tzaDefault?.skin) ? window.__tzaDefault.skin : BUILTIN[0].id);

export function skinPreference() {
  let v = null;
  try {
    v = localStorage.getItem(KEY);
  } catch {}
  return byId(v) ? v : defaultSkin();
}

// 当前皮肤在 theme.json 里声明的能力（features）与布局（layout），见 docs/THEMES.md。
// 读 <html data-skin>（首屏由 theme-boot.js 写好，运行中由 applySkin 换），认不出来就按偏好。
const currentSkin = () => byId(document.documentElement.dataset.skin) || byId(skinPreference());
export const skinLayout = () => currentSkin()?.layout || {};

// 密度写在 <html data-density> 上（styles.css 按它收紧卡片）；没声明就不写。规则与 theme-boot.js 相同。
function applyLayout(skin) {
  const density = byId(skin)?.layout?.density;
  if (density === "compact" || density === "cozy") document.documentElement.dataset.density = density;
  else delete document.documentElement.dataset.density;
}

// 浏览器地址栏 / 状态栏的底色：每套皮肤在清单里自带一对（浅色 / 深色）。
export function skinChrome() {
  return byId(skinPreference())?.chrome || BUILTIN[0].chrome;
}

// 已安装主题包的样式表：只有正在用的那套生效。首屏由 theme-css.js 同步挂上；运行中切换时
// 在这里补挂缺的、停用别的。有要加载的就返回等它们加载完的 Promise（先切 data-skin 的话会先按
// 默认样式画一帧）；内置皮肤或已经挂好的返回 null，调用方当场切换。
function syncThemeCSS(skin) {
  for (const l of document.querySelectorAll("link[data-theme-css]")) l.disabled = l.dataset.themeCss !== skin;
  const t = byId(skin);
  if (!t || t.builtin || !t.css?.length) return null;
  const have = [...document.querySelectorAll("link[data-theme-css]")].filter((l) => l.dataset.themeCss === skin);
  const missing = t.css.filter((href) => !have.some((l) => l.getAttribute("href") === href));
  if (!missing.length) return null;
  return Promise.all(
    missing.map(
      (href) =>
        new Promise((done) => {
          const l = document.createElement("link");
          l.rel = "stylesheet";
          l.dataset.themeCss = skin;
          l.href = href;
          l.onload = l.onerror = done;
          document.head.append(l);
        }),
    ),
  );
}

// 基础层：声明了 "base": "plain" 的主题包不要液态玻璃的材质层（glass.css），其余的都要。
// 首屏由 theme-css.js 定好；样式表一直在页面上，切换只改 disabled，同步生效。
function syncBaseCSS(skin) {
  const glass = document.querySelector('link[data-base-css="glass"]');
  if (glass) glass.disabled = byId(skin)?.base === "plain";
}

// 与 theme.js 各管一半：这里只管 data-skin、按钮文字与 meta 底色，明暗由 applyTheme 负责。
export function applySkin(skin = skinPreference()) {
  syncThemeCSS(skin);
  syncBaseCSS(skin);
  document.documentElement.dataset.skin = skin;
  applyOptions(skin);
  applyLayout(skin);
  const label = title(skin);
  for (const b of document.querySelectorAll("[data-skin-toggle]")) {
    b.innerHTML = inner(skin);
    b.title = label;
    b.setAttribute("aria-label", label);
  }
  paintChrome();
  closeMenu();
  // 依赖皮肤的渲染（板面段落、卡片装饰）在这里重做，而不是每秒轮询。
  document.dispatchEvent(new CustomEvent("tza:skin", { detail: skin }));
}

// ---- 主题设置（见 docs/THEMES.md「主题设置」）：主题在 theme.json 里声明菜单，管理员在后台
// 「主题」页调，值随清单发来（options）。正在用的那套的 toggle / choice 写成 <html data-opt-<id>>；
// addons 是关掉的装饰来源（附加包 id；主题自己的 id 代表它自带的装饰项），由 decorations.js 经 disabledSources() 读。
export function skinOption(skin, key) {
  const t = (window.__tzaThemes || SKINS).find((x) => x.id === skin);
  const st = (t?.settings || []).find((x) => x.id === key);
  if (!st) return undefined;
  const v = t.options?.[key];
  if (st.type === "addons") return Array.isArray(v) ? v.filter((x) => typeof x === "string") : [];
  if (st.type === "toggle") return v === "on" || v === "off" ? v : st.default || "off";
  return (st.options || []).some((o) => o.value === v) ? v : st.default || st.options?.[0]?.value;
}

// 这套主题在设置里关掉的装饰来源；没有 addons 设置项就是全开。
export function disabledSources(skin) {
  const t = (window.__tzaThemes || SKINS).find((x) => x.id === skin);
  const st = (t?.settings || []).find((x) => x.type === "addons");
  return new Set(st ? skinOption(skin, st.id) : []);
}

export function applyOptions(skin = skinPreference()) {
  const root = document.documentElement;
  for (const name of root.getAttributeNames()) if (name.startsWith("data-opt-")) root.removeAttribute(name);
  for (const st of byId(skin)?.settings || []) if (st.type === "toggle" || st.type === "choice") root.setAttribute(`data-opt-${st.id}`, skinOption(skin, st.id));
}

function paintChrome() {
  const c = skinChrome();
  const content = document.documentElement.dataset.theme === "dark" ? c.dark : c.light;
  for (const m of document.querySelectorAll('meta[name="theme-color"]')) m.content = content;
}

export function setSkin(id) {
  if (!byId(id)) return;
  try {
    if (id === defaultSkin()) localStorage.removeItem(KEY);
    else localStorage.setItem(KEY, id);
  } catch {}
  const pending = syncThemeCSS(id);
  if (pending) pending.then(() => applySkin(id));
  else applySkin(id);
}

const ICON = `<svg viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M10 3.2c-3.9 0-7 2.5-7 5.6 0 1.9 1.6 3 3.4 3h1.1c.9 0 1.5.9 1 1.7-.6 1 .1 2.3 1.4 2.3 3.9 0 7-2.6 7-5.8S13.9 3.2 10 3.2Z"/><circle cx="7.4" cy="8.2" r="1"/><circle cx="11" cy="7.2" r="1"/></svg>`;

const nameOf = (id) => byId(id)?.name || id;
const inner = (id) => `${ICON}<span>${esc(nameOf(id))}</span>`;
const title = (id) => `外观风格：${nameOf(id)}，点击选择`;

// 只有一套可选（没装任何主题包）时藏起切换按钮：一个选项的菜单没有意义。按钮照常输出，
// 后台装 / 删主题包后 registerThemes 只切显隐，不用重画侧栏。
export function skinButton(cls = "") {
  const id = skinPreference();
  return `<button type="button" class="skin-toggle theme-toggle ${cls}" data-skin-toggle aria-haspopup="listbox" aria-expanded="false" title="${title(id)}" aria-label="${title(id)}"${SKINS.length < 2 ? " hidden" : ""}>${inner(id)}</button>`;
}

// 弹出层挂在 body 上：公开页重绘 header、后台换标签页都不会把它一起清掉。
function menuEl() {
  let el = document.getElementById("skin-menu");
  if (el) return el;
  el = document.createElement("div");
  el.id = "skin-menu";
  el.className = "skin-menu";
  el.setAttribute("role", "listbox");
  el.setAttribute("aria-label", "外观风格");
  el.hidden = true;
  document.body.append(el);
  return el;
}

function renderMenu(el) {
  const cur = skinPreference();
  el.innerHTML = SKINS.map(
    (s) =>
      `<button type="button" class="skin-option${s.id === cur ? " on" : ""}" role="option" aria-selected="${s.id === cur}" data-skin-option="${esc(s.id)}">` +
      `<span class="skin-swatch" data-swatch="${esc(s.id)}" aria-hidden="true"><i></i><i></i><i></i></span>` +
      `<span class="skin-copy"><b>${esc(s.name)}</b><small>${esc(s.hint)}</small></span>` +
      `<svg class="skin-check" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M5.5 12.5l4.4 4.4L18.5 8"/></svg>` +
      `</button>`,
  ).join("");
  paintSwatches(el);
}

// 色样：内置皮肤的三格颜色写在 styles.css 的 [data-swatch] 规则里；主题包的颜色只有清单知道，
// 渲染完逐格上色。CSP 的 style-src 'self' 会丢掉 HTML 里的 style 属性，但经 CSSOM 写的不受限。
// 后台设置页的主题列表也用它。
export function paintSwatches(root) {
  for (const box of root.querySelectorAll("[data-swatch]")) {
    const t = byId(box.dataset.swatch);
    if (!t || t.builtin) continue;
    const colors = t.swatch || [];
    box.querySelectorAll("i").forEach((cell, i) => {
      if (/^#[0-9a-fA-F]{6}$/.test(colors[i] || "")) cell.style.background = colors[i];
    });
  }
}

function closeMenu() {
  const el = document.getElementById("skin-menu");
  if (el) el.hidden = true;
  for (const b of document.querySelectorAll("[data-skin-toggle]")) b.setAttribute("aria-expanded", "false");
}

// 贴着按钮展开，并夹在视口内：fixed 定位下只给 max-height 不够——按钮靠近底边时
// 整块会落到视口外面（最后一行点不到），所以上不去就翻到按钮上方。
function openMenu(btn) {
  const el = menuEl();
  renderMenu(el);
  el.hidden = false;
  btn.setAttribute("aria-expanded", "true");
  const r = btn.getBoundingClientRect();
  const w = el.offsetWidth;
  const h = el.offsetHeight;
  let top = r.bottom + 6;
  if (top + h > window.innerHeight - 8) top = Math.max(8, r.top - h - 6);
  const left = Math.min(Math.max(8, r.right - w), Math.max(8, window.innerWidth - w - 8));
  el.style.top = `${Math.round(top)}px`;
  el.style.left = `${Math.round(left)}px`;
}

// 委托到 document：公开页整块重绘 header、后台换标签页后按钮是新节点，不需要重新绑定。
document.addEventListener("click", (e) => {
  const btn = e.target.closest?.("[data-skin-toggle]");
  if (btn) {
    // 选择器自己的 stopPropagation 挡不住同一层文档委托，所以要显式判断开合。
    // 注意菜单第一次点开时还不存在（null），不能写成 !el?.hidden —— null 会被当成「开着」。
    const el = document.getElementById("skin-menu");
    if (el && !el.hidden) closeMenu();
    else openMenu(btn);
    return;
  }
  const opt = e.target.closest?.("[data-skin-option]");
  if (opt) {
    setSkin(opt.dataset.skinOption);
    return;
  }
  closeMenu();
});
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape") closeMenu();
});
