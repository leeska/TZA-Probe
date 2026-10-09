// 外观风格（皮肤）：面板有三套视觉语言 —— 默认的「液态玻璃」、「复古印刷」与「现代极简」。
// 明暗（data-theme）仍由 theme.js 决定，风格（data-skin）由这里决定，两者正交：
// 同一套皮肤在浅色与深色下各有一份令牌。偏好存 localStorage 的 tza-skin，
// theme-boot.js 在样式表生效前已把 data-skin 写到 <html> 上，避免首屏闪一下默认皮肤。
const KEY = "tza-skin";
// 二次元皮肤的卷轴透明度：皮肤菜单里的滑块改，存 localStorage、以 --anime-roll-alpha 挂到 <html> 上
// （theme-boot.js 首屏会先读一遍）。范围 0.25–1：再低节点名就看不清了。
const ALPHA_KEY = "tza-anime-roll-alpha";
export const ALPHA_MIN = 0.25;

export function rollAlpha() {
  let v = NaN;
  try {
    v = parseFloat(localStorage.getItem(ALPHA_KEY));
  } catch {}
  return isFinite(v) && v >= ALPHA_MIN && v <= 1 ? v : 1;
}

export function setRollAlpha(v) {
  const a = Math.min(1, Math.max(ALPHA_MIN, Math.round(v * 100) / 100));
  try {
    a >= 1 ? localStorage.removeItem(ALPHA_KEY) : localStorage.setItem(ALPHA_KEY, String(a));
  } catch {}
  document.documentElement.style.setProperty("--anime-roll-alpha", String(a));
  return a;
}

// swatch 的三格颜色写在 styles.css 里（CSP 的 style-src 'self' 会忽略行内 style），
// 这里只给 data-swatch 的值，配色见 styles.css 的 .skin-swatch 一段。
export const SKINS = [
  { id: "glass", name: "液态玻璃", hint: "毛玻璃 · 圆角 · 柔和阴影" },
  { id: "press", name: "复古印刷", hint: "纸张方格 · 硬边框 · 衬线标题" },
  { id: "shadcn", name: "现代极简", hint: "中性灰 · 发丝描边 · 小圆角" },
  { id: "anime", name: "二次元", hint: "线路机角色 · 白面樱花粉" },
];

const IDS = SKINS.map((s) => s.id);
const NAME = Object.fromEntries(SKINS.map((s) => [s.id, s.name]));
// 后台设的默认皮肤（/theme-default.js 写在 window.__tzaDefault 上）；认不出来就是内置默认。
const DEFAULT = IDS.includes(window.__tzaDefault?.skin) ? window.__tzaDefault.skin : "glass";

export function skinPreference() {
  let v = null;
  try {
    v = localStorage.getItem(KEY);
  } catch {}
  return IDS.includes(v) ? v : DEFAULT;
}

// 浏览器地址栏 / 状态栏的底色：玻璃是冷白，印刷是纸色，极简是纯白 / 纯黑。
const CHROME = {
  press: { light: "#f8f1df", dark: "#15120f" },
  shadcn: { light: "#ffffff", dark: "#0a0a0a" },
  anime: { light: "#f4f5fd", dark: "#14161f" },
};
export function skinChrome() {
  return CHROME[skinPreference()] || { light: "#edf2fb", dark: "#0f141c" };
}

// 与 theme.js 各管一半：这里只管 data-skin、按钮文字与 meta 底色，明暗由 applyTheme 负责。
export function applySkin(skin = skinPreference()) {
  document.documentElement.dataset.skin = skin;
  const title = `外观风格：${NAME[skin]}，点击选择`;
  for (const b of document.querySelectorAll("[data-skin-toggle]")) {
    b.innerHTML = inner(skin);
    b.title = title;
    b.setAttribute("aria-label", title);
  }
  paintChrome();
  closeMenu();
}

function paintChrome() {
  const c = skinChrome();
  const content = document.documentElement.dataset.theme === "dark" ? c.dark : c.light;
  for (const m of document.querySelectorAll('meta[name="theme-color"]')) m.content = content;
}

export function setSkin(id) {
  if (!IDS.includes(id)) return;
  try {
    if (id === DEFAULT) localStorage.removeItem(KEY);
    else localStorage.setItem(KEY, id);
  } catch {}
  applySkin(id);
}

const ICON = `<svg viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M10 3.2c-3.9 0-7 2.5-7 5.6 0 1.9 1.6 3 3.4 3h1.1c.9 0 1.5.9 1 1.7-.6 1 .1 2.3 1.4 2.3 3.9 0 7-2.6 7-5.8S13.9 3.2 10 3.2Z"/><circle cx="7.4" cy="8.2" r="1"/><circle cx="11" cy="7.2" r="1"/></svg>`;

const inner = (id) => `${ICON}<span>${NAME[id]}</span>`;
const title = (id) => `外观风格：${NAME[id]}，点击选择`;

export function skinButton(cls = "") {
  const id = skinPreference();
  return `<button type="button" class="skin-toggle theme-toggle ${cls}" data-skin-toggle aria-haspopup="listbox" aria-expanded="false" title="${title(id)}" aria-label="${title(id)}">${inner(id)}</button>`;
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
      `<button type="button" class="skin-option${s.id === cur ? " on" : ""}" role="option" aria-selected="${s.id === cur}" data-skin-option="${s.id}">` +
      `<span class="skin-swatch" data-swatch="${s.id}" aria-hidden="true"><i></i><i></i><i></i></span>` +
      `<span class="skin-copy"><b>${s.name}</b><small>${s.hint}</small></span>` +
      `<svg class="skin-check" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M5.5 12.5l4.4 4.4L18.5 8"/></svg>` +
      `</button>`,
  ).join("");
  // 二次元皮肤在菜单底部多一行：卷轴透明度。只有当前皮肤是它的时候才给——
  // 别的皮肤下卷轴根本不存在，摆一个滑块只会让人以为它对玻璃皮肤也有用。
  if (cur === "anime") {
    const pct = Math.round(rollAlpha() * 100);
    el.innerHTML +=
      `<div class="skin-tune" data-anime-tune>` +
      `<label for="anime-roll-alpha">卷轴透明度</label>` +
      `<input id="anime-roll-alpha" type="range" min="25" max="100" step="5" value="${pct}" data-roll-alpha aria-label="卷轴透明度">` +
      `<output for="anime-roll-alpha">${pct}%</output>` +
      `</div>`;
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
  // 透明度滑块的点击不算「点空白」，否则一拖菜单就没了。
  if (e.target.closest?.("[data-anime-tune]")) return;
  closeMenu();
});
// input 的委托挂在 document 上：菜单是 body 上的常驻节点，重新渲染后不需要重绑。
document.addEventListener("input", (e) => {
  const r = e.target.closest?.("[data-roll-alpha]");
  if (!r) return;
  const a = setRollAlpha(Number(r.value) / 100);
  const out = r.parentElement?.querySelector("output");
  if (out) out.textContent = `${Math.round(a * 100)}%`;
});
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape") closeMenu();
});
