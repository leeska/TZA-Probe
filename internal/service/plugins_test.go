package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leeska/TZA-Probe/internal/market/markettest"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/notify"
	"github.com/leeska/TZA-Probe/internal/plugin"
)

func withPlugins(t *testing.T, ids ...string) (*Service, *plugin.Store) {
	t.Helper()
	svc := open(t)
	store := &plugin.Store{Dir: filepath.Join(t.TempDir(), "plugins")}
	for _, id := range ids {
		if _, err := store.Install(markettest.PluginZip(t, id, "")); err != nil {
			t.Fatal(err)
		}
	}
	svc.Plugins = store
	return svc, store
}

// 通知插件渠道：按插件的设置校验，密钥留空沿用已存的、从不回显；PATCH 只改传了的项；
// 正被用作渠道的插件删不掉；插件失效时公开视图说清原因。
func TestPluginChannel(t *testing.T) {
	svc, store := withPlugins(t, "ntfy", "gotify")
	ctx := context.Background()
	save := func(c notify.Config) error { _, err := svc.ReplaceNotification(ctx, c); return err }

	if err := save(notify.Config{Channel: "plugin", Plugin: "ntfy", PluginSettings: map[string]string{"topic": "bad topic!"}}); !errors.Is(err, model.ErrInvalid) || !strings.Contains(err.Error(), "主题") {
		t.Fatalf("pattern: %v", err)
	}
	if err := save(notify.Config{Channel: "plugin", Plugin: "missing"}); !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("missing plugin: %v", err)
	}
	if err := save(notify.Config{Channel: "plugin", Plugin: "ntfy", PluginSettings: map[string]string{"topic": "probe", "token": "tk_one", "junk": "x"}}); err != nil {
		t.Fatal(err)
	}
	stored, _ := svc.Notification(ctx)
	if stored.PluginSettings["server"] != "https://ntfy.sh" || stored.PluginSettings["token"] != "tk_one" || stored.PluginSettings["junk"] != "" {
		t.Fatalf("stored: %+v", stored.PluginSettings)
	}
	// The form resubmits without the secret: it is kept.
	if err := save(notify.Config{Channel: "plugin", Plugin: "ntfy", PluginSettings: map[string]string{"topic": "probe2", "server": "https://ntfy.sh"}}); err != nil {
		t.Fatal(err)
	}
	stored, _ = svc.Notification(ctx)
	if stored.PluginSettings["token"] != "tk_one" || stored.PluginSettings["topic"] != "probe2" {
		t.Fatalf("blank secret: %+v", stored.PluginSettings)
	}
	view := svc.PublicNotification(stored)
	raw, _ := json.Marshal(view)
	if strings.Contains(string(raw), "tk_one") || view["plugin_name"] != "ntfy" || view["plugin_secrets"].(map[string]bool)["token"] != true || view["plugin_settings"].(map[string]string)["topic"] != "probe2" {
		t.Fatalf("public view: %s", raw)
	}
	// PATCH: only what is given.
	if _, err := svc.PatchNotification(ctx, notify.Config{PluginSettings: map[string]string{"topic": "probe3"}}); err != nil {
		t.Fatal(err)
	}
	stored, _ = svc.Notification(ctx)
	if stored.Channel != "plugin" || stored.PluginSettings["topic"] != "probe3" || stored.PluginSettings["token"] != "tk_one" {
		t.Fatalf("patch: %+v", stored)
	}
	// Another plugin's secrets do not carry over.
	if _, err := svc.PatchNotification(ctx, notify.Config{Plugin: "gotify", PluginSettings: map[string]string{"server": "http://10.0.0.5:8080"}}); !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("gotify without its token: %v", err)
	}

	if err := svc.RemovePlugin(ctx, "ntfy"); !errors.Is(err, model.ErrConflict) || !strings.Contains(err.Error(), "配置出口") {
		t.Fatalf("remove in use: %v", err)
	}
	if err := svc.RemovePlugin(ctx, "gotify"); err != nil {
		t.Fatal(err)
	}
	if err := svc.RemovePlugin(ctx, "gotify"); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("remove twice: %v", err)
	}
	// The plugin breaks behind the Server's back: the view says so.
	os.WriteFile(filepath.Join(store.Dir, "ntfy", "plugin.json"), []byte(`{}`), 0o644)
	if view := svc.PublicNotification(stored); view["plugin_error"] == nil {
		t.Fatalf("broken plugin not reported: %v", view)
	}
	// Switching away drops the plugin settings.
	if err := save(notify.Config{Channel: "none"}); err != nil {
		t.Fatal(err)
	}
	if stored, _ = svc.Notification(ctx); stored.Plugin != "" || stored.PluginSettings != nil {
		t.Fatalf("switch away: %+v", stored)
	}
}

func TestCatalogMergesTargetSets(t *testing.T) {
	svc, store := withPlugins(t, "public-dns", "ntfy")
	ctx := context.Background()
	c, err := svc.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	base, _ := builtinCatalog()
	added := 0
	for _, x := range c.Targets {
		if x.Plugin != "" {
			added++
			if x.Plugin != "public-dns" || x.PluginName != "公共 DNS" || !strings.HasPrefix(x.ID, "plugin:public-dns:") {
				t.Fatalf("plugin target: %+v", x)
			}
		}
	}
	if added == 0 || len(c.Targets) != len(base.Targets)+added || len(c.Plugins) != 1 || c.Plugins[0].Count != added {
		t.Fatalf("merge: %d added, %d total, sources %+v", added, len(c.Targets), c.Plugins)
	}
	if len(base.Targets) == len(c.Targets) {
		t.Fatal("built-in catalog modified in place")
	}
	// Its region is a valid card region while installed.
	n := model.Node{Name: "甲", Billing: model.Billing{ResetAt: "2026-09-01T00:00:00Z", TrafficMode: "sum"}, Display: model.NodeDisplay{LatencyRegion: "公共 DNS"}}
	if _, _, err := svc.CreateNode(ctx, n); err != nil {
		t.Fatalf("plugin region rejected: %v", err)
	}
	if err := svc.RemovePlugin(ctx, "public-dns"); err != nil {
		t.Fatal(err)
	}
	if c, _ := svc.Catalog(); len(c.Targets) != len(base.Targets) {
		t.Fatal("removed target set still in the catalog")
	}
	if _, _, err := svc.CreateNode(ctx, n); !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("region of a removed set: %v", err)
	}
	_ = store
}
