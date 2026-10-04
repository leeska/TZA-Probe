// 在样式表加载前同步执行：按已存偏好或系统设置确定 <html data-theme>（明暗）与
// <html data-skin>（外观风格），避免首屏闪烁。
// 保持为经典脚本且不使用内联，以满足 Server 的 script-src 'self' CSP。切换逻辑见 theme.js / skins.js。
(function () {
  var pref = null;
  try {
    pref = localStorage.getItem("tza-theme");
  } catch (e) {}
  var skin = null;
  try {
    skin = localStorage.getItem("tza-skin");
  } catch (e) {}
  var dark = pref === "dark" || (pref !== "light" && matchMedia("(prefers-color-scheme: dark)").matches);
  var root = document.documentElement;
  root.dataset.theme = dark ? "dark" : "light";
  root.dataset.skin = skin === "press" ? "press" : "glass";
})();
