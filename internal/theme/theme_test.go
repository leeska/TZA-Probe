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
	if len(s.List()) != len(Builtins) || !s.Known("anime") || s.Known("matcha") {
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
