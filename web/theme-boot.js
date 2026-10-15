// 在样式表加载前同步执行：按已存偏好、后台设的默认外观、或系统设置确定 <html data-theme>（明暗）与
// <html data-skin>（外观风格），避免首屏闪烁。
// 保持为经典脚本且不使用内联，以满足 Server 的 script-src 'self' CSP。
// 后台默认值由 /theme-default.js（上一行引用，同一份 CSP 下也是 self）写在 window.__tzaDefault 上。
// 切换逻辑见 theme.js / skins.js。
(function () {
  var d = window.__tzaDefault || {};
  // 认得的皮肤：/theme-default.js 发来的清单（内置 + 已安装的主题包）；没有清单时只认内置的液态玻璃。
  var list = window.__tzaThemes || [{ id: "glass" }];
  var known = function (id) {
    // 角色包（type: "characters"）不是外观，不能当皮肤用。
    for (var i = 0; i < list.length; i++) if (list[i].id === id && list[i].type !== "characters") return true;
    return false;
  };
  // 后台默认：没配过就是内置默认（跟随系统 + 液态玻璃），与 model.DefaultPanel* 一致。
  var themeDefault = d.theme === "light" || d.theme === "dark" ? d.theme : "auto";
  var skinDefault = known(d.skin) ? d.skin : "glass";
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
  // 认不出来的值（含 null、已删除的主题包）退回默认皮肤。
  root.dataset.skin = known(skin) ? skin : skinDefault;
  // 站点品牌：后台设了名称 / logo 就在首屏前换掉标签页标题与 favicon（index.html 把 <title> 与
  // <link rel="icon"> 放在本脚本之前）。后台保存后的即时替换见 brand.js 的 applyBrand。
  var brand = window.__tzaBrand || {};
  var name = typeof brand.name === "string" ? brand.name.trim() : "";
  if (name) document.title = name;
  if (typeof brand.logo === "string" && brand.logo) {
    var icon = document.querySelector('link[rel="icon"]');
    if (icon) {
      icon.removeAttribute("type");
      icon.href = brand.logo;
    }
  }
})();
