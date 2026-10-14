// Package theme is the panel's skin registry: the built-in skins plus theme
// packages an administrator uploads.
//
// A theme package is a zip holding theme.json, one or more stylesheets and the
// images / fonts they reference. It carries no script: the page CSP only runs
// the panel's own JavaScript, and a package's stylesheets load from this
// origin, so they cannot reach other hosts either. Installed packages live in
// <data>/themes/<id>/ and are served under /themes/<id>/. See docs/THEMES.md.
package theme

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

// Limits for one uploaded package. The archive itself is capped by the HTTP
// handler (MaxUpload); these bound what it may expand to.
const (
	MaxUpload   = 20 << 20
	maxUnpacked = 48 << 20
	maxFile     = 16 << 20
	maxFiles    = 300
	maxStyles   = 8
)

// Colors are the two browser-chrome colours (meta theme-color) of a skin.
type Colors struct {
	Light string `json:"light"`
	Dark  string `json:"dark"`
}

// Manifest describes one skin. Builtin and Rev are set by the server, never
// read from an uploaded theme.json.
type Manifest struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Hint    string   `json:"hint,omitempty"`
	Version string   `json:"version,omitempty"`
	Author  string   `json:"author,omitempty"`
	Chrome  Colors   `json:"chrome"`
	Swatch  []string `json:"swatch"`
	Styles  []string `json:"styles,omitempty"`
	Builtin bool     `json:"builtin,omitempty"`
	Rev     string   `json:"rev,omitempty"`
}

// Builtins are the skins compiled into web/: their stylesheets are always on
// the page, scoped by html[data-skin]. Order is the order the skin menu shows.
var Builtins = []Manifest{
	{ID: "glass", Name: "液态玻璃", Hint: "毛玻璃 · 圆角 · 柔和阴影", Chrome: Colors{"#edf2fb", "#0f141c"}, Swatch: []string{"#eef3fc", "#ffffff", "#0071e3"}, Builtin: true},
	{ID: "press", Name: "复古印刷", Hint: "纸张方格 · 硬边框 · 衬线标题", Chrome: Colors{"#f8f1df", "#15120f"}, Swatch: []string{"#f8f1df", "#fffaf0", "#b72f24"}, Builtin: true},
	{ID: "shadcn", Name: "现代极简", Hint: "中性灰 · 发丝描边 · 小圆角", Chrome: Colors{"#ffffff", "#0a0a0a"}, Swatch: []string{"#f5f5f5", "#ffffff", "#171717"}, Builtin: true},
	{ID: "anime", Name: "二次元", Hint: "线路机角色 · 樱花粉", Chrome: Colors{"#fdeef6", "#150f29"}, Swatch: []string{"#fdeef6", "#ffffff", "#ff4f9a"}, Builtin: true},
}

func builtin(id string) bool {
	for _, b := range Builtins {
		if b.ID == id {
			return true
		}
	}
	return false
}

var (
	idPattern   = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)
	hexColor    = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	safePath    = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)
	contentType = map[string]string{
		".css":   "text/css; charset=utf-8",
		".json":  "application/json",
		".webp":  "image/webp",
		".png":   "image/png",
		".jpg":   "image/jpeg",
		".jpeg":  "image/jpeg",
		".gif":   "image/gif",
		".avif":  "image/avif",
		".woff2": "font/woff2",
		".woff":  "font/woff",
	}
)

// Store keeps the installed packages in Dir. A nil *Store or an empty Dir
// means "built-ins only".
type Store struct {
	Dir string
	mu  sync.RWMutex
}

// List returns the built-ins followed by the installed packages, by id.
func (s *Store) List() []Manifest {
	out := append([]Manifest{}, Builtins...)
	return append(out, s.Installed()...)
}

// Installed returns the uploaded packages only, by id.
func (s *Store) Installed() []Manifest {
	if s == nil || s.Dir == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return nil
	}
	out := []Manifest{}
	for _, e := range entries {
		if !e.IsDir() || !idPattern.MatchString(e.Name()) {
			continue
		}
		if m, err := s.read(e.Name()); err == nil {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Known reports whether id names a skin a visitor may use.
func (s *Store) Known(id string) bool {
	if builtin(id) {
		return true
	}
	if s == nil || s.Dir == "" || !idPattern.MatchString(id) {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, err := s.read(id)
	return err == nil
}

// read loads an installed manifest; callers hold the lock.
func (s *Store) read(id string) (Manifest, error) {
	dir := filepath.Join(s.Dir, id)
	raw, err := os.ReadFile(filepath.Join(dir, "theme.json"))
	if err != nil {
		return Manifest{}, err
	}
	m, err := parse(raw)
	if err != nil || m.ID != id {
		return Manifest{}, fmt.Errorf("theme %s: invalid manifest", id)
	}
	// rev changes whenever the package is replaced, so stylesheet URLs bust caches.
	if info, err := os.Stat(dir); err == nil {
		m.Rev = fmt.Sprintf("%x", info.ModTime().UnixNano())
	}
	return m, nil
}

// parse decodes and checks a theme.json; it never trusts Builtin / Rev.
func parse(raw []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, errors.New("theme.json 不是有效的 JSON：" + err.Error())
	}
	m.Builtin, m.Rev = false, ""
	m.Name, m.Hint, m.Version, m.Author = strings.TrimSpace(m.Name), strings.TrimSpace(m.Hint), strings.TrimSpace(m.Version), strings.TrimSpace(m.Author)
	switch {
	case !idPattern.MatchString(m.ID):
		return Manifest{}, errors.New("id 须为 2–32 位小写字母、数字或连字符，以字母开头")
	case builtin(m.ID):
		return Manifest{}, fmt.Errorf("id %q 是内置主题，换一个名字", m.ID)
	case m.Name == "" || utf8.RuneCountInString(m.Name) > 30:
		return Manifest{}, errors.New("name 必填，最多 30 个字")
	case utf8.RuneCountInString(m.Hint) > 60, utf8.RuneCountInString(m.Version) > 20, utf8.RuneCountInString(m.Author) > 40:
		return Manifest{}, errors.New("hint 最多 60 字，version 最多 20 字，author 最多 40 字")
	case !hexColor.MatchString(m.Chrome.Light) || !hexColor.MatchString(m.Chrome.Dark):
		return Manifest{}, errors.New("chrome.light / chrome.dark 须为 #rrggbb 颜色")
	case len(m.Swatch) != 3:
		return Manifest{}, errors.New("swatch 须为 3 个 #rrggbb 颜色")
	case len(m.Styles) == 0 || len(m.Styles) > maxStyles:
		return Manifest{}, fmt.Errorf("styles 须列出 1–%d 个样式表", maxStyles)
	}
	for _, c := range m.Swatch {
		if !hexColor.MatchString(c) {
			return Manifest{}, errors.New("swatch 须为 3 个 #rrggbb 颜色")
		}
	}
	for _, p := range m.Styles {
		if !safePath.MatchString(p) || path.Ext(p) != ".css" || strings.Contains(p, "..") {
			return Manifest{}, fmt.Errorf("styles 里的 %q 须为包内的 .css 相对路径（只用字母、数字、. _ - /）", p)
		}
	}
	return m, nil
}

// Install unpacks and validates a package, replacing an installed one with the
// same id. Nothing is written unless the whole archive passes.
func (s *Store) Install(archive []byte) (Manifest, error) {
	if s == nil || s.Dir == "" {
		return Manifest{}, errors.New("这个 Server 没有配置主题目录")
	}
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return Manifest{}, errors.New("不是有效的 zip 压缩包")
	}
	files, err := entries(zr)
	if err != nil {
		return Manifest{}, err
	}
	rawManifest, ok := files["theme.json"]
	if !ok {
		return Manifest{}, errors.New("压缩包根目录（或唯一的顶层文件夹）里没有 theme.json")
	}
	m, err := parse(rawManifest)
	if err != nil {
		return Manifest{}, err
	}
	for _, p := range m.Styles {
		if _, ok := files[p]; !ok {
			return Manifest{}, fmt.Errorf("styles 里的 %q 不在压缩包里", p)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return Manifest{}, err
	}
	tmp, err := os.MkdirTemp(s.Dir, ".upload-")
	if err != nil {
		return Manifest{}, err
	}
	defer os.RemoveAll(tmp)
	for name, data := range files {
		target := filepath.Join(tmp, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return Manifest{}, err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return Manifest{}, err
		}
	}
	final := filepath.Join(s.Dir, m.ID)
	old := ""
	if _, err := os.Stat(final); err == nil {
		old = filepath.Join(s.Dir, ".old-"+m.ID+"-"+random())
		if err := os.Rename(final, old); err != nil {
			return Manifest{}, err
		}
	}
	if err := os.Rename(tmp, final); err != nil {
		if old != "" {
			os.Rename(old, final)
		}
		return Manifest{}, err
	}
	if old != "" {
		os.RemoveAll(old)
	}
	return s.read(m.ID)
}

// entries reads every regular file of the archive into memory after checking
// names, types and sizes. A single top-level folder (how most zip tools pack a
// directory) is stripped so theme.json sits at the root.
func entries(zr *zip.Reader) (map[string][]byte, error) {
	type item struct {
		name string
		f    *zip.File
	}
	items := []item{}
	for _, f := range zr.File {
		name := f.Name
		if strings.HasSuffix(name, "/") || strings.HasPrefix(name, "__MACOSX/") || path.Base(name) == ".DS_Store" {
			continue
		}
		if f.Mode()&fs.ModeType != 0 {
			return nil, fmt.Errorf("压缩包里的 %q 不是普通文件（不收符号链接）", name)
		}
		if !safePath.MatchString(name) || strings.Contains(name, "..") {
			return nil, fmt.Errorf("压缩包里的文件名 %q 不合规：只用字母、数字、. _ - 和 /", name)
		}
		items = append(items, item{name, f})
	}
	if len(items) == 0 {
		return nil, errors.New("压缩包是空的")
	}
	if len(items) > maxFiles {
		return nil, fmt.Errorf("文件太多（最多 %d 个）", maxFiles)
	}
	// Strip one shared top-level folder.
	if first := strings.SplitN(items[0].name, "/", 2); len(first) == 2 {
		prefix := first[0] + "/"
		shared := true
		for _, it := range items {
			if !strings.HasPrefix(it.name, prefix) {
				shared = false
				break
			}
		}
		if shared {
			for i := range items {
				items[i].name = strings.TrimPrefix(items[i].name, prefix)
			}
		}
	}
	out := map[string][]byte{}
	var total int64
	for _, it := range items {
		ext := strings.ToLower(path.Ext(it.name))
		if _, ok := contentType[ext]; !ok {
			return nil, fmt.Errorf("不支持的文件类型 %q：只收 CSS、theme.json、webp / png / jpg / gif / avif 图片和 woff / woff2 字体（不收脚本、HTML、SVG）", it.name)
		}
		if ext == ".json" && it.name != "theme.json" {
			return nil, fmt.Errorf("除 theme.json 外不收 JSON 文件：%q", it.name)
		}
		if it.f.UncompressedSize64 > maxFile {
			return nil, fmt.Errorf("%q 太大（单个文件最多 %d MB）", it.name, maxFile>>20)
		}
		rc, err := it.f.Open()
		if err != nil {
			return nil, fmt.Errorf("读不出 %q：%v", it.name, err)
		}
		// Read one byte past the limit: a lying header cannot sneak a bigger file through.
		data, err := io.ReadAll(io.LimitReader(rc, maxFile+1))
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("读不出 %q：%v", it.name, err)
		}
		if len(data) > maxFile {
			return nil, fmt.Errorf("%q 太大（单个文件最多 %d MB）", it.name, maxFile>>20)
		}
		total += int64(len(data))
		if total > maxUnpacked {
			return nil, fmt.Errorf("解压后太大（最多 %d MB）", maxUnpacked>>20)
		}
		if _, dup := out[it.name]; dup {
			return nil, fmt.Errorf("压缩包里有重名文件 %q", it.name)
		}
		out[it.name] = data
	}
	return out, nil
}

// Remove deletes an installed package. Built-ins cannot be removed.
func (s *Store) Remove(id string) error {
	if builtin(id) {
		return errors.New("内置主题不能删除")
	}
	if s == nil || s.Dir == "" || !idPattern.MatchString(id) {
		return os.ErrNotExist
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Join(s.Dir, id)
	if _, err := os.Stat(filepath.Join(dir, "theme.json")); err != nil {
		return os.ErrNotExist
	}
	trash := filepath.Join(s.Dir, ".old-"+id+"-"+random())
	if err := os.Rename(dir, trash); err != nil {
		return err
	}
	return os.RemoveAll(trash)
}

// ServeHTTP serves /themes/<id>/<file> for installed packages: whitelisted
// types only, never sniffed, revalidated by Last-Modified.
func (s *Store) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	id, file := r.PathValue("id"), r.PathValue("file")
	ctype, ok := contentType[strings.ToLower(path.Ext(file))]
	if s == nil || s.Dir == "" || !idPattern.MatchString(id) || !safePath.MatchString(file) || strings.Contains(file, "..") || !ok || builtin(id) {
		http.NotFound(w, r)
		return
	}
	s.mu.RLock()
	f, err := os.Open(filepath.Join(s.Dir, id, filepath.FromSlash(file)))
	s.mu.RUnlock()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "", info.ModTime(), f)
}

func random() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}
