// Package theme is the panel's skin registry: the built-in skins plus theme
// packages an administrator uploads.
//
// A theme package is a zip holding theme.json, one or more stylesheets and the
// images / fonts they reference. It carries no script: the page CSP only runs
// the panel's own JavaScript, and a package's stylesheets load from this
// origin, so they cannot reach other hosts either. Installed packages live in
// <data>/themes/<id>/ and are served under /themes/<id>/. A package installed
// from the TZA-CC market also has <data>/themes/.market/<id>.json recording the
// signed release; that directory is never served. See docs/THEMES.md.
package theme

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/leeska/TZA-Probe/pkg/tzapkg"
)

// MaxUpload caps one uploaded archive.
const MaxUpload = tzapkg.MaxUpload

// The package format lives in pkg/tzapkg, shared with TZA-CC.
type (
	Colors    = tzapkg.Colors
	Manifest  = tzapkg.Manifest
	Character = tzapkg.Character
	Plate     = tzapkg.Plate
	Palette   = tzapkg.Palette
	Setting   = tzapkg.SkinSetting
)

const (
	TypeSkin       = tzapkg.TypeSkin
	TypeCharacters = tzapkg.TypeCharacters
	SourceMarket   = tzapkg.SourceMarket
)

// Origin is what the Server remembers about a package installed from the
// market: the signed release, so the market page can offer updates.
type Origin struct {
	Version     string    `json:"version"`
	SHA256      string    `json:"sha256"`
	KeyID       string    `json:"key_id"`
	InstalledAt time.Time `json:"installed_at"`
}

// originDir holds the market records, beside the packages. Its name is not a
// valid package id, so neither Installed nor ServeHTTP ever reach it.
const originDir = ".market"

// Builtins are the skins compiled into web/: their stylesheets are always on
// the page, scoped by html[data-skin]. The first is the default skin
// (model.DefaultPanelSkin). 现代极简 and 液态玻璃 are compiled in; the other
// official looks (复古印刷 / 二次元) are packages in themes/ that the market
// distributes.
var Builtins = []Manifest{
	{ID: "shadcn", Name: "现代极简", Hint: "中性灰 · 发丝描边 · 小圆角", Chrome: Colors{Light: "#ffffff", Dark: "#0a0a0a"}, Swatch: []string{"#f5f5f5", "#ffffff", "#171717"}, Builtin: true},
	{ID: "glass", Name: "液态玻璃", Hint: "毛玻璃 · 圆角 · 柔和阴影", Chrome: Colors{Light: "#edf2fb", Dark: "#0f141c"}, Swatch: []string{"#eef3fc", "#ffffff", "#0071e3"}, Builtin: true},
}

// AnimeSkin is the id of the official 二次元 package; character packs only
// take effect while it is installed (the panel's character code keys on it).
const AnimeSkin = tzapkg.AnimeID

// IsBuiltin reports whether id is a compiled-in skin. A package under such an
// id (现代极简 was a market package in v1.2) is ignored and cannot be
// installed again; the market hides it.
func IsBuiltin(id string) bool { return builtin(id) }

func builtin(id string) bool {
	for _, b := range Builtins {
		if b.ID == id {
			return true
		}
	}
	return false
}

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
		if !e.IsDir() || !tzapkg.ValidID(e.Name()) || builtin(e.Name()) {
			continue
		}
		if m, err := s.read(e.Name()); err == nil {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Known reports whether id names a skin a visitor may use (character packs
// are not skins).
func (s *Store) Known(id string) bool {
	if builtin(id) {
		return true
	}
	if s == nil || s.Dir == "" || !tzapkg.ValidID(id) {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, err := s.read(id)
	return err == nil && m.Type == TypeSkin
}

// read loads an installed manifest; callers hold the lock.
func (s *Store) read(id string) (Manifest, error) {
	dir := filepath.Join(s.Dir, id)
	raw, err := os.ReadFile(filepath.Join(dir, "theme.json"))
	if err != nil {
		return Manifest{}, err
	}
	m, err := tzapkg.Parse(raw)
	if err != nil || m.ID != id {
		return Manifest{}, fmt.Errorf("theme %s: invalid manifest", id)
	}
	// rev changes whenever the package is replaced, so stylesheet URLs bust caches.
	if info, err := os.Stat(dir); err == nil {
		m.Rev = fmt.Sprintf("%x", info.ModTime().UnixNano())
	}
	if _, ok := s.origin(id); ok {
		m.Source = SourceMarket
	}
	if id == AnimeSkin {
		m.Settings = withCharacters(m.Settings)
		// 二次元在有 features 之前（v1.1.0 及更早）就画角色：老包没声明也补上，面板只认这个能力。
		if !slices.Contains(m.Features, tzapkg.FeatureCharacters) {
			m.Features = append(m.Features, tzapkg.FeatureCharacters)
		}
	}
	return m, nil
}

// withCharacters makes sure 二次元's settings menu has the 角色包 entry: the
// character packs live there, so a 二次元 package that predates theme
// settings (v1.0.0) still gets one.
func withCharacters(list []Setting) []Setting {
	for _, st := range list {
		if st.Type == tzapkg.SettingCharacters {
			return list
		}
	}
	return append([]Setting{{ID: "characters", Type: tzapkg.SettingCharacters, Label: "角色包", Hint: "关掉的角色包不参与挑角色"}}, list...)
}

func (s *Store) originPath(id string) string { return filepath.Join(s.Dir, originDir, id+".json") }

// origin reads a market record; callers hold the lock.
func (s *Store) origin(id string) (Origin, bool) {
	raw, err := os.ReadFile(s.originPath(id))
	if err != nil {
		return Origin{}, false
	}
	var o Origin
	if json.Unmarshal(raw, &o) != nil || o.Version == "" {
		return Origin{}, false
	}
	return o, true
}

// Origin reports the market release an installed package came from; ok is
// false for a local upload (or a built-in).
func (s *Store) Origin(id string) (Origin, bool) {
	if s == nil || s.Dir == "" || !tzapkg.ValidID(id) {
		return Origin{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, err := os.Stat(filepath.Join(s.Dir, id, "theme.json")); err != nil {
		return Origin{}, false
	}
	return s.origin(id)
}

// Install unpacks and validates a locally uploaded package, replacing an
// installed one with the same id (and forgetting that one's market origin).
// Nothing is written unless the whole archive passes.
func (s *Store) Install(archive []byte) (Manifest, error) { return s.install(archive, nil) }

// InstallMarket installs a package the market client has already verified
// against its TZA-CC signature, and records where it came from.
func (s *Store) InstallMarket(archive []byte, o Origin) (Manifest, error) {
	if o.Version == "" || o.SHA256 == "" {
		return Manifest{}, errors.New("缺少市场版本信息")
	}
	return s.install(archive, &o)
}

func (s *Store) install(archive []byte, o *Origin) (Manifest, error) {
	if s == nil || s.Dir == "" {
		return Manifest{}, errors.New("这个 Server 没有配置主题目录")
	}
	m, files, err := tzapkg.Open(archive)
	if err != nil {
		return Manifest{}, err
	}
	if builtin(m.ID) {
		return Manifest{}, fmt.Errorf("id %q 是内置主题，换一个名字", m.ID)
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
	if err := s.setOrigin(m.ID, o); err != nil {
		return Manifest{}, fmt.Errorf("主题包已装好，但记不下市场来源：%w", err)
	}
	return s.read(m.ID)
}

// setOrigin writes (or, for nil, removes) the market record of id; callers
// hold the lock.
func (s *Store) setOrigin(id string, o *Origin) error {
	path := s.originPath(id)
	if o == nil {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(o)
	if err != nil {
		return err
	}
	tmp := path + ".tmp-" + random()
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// Remove deletes an installed package. Built-ins cannot be removed.
func (s *Store) Remove(id string) error {
	if builtin(id) {
		return errors.New("内置主题不能删除")
	}
	if s == nil || s.Dir == "" || !tzapkg.ValidID(id) {
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
	s.setOrigin(id, nil)
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
	ctype, ok := tzapkg.ContentType(file)
	// Dot files are never package content (zips may not carry them by accident
	// either way): refusing them keeps any server-side record out of reach.
	hidden := strings.HasPrefix(file, ".") || strings.Contains(file, "/.")
	if s == nil || s.Dir == "" || !tzapkg.ValidID(id) || !tzapkg.SafePath(file) || !ok || builtin(id) || hidden {
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
