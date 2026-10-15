// Package plugin keeps the installed declarative plugins: notification
// channels and probe target sets, each a single plugin.json interpreted by
// pkg/tzapkg (no code). Installed plugins live in <data>/plugins/<id>/plugin.json;
// one installed from the TZA-CC market also has <data>/plugins/.market/<id>.json
// recording the signed release. Nothing here is served over HTTP. See
// docs/PLUGINS.md.
package plugin

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/leeska/TZA-Probe/pkg/tzapkg"
)

// Origin is what the Server remembers about a plugin installed from the market.
type Origin struct {
	Version     string    `json:"version"`
	SHA256      string    `json:"sha256"`
	KeyID       string    `json:"key_id"`
	InstalledAt time.Time `json:"installed_at"`
}

const originDir = ".market"

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
	if _, ok := s.origin(id); ok {
		p.Source = tzapkg.SourceMarket
	}
	return p, nil
}

func (s *Store) originPath(id string) string { return filepath.Join(s.Dir, originDir, id+".json") }

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

// Origin reports the market release an installed plugin came from; ok is
// false for a local upload.
func (s *Store) Origin(id string) (Origin, bool) {
	if s == nil || s.Dir == "" || !tzapkg.ValidID(id) {
		return Origin{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, err := os.Stat(filepath.Join(s.Dir, id, "plugin.json")); err != nil {
		return Origin{}, false
	}
	return s.origin(id)
}

// Install validates and installs a locally uploaded plugin, replacing one
// with the same id (and forgetting that one's market origin). Nothing is
// written unless the archive passes.
func (s *Store) Install(archive []byte) (tzapkg.Plugin, error) { return s.install(archive, nil) }

// InstallMarket installs a plugin the market client has already verified
// against its TZA-CC signature, and records where it came from.
func (s *Store) InstallMarket(archive []byte, o Origin) (tzapkg.Plugin, error) {
	if o.Version == "" || o.SHA256 == "" {
		return tzapkg.Plugin{}, errors.New("缺少市场版本信息")
	}
	return s.install(archive, &o)
}

func (s *Store) install(archive []byte, o *Origin) (tzapkg.Plugin, error) {
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
	if err := s.setOrigin(p.ID, o); err != nil {
		return tzapkg.Plugin{}, fmt.Errorf("插件已装好，但记不下市场来源：%w", err)
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
	s.setOrigin(id, nil)
	return os.RemoveAll(trash)
}

func random() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}
