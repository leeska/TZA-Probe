// Package theme is the panel's skin registry: the built-in skins plus theme
// packages an administrator uploads.
//
// CSS skins extend the core presentation. A frontend theme owns an HTML/JS
// document, served with an opaque-origin sandbox and a public-only bridge.
// Installed packages live in <data>/themes/<id>/. See docs/THEMES.md.
package theme

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/leeska/TZA-ControlCenter/pkg/tzapkg"
)

// MaxUpload caps one uploaded archive.
const MaxUpload = tzapkg.MaxUpload

// The package format lives in pkg/tzapkg.
type (
	Colors     = tzapkg.Colors
	Manifest   = tzapkg.Manifest
	Decoration = tzapkg.Decoration
	Setting    = tzapkg.SkinSetting
)

const (
	TypeSkin  = tzapkg.TypeSkin
	TypeAddon = tzapkg.TypeAddon
)

// Builtins are the skins compiled into web/: their stylesheets are always on
// the page, scoped by html[data-skin]. The first is the default skin
// (model.DefaultPanelSkin). Only 现代极简 is compiled in; all other looks
// are packages in themes/ that an administrator uploads.
var Builtins = []Manifest{
	{ID: "shadcn", Name: "现代极简", Hint: "中性灰 · 发丝描边 · 小圆角", Chrome: Colors{Light: "#ffffff", Dark: "#0a0a0a"}, Swatch: []string{"#f5f5f5", "#ffffff", "#171717"}, Builtin: true},
}

// IsBuiltin reports whether id is a compiled-in skin. A package under such an
// id (现代极简 was a package in v1.2) is ignored and cannot be
// installed again.
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

// Known reports whether id names a skin a visitor may use (add-ons are not
// skins).
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
	return m, nil
}

// Install unpacks and validates an uploaded package, replacing an installed
// one with the same id. Nothing is written unless the whole archive passes.
func (s *Store) Install(archive []byte) (Manifest, error) {
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
	return s.read(m.ID)
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
	m, manifestErr := s.read(id)
	if manifestErr != nil || !tzapkg.ThemeFileAllowed(m, file) {
		s.mu.RUnlock()
		http.NotFound(w, r)
		return
	}
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
	if m.Frontend != nil || m.SettingsUI != nil {
		// The CSP sandbox also applies when someone opens a package document directly.
		// No allow-same-origin: package JS cannot see sessions, storage or the parent DOM.
		w.Header().Del("X-Frame-Options")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		assets := "http://" + r.Host + "/themes/" + id + "/ https://" + r.Host + "/themes/" + id + "/"
		w.Header().Set("Content-Security-Policy", "sandbox allow-scripts; default-src 'none'; script-src 'self' 'unsafe-inline' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; connect-src "+assets+"; worker-src 'none'; frame-src 'none'; frame-ancestors 'self'; base-uri 'none'; form-action 'none'")
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
