// 在样式表加载前同步执行：按已存偏好、后台设的默认外观、或系统设置确定 <html data-theme>（明暗）与
// <html data-skin>（外观风格），避免首屏闪烁。
// 保持为经典脚本且不使用内联，以满足 Server 的 script-src 'self' CSP。
// 后台默认值由 /theme-default.js（上一行引用，同一份 CSP 下也是 self）写在 window.__tzaDefault 上。
// 切换逻辑见 theme.js / skins.js。
(function () {
  var d = window.__tzaDefault || {};
  // 后台默认：没配过就是内置默认（跟随系统 + 液态玻璃），与 model.DefaultPanel* 一致。
  var themeDefault = d.theme === "light" || d.theme === "dark" ? d.theme : "auto";
  var skinDefault = d.skin === "press" || d.skin === "shadcn" ? d.skin : "glass";
  var pref = null;
  try {
    pref = localStorage.getItem("tza-theme");
  } catch (e) {}
  var skin = null;
  try {
    skin = localStorage.getItem("tza-skin");
  } catch (e) {}
  // 访客自己选过就听他的（localStorage 有值），没选过才用后台默认。
  if (pref !== "light" && pref !== "dark" && pref !== "auto") pref = themeDefault;
  var dark = pref === "dark" || (pref === "auto" && matchMedia("(prefers-color-scheme: dark)").matches);
  var root = document.documentElement;
  root.dataset.theme = dark ? "dark" : "light";
  // 与 skins.js 的 SKINS 同名清单：认不出来的值（含 null）退回默认皮肤。
  root.dataset.skin = skin === "press" || skin === "shadcn" ? skin : skinDefault;
})();
