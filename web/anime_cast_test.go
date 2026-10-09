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
		if !strings.Contains(string(css), `--cast-art-url: url("/anime/`+id+`.webp")`) {
			t.Errorf("anime.css 里角色 %q 缺 --cast-art-url（立绘与「下半身」共用这一张图）", id)
		}
		if _, err := os.Stat("anime/" + id + ".webp"); err != nil {
			t.Errorf("人物图 web/anime/%s.webp 不存在: %v", id, err)
		}
	}
	// 立绘是透明底全身像，三样几何量每台不同、少一条那台机的构图就会崩：
	// --cast-hand 手停在哪一行（卷轴压在这条线上）、--cast-eye-top/bottom 眼睛带（眨眼层只在这里淡入）。
	for _, m := range ids {
		id := m[1]
		for _, v := range []string{"--cast-figure-h:", "--cast-hand:", "--cast-eye-top:", "--cast-eye-bottom:"} {
			if !strings.Contains(string(css), v) {
				t.Errorf("anime.css 里角色 %q 缺 %s（全身立绘的几何量）", id, v)
			}
		}
	}
	// 段首的「立绘层」规则必须按高度放全身像、并且裁在手上：background-size 用 auto + --cast-figure-h，
	// 高度用 figure-h × --cast-hand。改回「按宽度铺满」= 她会被拉成满宽，两侧的留白和卷轴位置全错。
	stage := regexp.MustCompile(`(?s)html\[data-skin="anime"\] \.anime-stage \{(.*?)\n\}`).FindStringSubmatch(string(css))
	if stage == nil {
		t.Fatal("anime.css 里找不到 .anime-stage 的规则")
	} else {
		body := stage[1]
		for _, want := range []string{
			"background-size: auto var(--cast-figure-h, 720px);",
			"height: calc(var(--cast-figure-h, 720px) * var(--cast-hand, 0.56));",
			"overflow: hidden;",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("anime.css 的 .anime-stage 少了 %q", want)
			}
		}
	}
	// 眨眼要用同一个人的闭眼帧：图不在、或样式里没引用，那台机就不会眨眼（其它三台照眨，更难发现）。
	for _, m := range ids {
		id := m[1]
		if _, err := os.Stat("anime/" + id + "-blink.webp"); err != nil {
			t.Errorf("缺闭眼帧 anime/%s-blink.webp（拿立绘 edit 一张，只改眼睛）", id)
		}
		if !strings.Contains(string(css), `--cast-blink-url: url("/anime/`+id+`-blink.webp")`) {
			t.Errorf("anime.css 里角色 %q 没有 --cast-blink-url（/anime/%s-blink.webp）", id, id)
		}
	}
	if got, want := strings.Count(string(css), "--cast-blink-dur:"), len(ids); got != want {
		t.Errorf("--cast-blink-dur 要 %d 条（四个人得错开眨眼，不然四张卡同时眨），现在是 %d 条", want, got)
	}
	for _, m := range regexp.MustCompile(`data-cast="([a-z0-9-]+)"`).FindAllStringSubmatch(string(css), -1) {
		if !strings.Contains(string(cast), `"`+m[1]+`":`) {
			t.Errorf("anime.css 挂着角色 %q，但 anime-cast.js 的名单里没有它", m[1])
		}
	}
}
