package theme

import (
	"archive/zip"
	"bytes"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const manifest = `{"id":"matcha","name":"抹茶","hint":"示例","version":"1.0.0",
 "chrome":{"light":"#eef0e2","dark":"#141811"},"swatch":["#eef0e2","#fbfaf3","#5f8f3a"],"styles":["theme.css"]}`

type file struct {
	name, body string
	mode       fs.FileMode
}

func pack(t *testing.T, files ...file) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		h := &zip.FileHeader{Name: f.name, Method: zip.Deflate}
		if f.mode != 0 {
			h.SetMode(f.mode)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(f.body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestInstallListServeRemove(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	// Packed as a folder (how Finder / zip -r pack a directory): the folder is stripped.
	m, err := s.Install(pack(t,
		file{name: "matcha/theme.json", body: manifest},
		file{name: "matcha/theme.css", body: `:root[data-skin="matcha"]{--accent:#5f8f3a}`},
		file{name: "matcha/img/paper.webp", body: "RIFF"},
		file{name: "__MACOSX/matcha/._theme.css", body: "x"},
	))
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "matcha" || m.Builtin || m.Rev == "" {
		t.Fatalf("manifest: %+v", m)
	}
	list := s.List()
	if len(list) != len(Builtins)+1 || !list[0].Builtin || list[len(list)-1].ID != "matcha" {
		t.Fatalf("list: built-ins first, then installed: %+v", list)
	}
	if !s.Known("matcha") || !s.Known("glass") || s.Known("nope") {
		t.Fatal("Known must cover built-ins and installed packages only")
	}

	mux := http.NewServeMux()
	mux.Handle("GET /themes/{id}/{file...}", s)
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec
	}
	css := get("/themes/matcha/theme.css")
	if css.Code != 200 || css.Header().Get("Content-Type") != "text/css; charset=utf-8" || css.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("stylesheet: %d %v", css.Code, css.Header())
	}
	if rec := get("/themes/matcha/img/paper.webp"); rec.Code != 200 || rec.Header().Get("Content-Type") != "image/webp" {
		t.Fatalf("nested image: %d %v", rec.Code, rec.Header())
	}
	for _, path := range []string{"/themes/matcha/missing.css", "/themes/glass/theme.css", "/themes/matcha/theme.json.bak", "/themes/nope/theme.css"} {
		if rec := get(path); rec.Code != 404 {
			t.Errorf("%s must be 404, got %d", path, rec.Code)
		}
	}

	// Re-uploading the same id replaces the package and changes its revision.
	again, err := s.Install(pack(t, file{name: "theme.json", body: strings.Replace(manifest, "1.0.0", "1.1.0", 1)}, file{name: "theme.css", body: "/* v2 */"}))
	if err != nil || again.Version != "1.1.0" {
		t.Fatalf("upgrade: %+v %v", again, err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "matcha", "img")); !os.IsNotExist(err) {
		t.Fatal("an upgrade replaces the whole package; files of the old version must not linger")
	}

	if err := s.Remove("glass"); err == nil {
		t.Fatal("built-ins cannot be removed")
	}
	if err := s.Remove("matcha"); err != nil {
		t.Fatal(err)
	}
	if s.Known("matcha") || len(s.Installed()) != 0 {
		t.Fatal("removed package still listed")
	}
	if err := s.Remove("matcha"); !os.IsNotExist(err) {
		t.Fatalf("removing twice: %v", err)
	}
	if entries, _ := os.ReadDir(s.Dir); len(entries) != 0 {
		t.Fatalf("temporary upload / trash folders left behind: %v", entries)
	}
}

func TestInstallRejects(t *testing.T) {
	css := file{name: "theme.css", body: "a{}"}
	ok := file{name: "theme.json", body: manifest}
	cases := map[string][]byte{
		"script":        pack(t, ok, css, file{name: "evil.js", body: "alert(1)"}),
		"svg":           pack(t, ok, css, file{name: "logo.svg", body: "<svg onload=alert(1)>"}),
		"html":          pack(t, ok, css, file{name: "index.html", body: "<script>"}),
		"traversal":     pack(t, ok, css, file{name: "../escape.css", body: "a{}"}),
		"absolute":      pack(t, ok, css, file{name: "/etc/x.css", body: "a{}"}),
		"symlink":       pack(t, ok, css, file{name: "link.css", body: "/etc/passwd", mode: fs.ModeSymlink | 0o777}),
		"extra json":    pack(t, ok, css, file{name: "data.json", body: "{}"}),
		"no manifest":   pack(t, css),
		"missing style": pack(t, ok),
		"builtin id":    pack(t, file{name: "theme.json", body: strings.Replace(manifest, `"matcha"`, `"glass"`, 1)}, css),
		"bad id":        pack(t, file{name: "theme.json", body: strings.Replace(manifest, `"matcha"`, `"Matcha!"`, 1)}, css),
		"bad colour":    pack(t, file{name: "theme.json", body: strings.Replace(manifest, "#141811", "red", 1)}, css),
		"style outside": pack(t, file{name: "theme.json", body: strings.Replace(manifest, `["theme.css"]`, `["../x.css"]`, 1)}, css),
		"style not css": pack(t, file{name: "theme.json", body: strings.Replace(manifest, `["theme.css"]`, `["theme.png"]`, 1)}, css, file{name: "theme.png", body: "x"}),
		"not a zip":     []byte("hello"),
		"empty":         pack(t),
		"broken json":   pack(t, file{name: "theme.json", body: "{"}, css),
	}
	for name, archive := range cases {
		s := &Store{Dir: t.TempDir()}
		if _, err := s.Install(archive); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if entries, _ := os.ReadDir(s.Dir); len(entries) != 0 {
			t.Errorf("%s: a rejected package must leave nothing on disk, found %v", name, entries)
		}
	}
	// A manifest may not claim to be built-in or pick its own revision.
	s := &Store{Dir: t.TempDir()}
	m, err := s.Install(pack(t, file{name: "theme.json", body: strings.Replace(manifest, `"id"`, `"builtin":true,"rev":"x","id"`, 1)}, css))
	if err != nil || m.Builtin || m.Rev == "x" {
		t.Fatalf("builtin / rev must come from the server: %+v %v", m, err)
	}
}

func TestNilStoreIsBuiltinsOnly(t *testing.T) {
	var s *Store
	if len(s.List()) != len(Builtins) || !s.Known("glass") || s.Known("anime") || s.Known("matcha") {
		t.Fatal("a nil store lists the built-ins only")
	}
	if _, err := s.Install(nil); err == nil {
		t.Fatal("a nil store cannot install")
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/themes/x/y.css", nil))
	if rec.Code != 404 {
		t.Fatalf("nil store serves nothing: %d", rec.Code)
	}
}

const packManifest = `{"id":"europe","type":"characters","name":"欧洲","characters":[
 {"id":"germany","name":"德国","regions":["德国"],"art":"de.webp","hands":"de-hands.webp",
  "plate":{"top":0.307,"bottom":0.446},
  "palette":{"light":{"board":"#eef8f7","line":"#a8d8d3","track":"#d3eeeb","accent":"#2a8f86"},
             "dark":{"board":"#14262a","line":"#2b4d52","track":"#1d363a","accent":"#6fd3c9"}}}]}`

// A character pack adds figures to the 二次元 skin: it installs like any
// package but is not a skin a visitor (or the default setting) can pick.
func TestCharacterPack(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	m, err := s.Install(pack(t, file{name: "theme.json", body: packManifest}, file{name: "de.webp", body: "RIFF"}, file{name: "de-hands.webp", body: "RIFF"}))
	if err != nil {
		t.Fatal(err)
	}
	if m.Type != TypeCharacters || len(m.Characters) != 1 || m.Characters[0].Regions[0] != "德国" {
		t.Fatalf("manifest: %+v", m)
	}
	if s.Known("europe") {
		t.Fatal("a character pack is not a skin")
	}
	if got := s.List(); got[len(got)-1].ID != "europe" {
		t.Fatal("character packs are listed with the rest")
	}

	css := file{name: "a.css", body: "a{}"}
	art := file{name: "de.webp", body: "RIFF"}
	hands := file{name: "de-hands.webp", body: "RIFF"}
	swap := func(from, to string) file {
		return file{name: "theme.json", body: strings.Replace(packManifest, from, to, 1)}
	}
	skin := strings.Replace(manifest, `"styles"`, `"characters":[{"id":"x"}],"styles"`, 1)
	for name, archive := range map[string][]byte{
		"styles in a pack":    pack(t, swap(`"type":"characters",`, `"type":"characters","styles":["a.css"],`), art, hands, css),
		"characters in skin":  pack(t, file{name: "theme.json", body: skin}, file{name: "theme.css", body: "a{}"}),
		"unknown type":        pack(t, swap(`"characters",`, `"plugin",`), art, hands),
		"missing art file":    pack(t, file{name: "theme.json", body: packManifest}, hands),
		"missing hands file":  pack(t, file{name: "theme.json", body: packManifest}, art),
		"svg art":             pack(t, swap(`"de.webp"`, `"de.svg"`), hands, file{name: "de.svg", body: "<svg/>"}),
		"plate too thin":      pack(t, swap(`"bottom":0.446`, `"bottom":0.31`), art, hands),
		"plate out of range":  pack(t, swap(`"top":0.307`, `"top":0.05`), art, hands),
		"bad accent":          pack(t, swap(`"accent":"#2a8f86"`, `"accent":"teal"`), art, hands),
		"missing dark":        pack(t, swap(`"dark":{"board":"#14262a","line":"#2b4d52","track":"#1d363a","accent":"#6fd3c9"}`, `"dark":{}`), art, hands),
		"bad character id":    pack(t, swap(`"id":"germany"`, `"id":"Germany!"`), art, hands),
		"no characters":       pack(t, file{name: "theme.json", body: `{"id":"empty","type":"characters","name":"空"}`}),
		"duplicate character": pack(t, swap(`}}}]}`, `}}},{"id":"germany","name":"又一个","art":"de.webp","plate":{"top":0.3,"bottom":0.4},"palette":{"light":{"board":"#ffffff","line":"#ffffff","track":"#ffffff","accent":"#ffffff"},"dark":{"board":"#000000","line":"#000000","track":"#000000","accent":"#000000"}}}]}`), art, hands),
	} {
		s := &Store{Dir: t.TempDir()}
		if _, err := s.Install(archive); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// 市场装的包记得来源（版本与摘要，存在不对外发的 .market/ 里）；本地再传同一个 id 就忘掉它，
// 删除时一并删掉。
func TestMarketOrigin(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	archive := pack(t, file{name: "theme.json", body: manifest}, file{name: "theme.css", body: "a{}"})
	if _, err := s.InstallMarket(archive, Origin{}); err == nil {
		t.Fatal("a market install needs its release")
	}
	m, err := s.InstallMarket(archive, Origin{Version: "1.0.0", SHA256: strings.Repeat("a", 64), KeyID: "cc"})
	if err != nil || m.Source != SourceMarket {
		t.Fatalf("market install: %+v %v", m, err)
	}
	if o, ok := s.Origin("matcha"); !ok || o.Version != "1.0.0" || o.KeyID != "cc" {
		t.Fatalf("origin: %+v %v", o, ok)
	}
	if list := s.Installed(); len(list) != 1 || list[0].Source != SourceMarket {
		t.Fatalf("installed: %+v", list)
	}
	// The record is not reachable over HTTP, by id or as a dot file inside a package.
	serve := func(path string) int {
		mux := http.NewServeMux()
		mux.Handle("GET /themes/{id}/{file...}", s)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w.Code
	}
	os.WriteFile(filepath.Join(s.Dir, "matcha", ".secret.json"), []byte("{}"), 0o644)
	for _, path := range []string{"/themes/.market/matcha.json", "/themes/matcha/.secret.json", "/themes/matcha/sub/.x.css"} {
		if code := serve(path); code != 404 {
			t.Fatalf("%s: %d", path, code)
		}
	}
	if code := serve("/themes/matcha/theme.css"); code != 200 {
		t.Fatalf("theme.css: %d", code)
	}

	if m, err := s.Install(archive); err != nil || m.Source != "" {
		t.Fatalf("local upload: %+v %v", m, err)
	}
	if _, ok := s.Origin("matcha"); ok {
		t.Fatal("local upload kept the market origin")
	}
	s.InstallMarket(archive, Origin{Version: "1.0.0", SHA256: "x"})
	if err := s.Remove("matcha"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, ".market", "matcha.json")); !os.IsNotExist(err) {
		t.Fatal("remove left the market record")
	}
}

// 现代极简在 v1.2 是市场包，从 v1.3 起编进二进制：数据目录里留下的旧包不再列出，同 id 也装不上。
func TestBuiltinShadowsOldPackage(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	old := strings.Replace(manifest, `"matcha"`, `"shadcn"`, 1)
	if _, err := s.Install(pack(t, file{name: "theme.json", body: old}, file{name: "theme.css", body: "a{}"})); err == nil {
		t.Fatal("a package must not take a built-in id")
	}
	if err := os.MkdirAll(filepath.Join(s.Dir, "shadcn"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(s.Dir, "shadcn", "theme.json"), []byte(old), 0o644)
	for _, m := range s.Installed() {
		if m.ID == "shadcn" {
			t.Fatal("a leftover package under a built-in id must be ignored")
		}
	}
	if list := s.List(); list[0].ID != "shadcn" || !list[0].Builtin {
		t.Fatalf("现代极简 is the first built-in (the default): %+v", list[0])
	}
}

// 早于主题设置的二次元包（v1.0.0 没有 settings）也得有「角色包」菜单：角色包就在那里开关。
func TestAnimeAlwaysHasCharacterMenu(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	old := strings.Replace(manifest, `"matcha"`, `"anime"`, 1)
	if _, err := s.Install(pack(t, file{name: "theme.json", body: old}, file{name: "theme.css", body: "a{}"})); err != nil {
		t.Fatal(err)
	}
	for _, m := range s.Installed() {
		if m.ID == "anime" && (len(m.Settings) != 1 || m.Settings[0].Type != "characters") {
			t.Fatalf("anime settings: %+v", m.Settings)
		}
	}
}

// theme.json 的外观声明：base（基础层）、features（能力）、layout（布局变体）。合规的原样落进清单，
// 不合规的整包拒收、磁盘上不留东西，错误信息指出是哪一项。
func TestLookDeclarations(t *testing.T) {
	css := file{name: "theme.css", body: "a{}"}
	with := func(extra string) []byte {
		return pack(t, file{name: "theme.json", body: strings.Replace(manifest, `"styles"`, extra+`,"styles"`, 1)}, css)
	}
	s := &Store{Dir: t.TempDir()}
	m, err := s.Install(with(`"base":"plain","layout":{"card_sections":["metrics-grid","node-sub"],"density":"compact","default_view":"list"}`))
	if err != nil {
		t.Fatal(err)
	}
	if m.Base != "plain" || m.Layout == nil || strings.Join(m.Layout.CardSections, ",") != "metrics-grid,node-sub" || m.Layout.Density != "compact" || m.Layout.DefaultView != "list" {
		t.Fatalf("declarations must survive install: %+v %+v", m, m.Layout)
	}
	if got := s.Installed(); len(got) != 1 || got[0].Base != "plain" || got[0].Layout == nil {
		t.Fatalf("declarations must survive a re-read: %+v", got)
	}
	// 一段都不写也合法：板面只剩头部（名字、标签、详细）。
	if _, err := (&Store{Dir: t.TempDir()}).Install(with(`"layout":{"card_sections":[]}`)); err != nil {
		t.Fatalf("an empty card_sections is allowed: %v", err)
	}
	for name, c := range map[string]struct{ extra, want string }{
		"unknown base":     {`"base":"neon"`, "base"},
		"unknown feature":  {`"features":["sparkles"]`, "sparkles"},
		"characters":       {`"features":["characters"]`, "characters"},
		"unknown section":  {`"layout":{"card_sections":["chart"]}`, `"chart"`},
		"repeated section": {`"layout":{"card_sections":["network","network"]}`, "重复"},
		"bad density":      {`"layout":{"density":"tight"}`, "density"},
		"bad view":         {`"layout":{"default_view":"table"}`, "default_view"},
	} {
		s := &Store{Dir: t.TempDir()}
		_, err := s.Install(with(c.extra))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want an error naming %q, got %v", name, c.want, err)
		}
		if entries, _ := os.ReadDir(s.Dir); len(entries) != 0 {
			t.Errorf("%s: a rejected package must leave nothing on disk, found %v", name, entries)
		}
	}
	// 角色包不是外观，不带这些声明。
	chars := `{"id":"europe","type":"characters","name":"欧洲","base":"plain","characters":[{"id":"de","name":"德国","art":"de.webp","plate":{"top":0.3,"bottom":0.45},"palette":{"light":{"board":"#ffffff","line":"#ffffff","track":"#ffffff","accent":"#ffffff"},"dark":{"board":"#000000","line":"#000000","track":"#000000","accent":"#000000"}}}]}`
	if _, err := (&Store{Dir: t.TempDir()}).Install(pack(t, file{name: "theme.json", body: chars}, file{name: "de.webp", body: "RIFF"})); err == nil || !strings.Contains(err.Error(), "base") {
		t.Errorf("a character pack with base must be refused, got %v", err)
	}
}

// 二次元声明 characters 能力；早于 features 的老包（v1.1.0 及更早）由 Store 补上，面板只认这个能力。
func TestAnimeHasCharactersFeature(t *testing.T) {
	for name, body := range map[string]string{
		"declared": strings.Replace(strings.Replace(manifest, `"matcha"`, `"anime"`, 1), `"styles"`, `"features":["characters"],"styles"`, 1),
		"legacy":   strings.Replace(manifest, `"matcha"`, `"anime"`, 1),
	} {
		s := &Store{Dir: t.TempDir()}
		if _, err := s.Install(pack(t, file{name: "theme.json", body: body}, file{name: "theme.css", body: "a{}"})); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := s.Installed()
		if len(got) != 1 || strings.Join(got[0].Features, ",") != "characters" {
			t.Errorf("%s: anime features = %+v", name, got)
		}
	}
}
