// Package plugin keeps the installed declarative plugins: notification
// channels and probe target sets, each a single plugin.json interpreted by
// pkg/tzapkg (no code). Installed plugins live in <data>/plugins/<id>/plugin.json.
// Nothing here is served over HTTP. See
// docs/PLUGINS.md.
package plugin

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/leeska/TZA-ControlCenter/pkg/tzapkg"
)

// ErrNotFound means no plugin is installed under the id; it matches
// os.ErrNotExist, so callers need not import this package to recognise it.
var ErrNotFound error = notFound{}

type notFound struct{}

func (notFound) Error() string        { return "没有安装这个插件" }
func (notFound) Is(target error) bool { return target == os.ErrNotExist }

// Store keeps the installed plugins in Dir. A nil *Store or an empty Dir
// means "no plugins".
type Store struct {
	Dir string
	mu  sync.RWMutex
}

// List returns the installed plugins that still parse, by id.
func (s *Store) List() []tzapkg.Plugin {
	if s == nil || s.Dir == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return nil
	}
	out := []tzapkg.Plugin{}
	for _, e := range entries {
		if !e.IsDir() || !tzapkg.ValidID(e.Name()) {
			continue
		}
		if p, err := s.read(e.Name()); err == nil {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Plugin loads one installed plugin. A missing one is ErrNotFound; one whose
// plugin.json no longer passes validation is an error that says so.
func (s *Store) Plugin(id string) (tzapkg.Plugin, error) {
	if s == nil || s.Dir == "" || !tzapkg.ValidID(id) {
		return tzapkg.Plugin{}, ErrNotFound
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.read(id)
}

// read parses an installed plugin; callers hold the lock.
func (s *Store) read(id string) (tzapkg.Plugin, error) {
	raw, err := os.ReadFile(filepath.Join(s.Dir, id, "plugin.json"))
	if errors.Is(err, os.ErrNotExist) {
		return tzapkg.Plugin{}, ErrNotFound
	}
	if err != nil {
		return tzapkg.Plugin{}, err
	}
	p, err := tzapkg.ParsePlugin(raw)
	if err != nil {
		return tzapkg.Plugin{}, fmt.Errorf("插件 %s 的 plugin.json 无效：%w", id, err)
	}
	if p.ID != id {
		return tzapkg.Plugin{}, fmt.Errorf("插件 %s 的 plugin.json 写的 id 是 %s", id, p.ID)
	}
	return p, nil
}

// Install validates and installs an uploaded plugin, replacing one with the
// same id. Nothing is written unless the archive passes.
func (s *Store) Install(archive []byte) (tzapkg.Plugin, error) {
	if s == nil || s.Dir == "" {
		return tzapkg.Plugin{}, errors.New("这个 Server 没有配置插件目录")
	}
	p, err := tzapkg.OpenPlugin(archive)
	if err != nil {
		return tzapkg.Plugin{}, err
	}
	raw, err := pluginJSON(archive)
	if err != nil {
		return tzapkg.Plugin{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return tzapkg.Plugin{}, err
	}
	tmp, err := os.MkdirTemp(s.Dir, ".upload-")
	if err != nil {
		return tzapkg.Plugin{}, err
	}
	defer os.RemoveAll(tmp)
	if err := os.WriteFile(filepath.Join(tmp, "plugin.json"), raw, 0o644); err != nil {
		return tzapkg.Plugin{}, err
	}
	final := filepath.Join(s.Dir, p.ID)
	old := ""
	if _, err := os.Stat(final); err == nil {
		old = filepath.Join(s.Dir, ".old-"+p.ID+"-"+random())
		if err := os.Rename(final, old); err != nil {
			return tzapkg.Plugin{}, err
		}
	}
	if err := os.Rename(tmp, final); err != nil {
		if old != "" {
			os.Rename(old, final)
		}
		return tzapkg.Plugin{}, err
	}
	if old != "" {
		os.RemoveAll(old)
	}
	return s.read(p.ID)
}

// pluginJSON pulls the (already validated) plugin.json out of the archive.
func pluginJSON(archive []byte) ([]byte, error) {
	files, err := tzapkg.Files(archive)
	if err != nil {
		return nil, err
	}
	raw, ok := files["plugin.json"]
	if !ok {
		return nil, errors.New("压缩包里没有 plugin.json")
	}
	return raw, nil
}

// Remove deletes an installed plugin. Whether it may go (a notification
// channel in use) is the caller's rule, see service.RemovePlugin.
func (s *Store) Remove(id string) error {
	if s == nil || s.Dir == "" || !tzapkg.ValidID(id) {
		return ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Join(s.Dir, id)
	if _, err := os.Stat(filepath.Join(dir, "plugin.json")); err != nil {
		return ErrNotFound
	}
	trash := filepath.Join(s.Dir, ".old-"+id+"-"+random())
	if err := os.Rename(dir, trash); err != nil {
		return err
	}
	return os.RemoveAll(trash)
}

func random() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}
