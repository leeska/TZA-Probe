package web

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// animeDir 是二次元主题包的源码（从市场安装，样式表与人物图都在包里）。
const animeDir = "../themes/anime"

// 角色名单（web/anime-cast.js）、样式表（themes/anime/anime.css）与人物图（themes/anime/anime/<id>.webp）三处必须对齐：
// 名单里多一个 id 而样式表没规则 = 卡片顶部空一块（元素渲染了、图却挂不上）；样式表里挂了一个
// 名单里没有的 id = 拼错的死规则；少了图片文件则是运行时 404。三样都是「静默不显示」，
// 只有这条测试能提前拦住。
//
// 图层结构（2026-10-10 v2，用户原话「上下体不要分开，现在状态很乱」）：
//
//	① 完整人物图 .anime-cast —— **整张立绘，一张连续的图**（不是上/下半身两段拼接）
//	② HTML 看板 .card-body   —— 直接盖在人物图上；展开态从牌子上沿铺到卡片底部
//	③ 双手层 .anime-hands    —— 同一张图的手部切片，压在看板上沿
//
// 任何一次「为了补空白 / 遮下半身而把人物切两段」的改动都要先在这里红。
func TestAnimeCastMatchesStylesheetAndAssets(t *testing.T) {
	cast, err := os.ReadFile("anime-cast.js")
	if err != nil {
		t.Fatal(err)
	}
	css, err := os.ReadFile(animeDir + "/anime.css")
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
	if !strings.Contains(string(app), `class="anime-cast"`) {
		t.Error("app.js 要渲染 .anime-cast 人物层（整张立绘）")
	}
	if !strings.Contains(string(app), `class="anime-hands"`) {
		t.Error("app.js 要渲染 .anime-hands 双手层（压在 HTML 看板上沿的那一层）")
	}
	// 人物层必须是**一张完整连续的图**：旧的「上/下半身两段分别裁切定位」那套元素与样式一个都不许留。
	if strings.Contains(string(app), "anime-stage") || strings.Contains(string(app), "anime-legs") {
		t.Error("app.js 里不该再有 .anime-stage / .anime-legs：人物永远是一张完整连续的图（不再上下两段拼接）")
	}
	if strings.Contains(string(css), ".anime-stage") || strings.Contains(string(css), ".anime-legs") {
		t.Error("anime.css 里不该再有 .anime-stage / .anime-legs：人物永远是一张完整连续的图（不再上下两段拼接）")
	}
	// 补板面空白的徽记 / 斜纹装饰整条去掉（用户 2026-10-10 原话「现在状态很乱」，嫌那些装饰乱）。
	// 只针对「板条上的装饰」：卡牌底纹 --cast-motif 是牌面本来的设计，不在此列。
	if strings.Contains(string(css), "--cast-stripe") || strings.Contains(string(css), "cast-emblem") ||
		strings.Contains(string(css), "data-plate-tall") ||
		regexp.MustCompile(`\.card-body::after`).MatchString(string(css)) {
		t.Error("anime.css 里不该再有板条徽记 / 斜纹装饰（--cast-stripe / cast-emblem / data-plate-tall / .card-body::after）")
	}
	if strings.Contains(string(app), "data-plate-tall") || strings.Contains(string(app), "plateTall") {
		t.Error("app.js 里不该再挂 data-plate-tall：徽记 / 斜纹装饰整条去掉")
	}
	// 图层顺序就是 DOM 顺序：人物层 → HTML 看板 → 双手层（双手层是卡片最后一个孩子，靠 z-index 压在看板上沿）。
	// 顺序写反了（比如手层落在看板前面）看起来就是「手被板压住」，和 CSS 里的 z-index 对不上，所以在这里钉死。
	bodyAt := strings.Index(string(app), `${figure}<div class="card-body">`)
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
		if !strings.Contains(string(css), `url("anime/`+id+`.webp")`) {
			t.Errorf("anime.css 里角色 %q 的人物图路径不是 anime/%s.webp（相对主题包）", id, id)
		}
		// 牌面配色（卡壳上的 .node-card[data-cast]）与它的夜版，是「加一台机器」的第四、第五处：
		// 漏了不会报错，只会让这台机的卡片停在默认白面——所以和立绘、名单一起守住。
		if !strings.Contains(string(css), `.node-card[data-cast="`+id+`"]`) {
			t.Errorf("anime.css 缺少角色 %q 的牌面配色（.node-card[data-cast]）", id)
		}
		if !strings.Contains(string(css), `[data-theme="dark"] .node-card[data-cast="`+id+`"]`) {
			t.Errorf("anime.css 缺少角色 %q 的夜版牌面配色", id)
		}
		if !strings.Contains(string(css), `--cast-art-url: url("anime/`+id+`.webp")`) {
			t.Errorf("anime.css 里角色 %q 缺 --cast-art-url（人物层：整张连续立绘）", id)
		}
		if !strings.Contains(string(css), `--cast-hands-url: url("anime/`+id+`-hands.webp")`) {
			t.Errorf("anime.css 里角色 %q 缺 --cast-hands-url（双手层：压在 HTML 看板上沿的那双手）", id)
		}
		if _, err := os.Stat(animeDir + "/anime/" + id + ".webp"); err != nil {
			t.Errorf("人物图 themes/anime/anime/%s.webp 不存在: %v", id, err)
		}
		if _, err := os.Stat(animeDir + "/anime/" + id + "-hands.webp"); err != nil {
			t.Errorf("双手层图 themes/anime/anime/%s-hands.webp 不存在（拆层脚本 scripts/anime-split-layers.py）: %v", id, err)
		}
	}
	// 人物图几何（2026-10-10 v2 后的契约）：每台机五个量，少一样那张卡的构图就会崩。
	//   --cast-top-frac  牌子上外沿占立绘高的比例（逐台实测，从原图量）
	//   --cast-bot-frac  牌子下外沿占立绘高的比例（收起态板条正好盖住这块牌子）
	//   --cast-figure-h  人物图高（实测板高反推：脚下留白 / 牌子上沿比例）——收起 / 展开同一个值
	//   --cast-board-top 板顶距卡顶的像素（= 图顶偏移 + 图高×牌子上沿比例 − 板唇）
	//   --cast-plate-h   收起态板条高（= 图高×(牌子下沿−上沿) + 2×板唇，正好盖住原图那块牌子）
	// 真浏览器里 app.js 用实测板高覆盖这三个 px 量；这三条是 jsdom / 关 JS 时的兜底常量。
	for _, v := range []string{"--cast-top-frac:", "--cast-bot-frac:", "--cast-figure-h:", "--cast-board-top:", "--cast-plate-h:"} {
		if got, want := strings.Count(string(css), v), len(ids); got != want {
			t.Errorf("%s 要 %d 条（每台一条：牌子上/下沿逐台实测），现在是 %d 条", v, want, got)
		}
	}
	// 人物层只画**整张立绘**：按图高缩放（background-size: auto 100%），顶对齐居中，绝对定位、不吃指针。
	// 改回「66% 卡宽按比例裁一段」= 又变成上下两段拼接（用户否掉的那版）；
	// 去掉 background-position: top center 或换背景尺寸 = 人物相对看板错位。
	figure := regexp.MustCompile(`(?s)html\[data-skin="anime"\] \.anime-cast \{(.*?)\n\}`).FindStringSubmatch(string(css))
	if figure == nil {
		t.Fatal("anime.css 里找不到 .anime-cast 的规则")
	}
	for _, want := range []string{
		"position: absolute",
		"height: var(--cast-figure-h,",
		"background-image: var(--cast-art-url)",
		"background-size: auto 100%",
		"background-position: top center",
		"pointer-events: none",
		"z-index: 1",
	} {
		if !strings.Contains(figure[1], want) {
			t.Errorf("anime.css 的 .anime-cast 少了 %q（整张连续人物图：按图高缩放、顶对齐居中、看板之下）", want)
		}
	}
	// 双手层与人物层共用同一套几何（同一个 --cast-figure-h / --cast-fig-top / auto 100% / top center），
	// 否则手会从手腕上错开；z-index 3 才压在看板上沿、修补被板沿吃掉的那几像素指尖。
	handsRule := regexp.MustCompile(`(?sm)^html\[data-skin="anime"\] \.anime-hands \{(.*?)\n\}`).FindStringSubmatch(string(css))
	if handsRule == nil {
		t.Fatal("anime.css 里找不到 .anime-hands 的主规则")
	}
	for _, want := range []string{
		"position: absolute",
		"top: var(--cast-fig-top,",
		"height: var(--cast-figure-h,",
		"z-index: 3",
		"background-image: var(--cast-hands-url)",
		"background-size: auto 100%",
		"background-position: top center",
		"pointer-events: none",
	} {
		if !strings.Contains(handsRule[1], want) {
			t.Errorf("anime.css 的 .anime-hands 少了 %q（要与人物层同一套几何，手才不会从手腕上错开）", want)
		}
	}
	// 两态卡高必须**恒等**：收起与展开不能改变卡片尺寸。
	// 高度靠 CSS 保：收起态板内数据只 visibility:hidden（仍占位 → 板盒高度 = 展开态板高）；
	// 展开 / 收起都收掉卡片下内边距（多一个 20px 会让收起态凭空高 20px）。
	if !regexp.MustCompile(`\.node-card\[data-cast\]:not\(\[data-rolled="true"\]\)\s*\{[^}]*padding-bottom: 0`).MatchString(string(css)) &&
		!regexp.MustCompile(`\.node-card\[data-cast\]\s*\{[^}]*padding-bottom: 0`).MatchString(string(css)) {
		t.Error("anime.css 卡片要 padding-bottom: 0（两态都要）：看板下沿贴着卡片底边，收起态才不凭空高 20px")
	}
	if !regexp.MustCompile(`\.node-card\[data-cast\]\[data-rolled="true"\] \.card-body > \[id\] > :not\(\.node-sub\)\s*\{[^}]*visibility: hidden`).MatchString(string(css)) {
		t.Error("anime.css 收起态板内数据要 visibility: hidden（不能 display: none）：数据仍占位，板盒高度 = 展开态板高，卡片高度才恒定")
	}
	if !regexp.MustCompile(`\.node-card\[data-cast\]\[data-rolled="true"\] \.card-body::before\s*\{[^}]*height: var\(--cast-plate-h`).MatchString(string(css)) {
		t.Error("anime.css 收起态的板面要由 .card-body::before 按 --cast-plate-h 画（板盒保持整高，板条正好盖住原图那块牌子）")
	}
	if !regexp.MustCompile(`\.node-card\[data-cast\]\[data-rolled="true"\] \.card-body\s*\{[^}]*overflow: visible`).MatchString(string(css)) {
		t.Error("anime.css 收起态板盒要 overflow: visible：板条以下是人物层，同一张图里的裙摆 / 腿 / 鞋要从透出来的板盒里露出来")
	}
	if !strings.Contains(string(app), "--cast-plate-h") || !strings.Contains(string(app), "function layoutCastCards") {
		t.Error("app.js 要用 layoutCastCards() 量出 --cast-plate-h / --cast-board-top / --cast-figure-h")
	}
	// 看板高度必须**由数据决定**、板内不许滚动：用户逐个否掉了「定高 + 板内竖滚」（数据要滚才看得全，
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
	// 卡壳必须是定位元素：人物层 / 双手层（absolute, top 按图算）要相对这张卡放。少了 position: relative，
	// 四台卡的立绘会一起锚到更高的定位祖先（body），人整张飘到页面顶上的概览区。
	if !regexp.MustCompile(`(?s)html\[data-skin="anime"\] \.node-card \{[^}]*position: relative`).MatchString(string(css)) {
		t.Error("anime.css 的 .node-card 要 position: relative：人物层 / 双手层是绝对定位，得相对这张卡放")
	}
	// 眨眼要用同一个人的闭眼帧：图不在、或样式里没引用，那台机就不会眨眼（其它三台照眨，更难发现）。
	for _, m := range ids {
		id := m[1]
		if _, err := os.Stat(animeDir + "/anime/" + id + "-blink.webp"); err != nil {
			t.Errorf("缺闭眼帧 anime/%s-blink.webp（拿立绘 edit 一张，只改眼睛）", id)
		}
		if !strings.Contains(string(css), `--cast-blink-url: url("anime/`+id+`-blink.webp")`) {
			t.Errorf("anime.css 里角色 %q 没有 --cast-blink-url（anime/%s-blink.webp）", id, id)
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
