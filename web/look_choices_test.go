package web

import (
	"os"
	"strings"
	"testing"

	"github.com/leeska/TZA-Probe/internal/model"
)

// 后台「前台默认外观」的可选值有两份清单：服务端的 model.PanelThemes / PanelSkins 管校验，
// 浏览器这边 theme.js 的 ORDER、skins.js 的 SKINS 管渲染。两边对不上时，后台能存下一个
// 前端不认识的 id（面板于是退回内置默认，看起来像「设置了没生效」）。这里把它们钉在一起。
func TestPanelLookChoicesMatchFrontend(t *testing.T) {
	themeJS, err := os.ReadFile("theme.js")
	if err != nil {
		t.Fatal(err)
	}
	skinsJS, err := os.ReadFile("skins.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range model.PanelThemes {
		if !strings.Contains(string(themeJS), `"`+id+`"`) {
			t.Errorf("theme.js 里没有明暗取值 %q", id)
		}
	}
	for _, id := range model.PanelSkins {
		if !strings.Contains(string(skinsJS), `id: "`+id+`"`) {
			t.Errorf("skins.js 里没有皮肤 %q", id)
		}
	}
	// 首屏顺序：默认外观 → theme-boot，反了会先按内置默认闪一下。
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	def := strings.Index(string(html), "/theme-default.js")
	boot := strings.Index(string(html), "/theme-boot.js")
	if def < 0 || boot < 0 || def > boot {
		t.Errorf("index.html 里的默认外观脚本位置不对: theme-default.js=%d theme-boot.js=%d", def, boot)
	}
	// 站点品牌：theme-boot.js 要在首屏前改掉 <title> 与 favicon，所以这两样得写在它前面。
	title := strings.Index(string(html), "<title>")
	icon := strings.Index(string(html), `rel="icon"`)
	if title < 0 || icon < 0 || title > boot || icon > boot {
		t.Errorf("index.html 的 <title>=%d / icon=%d 得在 theme-boot.js=%d 之前", title, icon, boot)
	}
}

// 计费汇总单位的清单也有两份：服务端 model.PriceUnits 管校验（存不进汇率表外或币种），
// 浏览器这边 format.js 的 FX_CNY 汇率表管折算、admin-nodes.js 的 CURRENCIES 管两个下拉的
// 选项。三处对不上时，要么后台存下一个前端折不动的单位（汇总磁贴永远显示 —），
// 要么设置页能选出一个服务端拒绝的币种。这里把它们钉在一起。
func TestPriceUnitChoicesMatchFrontend(t *testing.T) {
	formatJS, err := os.ReadFile("format.js")
	if err != nil {
		t.Fatal(err)
	}
	nodesJS, err := os.ReadFile("admin-nodes.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, unit := range model.PriceUnits {
		// FX_CNY 的键写成「CNY: 1」这样的一行；CURRENCIES 的选项写成 ["CNY", "…"]。
		if !strings.Contains(string(formatJS), " "+unit+": ") {
			t.Errorf("format.js 的 FX_CNY 里没有 %q 的参考汇率", unit)
		}
		if !strings.Contains(string(nodesJS), `["`+unit+`", "`) {
			t.Errorf("admin-nodes.js 的 CURRENCIES 里没有 %q 选项", unit)
		}
	}
	// 首屏脚本要带上汇总单位（app.js 的概览磁贴从 window.__tzaPriceUnit 读），漏了就都按 CNY 折。
	apiJS, err := os.ReadFile("../internal/httpapi/api.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(apiJS), "__tzaPriceUnit") {
		t.Errorf("/theme-default.js 没有下发 __tzaPriceUnit")
	}
}
