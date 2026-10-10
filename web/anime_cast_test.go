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
		t.Error("app.js 要渲染 .anime-stage 人物层")
	}
	if !strings.Contains(string(app), `class="anime-hands"`) {
		t.Error("app.js 要渲染 .anime-hands 双手层（压在 HTML 看板上沿的那一层）")
	}
	// 图层顺序就是 DOM 顺序：人物层 → HTML 看板 → 双手层（双手层是卡片最后一个孩子，靠 z-index 压在看板上沿）。
	// 顺序写反了（比如手层落在看板前面）看起来就是「手被板压住」，和 CSS 里的 z-index 对不上，所以在这里钉死。
	bodyAt := strings.Index(string(app), `${stage}<div class="card-body">`)
	handsAt := strings.LastIndex(string(app), `${hands}`)
	if bodyAt < 0 || handsAt < 0 || handsAt < bodyAt {
		t.Error("app.js 里三层的顺序要是「人物层 → .card-body（看板）→ 双手层」")
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
			t.Errorf("anime.css 里角色 %q 缺 --cast-art-url（人物层：牌子与双手已从图里拆走）", id)
		}
		if !strings.Contains(string(css), `--cast-hands-url: url("/anime/`+id+`-hands.webp")`) {
			t.Errorf("anime.css 里角色 %q 缺 --cast-hands-url（双手层：压在 HTML 看板上沿的那双手）", id)
		}
		if _, err := os.Stat("anime/" + id + ".webp"); err != nil {
			t.Errorf("人物图 web/anime/%s.webp 不存在: %v", id, err)
		}
		if _, err := os.Stat("anime/" + id + "-hands.webp"); err != nil {
			t.Errorf("双手层图 web/anime/%s-hands.webp 不存在（拆层脚本 scripts/anime-split-layers.py）: %v", id, err)
		}
	}
	// 四段几何（2026-10-10 看板重做后的契约）：每台机五个比例量，少一样那张卡的构图就会崩。
	//   --cast-top-ar   上半身层（牌子上沿以上）的 aspect-ratio = W/(0.66·t)
	//   --cast-hands-ar 双手层的 aspect-ratio = W/(0.66·(t+60))，含手指压过板沿那一段
	//   --cast-legs-ar  下半身层（牌子下沿以下）的 aspect-ratio = W/(0.66·(H−b))
	//   --cast-full-ar  下半身层里那张全立绘盒子的 aspect-ratio = W/(0.66·H)
	//   --cast-seam     translateY 的位移 = b/H（牌子下外沿占立绘高的比例）
	for _, v := range []string{"--cast-top-ar:", "--cast-hands-ar:", "--cast-legs-ar:", "--cast-full-ar:", "--cast-seam:"} {
		if got, want := strings.Count(string(css), v), len(ids); got != want {
			t.Errorf("%s 要 %d 条（每台一条：牌子上/下外沿逐台实测），现在是 %d 条", v, want, got)
		}
	}
	// 上半身层只画「牌子上沿以上」那一段：按 66% 卡宽缩放（background-size: 66% auto），
	// 高度由 aspect-ratio 推出来，盒子底边正好落在立绘第 t 行（牌子上沿），看板压在那儿。
	// 改回「按定高缩放」= 看板高度就不再只由数据决定（就是用户否掉的那一版）；改回
	// 「按宽度铺满整身」= 下半身又被看板盖住。
	stage := regexp.MustCompile(`(?s)html\[data-skin="anime"\] \.anime-stage \{(.*?)\n\}`).FindStringSubmatch(string(css))
	if stage == nil {
		t.Fatal("anime.css 里找不到 .anime-stage 的规则")
	} else {
		body := stage[1]
		for _, want := range []string{
			"aspect-ratio: var(--cast-top-ar,",
			"background-size: 66% auto;",
			"overflow: hidden;",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("anime.css 的 .anime-stage 少了 %q", want)
			}
		}
	}
	// 下半身层 = 牌子下沿 → 画布底那一段：盒子里放一张全立绘，用 translateY(--cast-seam) 把它
	// 上移到第 b 行正好落在盒子顶边（也就是看板的下沿）。少了任何一半，裙/腿/袜/鞋就露不出来。
	legs := regexp.MustCompile(`(?s)html\[data-skin="anime"\] \.anime-legs::before \{(.*?)\n\}`).FindStringSubmatch(string(css))
	if legs == nil {
		t.Fatal("anime.css 里找不到 .anime-legs::before（下半身层：牌子下沿以下那一段立绘）")
	} else {
		for _, want := range []string{
			"aspect-ratio: var(--cast-full-ar,",
			"transform: translateY(calc(-100% * var(--cast-seam,",
			"background-size: 66% auto;",
		} {
			if !strings.Contains(legs[1], want) {
				t.Errorf("anime.css 的 .anime-legs::before 少了 %q", want)
			}
		}
	}
	if !strings.Contains(string(app), `class="anime-legs"`) {
		t.Error("app.js 要渲染 .anime-legs 下半身层（裙摆 / 大腿 / 白袜 / 皮鞋）")
	}
	// 下半身层只属于收起态：展开态看板从牌子上沿铺到卡片底部、把下半身整段盖住，不能再把这一层
	// 接在板下（那就是被用户否掉的「卡片被撑到 960~1015px」那一版）。收起态才放它出来。
	if !regexp.MustCompile(`\.node-card\[data-cast\]:not\(\[data-rolled="true"\]\) \.anime-legs\s*\{[^}]*display: none`).MatchString(string(css)) {
		t.Error("anime.css 要在展开态（:not([data-rolled=\"true\"])）藏掉 .anime-legs：展开不露下半身，看板铺到卡底")
	}
	// 两态卡高必须**恒等**（2026-10-10 用户：「收起卡片卡片尺寸会有变化，要保证各个卡片尺寸固定」）。
	// 高度靠 CSS 保：收起态板内数据只 visibility:hidden（仍占位 → 板盒高度 = 展开态板高），
	// 下半身层绝对定位贴底（不参与高度）。展开/收起都收掉卡片下内边距（多一个 20px 会让收起态凭空高 20px）。
	if !regexp.MustCompile(`\.node-card\[data-cast\]:not\(\[data-rolled="true"\]\)\s*\{[^}]*padding-bottom: 0`).MatchString(string(css)) &&
		!regexp.MustCompile(`\.node-card\[data-cast\]\s*\{[^}]*padding-bottom: 0`).MatchString(string(css)) {
		t.Error("anime.css 卡片要 padding-bottom: 0（两态都要）：看板下沿 / 下半身层下沿贴着卡片底边，收起态才不凭空高 20px")
	}
	if !regexp.MustCompile(`\.node-card\[data-cast\]\[data-rolled="true"\] \.card-body > \[id\]\s*\{[^}]*visibility: hidden`).MatchString(string(css)) {
		t.Error("anime.css 收起态板内数据要 visibility: hidden（不能 display: none）：数据仍占位，板盒高度 = 展开态板高，卡片高度才恒定")
	}
	if !regexp.MustCompile(`\.node-card\[data-cast\]\[data-rolled="true"\] \.card-body::before\s*\{[^}]*height: var\(--cast-plate-h`).MatchString(string(css)) {
		t.Error("anime.css 收起态的板面要由 .card-body::before 按 --cast-plate-h 画（板盒保持整高，板面只画到下半身层顶边）")
	}
	if !regexp.MustCompile(`\.node-card\[data-cast\]\[data-rolled="true"\] \.anime-legs\s*\{[^}]*position: absolute[^}]*bottom: 0`).MatchString(string(css)) {
		t.Error("anime.css 收起态下半身层要绝对定位贴卡片底部（bottom: 0）：它不参与卡片高度，卡高才与展开态恒等")
	}
	if !strings.Contains(string(app), "--cast-plate-h") || !strings.Contains(string(app), "function layoutCastPlates") {
		t.Error("app.js 要用 layoutCastPlates() 量出 --cast-plate-h（展开态板高 − 下半身层高，下限 52px）")
	}
	// 看板高度必须**由内容决定**、板内不许滚动：用户逐个否掉了「定高 + 板内竖滚」（数据要滚才看得全，
	// 原话「内容都没了，为啥做得这么小」）和「铺满整卡」（下半身看不到）两版。
	// 板高 auto + 板内 overflow: visible，数据才既一眼看全、又不滚动。
	if !regexp.MustCompile(`(?s)\.node-card\[data-cast\] \.card-body \{[^}]*height: auto;`).MatchString(string(css)) {
		t.Error("anime.css 的 .card-body（看板）要 height: auto：板高由数据决定，不许定高")
	}
	if regexp.MustCompile(`\.card-body[^{]*\{[^}]*overflow-y: auto`).MatchString(string(css)) {
		t.Error("anime.css 的看板不许再有 overflow-y: auto：板内滚动被用户否掉了（数据要一眼看全）")
	}
	idBody := regexp.MustCompile(`(?s)html\[data-skin="anime"\] \.node-card\[data-cast\] \.card-body > \[id\] \{(.*?)\n\}`).FindStringSubmatch(string(css))
	if idBody == nil {
		t.Fatal("anime.css 里找不到看板内数据区（.card-body > [id]）的规则")
	} else if !strings.Contains(idBody[1], "overflow: visible;") {
		t.Error("anime.css 的板内数据区要 overflow: visible：数据全部排在板内，不裁不滚")
	}
	// 卡壳必须是定位元素：双手层（absolute, top:0）要相对这张卡放。少了 position: relative，
	// 四台卡的双手层会一起锚到更高的定位祖先（body），几只手就飘到页面顶上的概览区
	//（用户发现「悬空的手指」）。
	if !regexp.MustCompile(`(?s)html\[data-skin="anime"\] \.node-card \{[^}]*position: relative`).MatchString(string(css)) {
		t.Error("anime.css 的 .node-card 要 position: relative：双手层是绝对定位，得相对这张卡放")
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
	// 双手层：每台一条，少一台那张卡的手就会落错位置。
	for _, v := range []string{"--cast-hands-url:"} {
		if got, want := strings.Count(string(css), v), len(ids); got != want {
			t.Errorf("%s 要 %d 条（每台一条：牌面逐台实测），现在是 %d 条", v, want, got)
		}
	}
	// 看板是 HTML 自己画的：板沿 + 板面上的数据都是 CSS 里的盒子，名字印在板面正中的固定坐标上。
	// 上一版「名字印在她手里那块牌子上」用一个全卡大小的卷轴层盖住整卡（手是 CSS 抠的伪元素、板沿
	// 会透出原图描边），整条去掉了——它要是回来，先在这里红。
	if !strings.Contains(string(css), ".node-card[data-cast] .card-body {") {
		t.Error("anime.css 缺少 .node-card[data-cast] .card-body（HTML 看板：板沿 + 板面）")
	}
	if !strings.Contains(string(css), ".anime-hands {") {
		t.Error("anime.css 缺少 .anime-hands（双手层：压在 HTML 看板上沿那一层）")
	}
	if strings.Contains(string(css), "anime-roll") {
		t.Error("anime.css 里不该再有卷轴（.anime-roll）：名字现在印在 HTML 看板正中的固定坐标上")
	}
	for _, m := range regexp.MustCompile(`data-cast="([a-z0-9-]+)"`).FindAllStringSubmatch(string(css), -1) {
		if !strings.Contains(string(cast), `"`+m[1]+`":`) {
			t.Errorf("anime.css 挂着角色 %q，但 anime-cast.js 的名单里没有它", m[1])
		}
	}
}
