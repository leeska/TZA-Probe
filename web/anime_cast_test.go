package web

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// 角色名单（web/anime-cast.js）、样式表（web/anime.css）与人物图（web/anime/<id>.webp）三处必须对齐：
// 名单里多一个 id 而样式表没规则 = 卡片顶部空一块（元素渲染了、图却挂不上）；样式表里挂了一个
// 名单里没有的 id = 拼错的死规则；少了图片文件则是运行时 404。三样都是「静默不显示」，
// 只有这条测试能提前拦住。
func TestAnimeCastMatchesStylesheetAndAssets(t *testing.T) {
	cast, err := os.ReadFile("anime-cast.js")
	if err != nil {
		t.Fatal(err)
	}
	css, err := os.ReadFile("anime.css")
	if err != nil {
		t.Fatal(err)
	}
	app, err := os.ReadFile("app.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(app), `from "./anime-cast.js"`) {
		t.Error("app.js 必须从 anime-cast.js 取角色名单")
	}
	if !strings.Contains(string(app), `class="anime-stage"`) {
		t.Error("app.js 要渲染 .anime-stage 立绘层")
	}
	if !strings.Contains(string(app), `data-cast="${cast}"`) {
		t.Error("app.js 要在卡壳上挂 data-cast（牌面配色按它上色）")
	}

	ids := regexp.MustCompile(`(?m)^\s{2}"([a-z0-9-]+)":\s*\{`).FindAllStringSubmatch(string(cast), -1)
	if len(ids) == 0 {
		t.Fatal("anime-cast.js 里没解析出任何角色")
	}
	for _, m := range ids {
		id := m[1]
		if !strings.Contains(string(css), `[data-cast="`+id+`"]`) {
			t.Errorf("anime.css 缺少角色 %q 的 [data-cast] 规则", id)
		}
		if !strings.Contains(string(css), `url("/anime/`+id+`.webp")`) {
			t.Errorf("anime.css 里角色 %q 的人物图路径不是 /anime/%s.webp", id, id)
		}
		// 牌面配色（卡壳上的 .node-card[data-cast]）与它的夜版，是「加一台机器」的第四、第五处：
		// 漏了不会报错，只会让这台机的卡片停在默认白面——所以和立绘、名单一起守住。
		if !strings.Contains(string(css), `.node-card[data-cast="`+id+`"]`) {
			t.Errorf("anime.css 缺少角色 %q 的牌面配色（.node-card[data-cast]）", id)
		}
		if !strings.Contains(string(css), `[data-theme="dark"] .node-card[data-cast="`+id+`"]`) {
			t.Errorf("anime.css 缺少角色 %q 的夜版牌面配色", id)
		}
		if _, err := os.Stat("anime/" + id + ".webp"); err != nil {
			t.Errorf("人物图 web/anime/%s.webp 不存在: %v", id, err)
		}
	}
	for _, m := range regexp.MustCompile(`data-cast="([a-z0-9-]+)"`).FindAllStringSubmatch(string(css), -1) {
		if !strings.Contains(string(cast), `"`+m[1]+`":`) {
			t.Errorf("anime.css 挂着角色 %q，但 anime-cast.js 的名单里没有它", m[1])
		}
	}
}
