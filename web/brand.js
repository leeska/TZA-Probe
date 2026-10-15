import { esc } from "./api.js";

// 站点品牌：后台「面板设置」里改的站点名称与 logo。首屏值由 /theme-default.js 写在
// window.__tzaBrand 上（theme-boot.js 已据此换好标签页标题与 favicon），这里只负责拼品牌区。
// 两项都空就是内置的 TZA Probe 品牌：t·z·a 字标 + PROBE。
export const DEFAULT_NAME = "TZA Probe";

export function currentBrand() {
  const b = window.__tzaBrand || {};
  return { name: String(b.name || "").trim(), logo: String(b.logo || "") };
}

export function brandHTML() {
  const { name, logo } = currentBrand();
  const mark = logo ? `<img class="brand-logo" src="${esc(logo)}" alt="">` : `<span class="brand-mark">t<span>z</span>a</span>`;
  const text = name ? `<span class="brand-name">${esc(name)}<small>基础设施观测</small></span>` : `<span>PROBE<small>基础设施观测</small></span>`;
  return `<a class="brand" href="/" aria-label="${esc(name || DEFAULT_NAME)} 首页">${mark}${text}</a>`;
}

// 后台保存之后当场换上：标签页标题、favicon 与页面上已有的品牌区一起换，不用刷新。
// 与 theme-boot.js 的首屏逻辑保持一致。
export function applyBrand(next) {
  window.__tzaBrand = { ...currentBrand(), ...next };
  const { name, logo } = currentBrand();
  document.title = name || DEFAULT_NAME;
  const icon = document.querySelector('link[rel="icon"]');
  if (icon) {
    icon.href = logo || "/favicon.svg";
    if (logo) icon.removeAttribute("type");
    else icon.type = "image/svg+xml";
  }
  for (const el of document.querySelectorAll("a.brand")) el.outerHTML = brandHTML();
}
