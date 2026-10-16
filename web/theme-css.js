// 已安装主题包的样式表：首屏前同步挂上，避免先按默认样式画一帧再跳。
// 放在 index.html 里内置样式表**之后**引用——主题包是叠在默认样式之上的一层，要排在后面才盖得住；
// 写在 theme-boot.js 里的话链接会插到内置样式表前面，优先级反了。
// 经典脚本、不内联，满足 CSP 的 script-src 'self'。清单与 data-skin 由 theme-boot.js 先定好。
(function () {
  var list = window.__tzaThemes || [];
  var skin = document.documentElement.dataset.skin;
  // 基础层：主题包声明 "base": "plain" 就停用液态玻璃的材质层（index.html 里带 data-base-css 的那条）。
  // 本脚本排在内置样式表之后，执行时它们已经加载完、首屏还没画，停用不会闪。规则与 skins.js 的 syncBaseCSS 相同。
  var plain = false;
  for (var k = 0; k < list.length; k++) if (list[k].id === skin && list[k].base === "plain") plain = true;
  var glass = document.querySelector('link[data-base-css="glass"]');
  if (glass) glass.disabled = plain;
  for (var i = 0; i < list.length; i++) {
    var t = list[i];
    if (t.id !== skin || t.builtin || !t.css) continue;
    for (var j = 0; j < t.css.length; j++) {
      // 地址由服务端拼好、只含 [A-Za-z0-9._/-?=]（见 internal/theme 的 safePath），可以直接写进属性。
      document.write('<link rel="stylesheet" data-theme-css="' + t.id + '" href="' + t.css[j] + '">');
    }
  }
})();
