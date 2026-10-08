// 亮暗主题：偏好存于 localStorage，"auto" 跟随系统。后台设的默认明暗（"auto" / "light" /
// "dark"，由 /theme-default.js 写在 window.__tzaDefault 上）用于访客还没自己选过的时候；
// theme-boot.js 在样式生效前已按同样规则设置了 <html data-theme>，这里负责切换按钮和系统偏好变化。
// 外观风格（data-skin）在 skins.js，两者正交：applyTheme 只管明暗，底色交给 skinChrome()。
import { skinChrome } from "./skins.js";

const KEY = "tza-theme";
const ORDER = ["auto", "light", "dark"];
// 后台默认值：认不出来（含未配置）就是内置默认「跟随系统」。
const DEFAULT = ORDER.includes(window.__tzaDefault?.theme) ? window.__tzaDefault.theme : "auto";
const LABEL = { auto: "跟随系统", light: "浅色", dark: "深色" };
// 后台「前台默认外观」的下拉项就用这一份，别在 admin.js 里再抄一遍标签。
export const THEMES = ORDER.map((id) => ({ id, name: LABEL[id] }));
const ICON = {
  auto: '<svg viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" aria-hidden="true"><circle cx="10" cy="10" r="6.5"/><path d="M10 3.5v13" /><path d="M10 3.5a6.5 6.5 0 0 1 0 13" fill="currentColor" stroke="none"/></svg>',
  light: '<svg viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" aria-hidden="true"><circle cx="10" cy="10" r="3.5"/><path d="M10 2.5v2M10 15.5v2M2.5 10h2M15.5 10h2M4.7 4.7l1.4 1.4M13.9 13.9l1.4 1.4M4.7 15.3l1.4-1.4M13.9 6.1l1.4-1.4"/></svg>',
  dark: '<svg viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M16.5 12.2A7 7 0 0 1 7.8 3.5a7 7 0 1 0 8.7 8.7Z"/></svg>',
};
const media = window.matchMedia?.("(prefers-color-scheme: dark)");

export function preference() {
  let v = null;
  try {
    v = localStorage.getItem(KEY);
  } catch {}
  return ORDER.includes(v) ? v : DEFAULT;
}

function resolved(pref) {
  return pref === "auto" ? (media?.matches ? "dark" : "light") : pref;
}

export function applyTheme(pref = preference()) {
  const theme = resolved(pref);
  const root = document.documentElement;
  root.dataset.theme = theme;
  const chrome = skinChrome();
  for (const m of document.querySelectorAll('meta[name="theme-color"]')) m.content = theme === "dark" ? chrome.dark : chrome.light;
  for (const b of document.querySelectorAll("[data-theme-toggle]")) {
    b.innerHTML = inner(pref);
    b.title = title(pref);
    b.setAttribute("aria-label", b.title);
  }
}

const inner = (pref) => ICON[pref] + `<span>${LABEL[pref]}</span>`;
const title = (pref) => `主题：${LABEL[pref]}，点击切换`;

export function setPreference(pref) {
  try {
    // 与后台默认一致就不留键：键在 = 访客自己选过，键不在 = 跟着后台默认走。
    if (pref === DEFAULT) localStorage.removeItem(KEY);
    else localStorage.setItem(KEY, pref);
  } catch {}
  applyTheme(pref);
}

export function cycleTheme() {
  const pref = preference();
  setPreference(ORDER[(ORDER.indexOf(pref) + 1) % ORDER.length]);
}

export function themeButton(cls = "") {
  const pref = preference();
  return `<button type="button" class="theme-toggle ${cls}" data-theme-toggle title="${title(pref)}" aria-label="${title(pref)}">${inner(pref)}</button>`;
}

// 委托到 document，页面重绘后新插入的按钮无需重新绑定。
document.addEventListener("click", (e) => {
  if (e.target.closest?.("[data-theme-toggle]")) cycleTheme();
});
media?.addEventListener?.("change", () => {
  if (preference() === "auto") applyTheme("auto");
});
