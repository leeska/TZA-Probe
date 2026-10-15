package plugin

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leeska/TZA-Probe/internal/market/markettest"
	"github.com/leeska/TZA-Probe/pkg/tzapkg"
)

func TestInstallListRemove(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if list := s.List(); len(list) != 0 {
		t.Fatalf("empty store: %v", list)
	}
	p, err := s.Install(markettest.PluginZip(t, "ntfy", ""))
	if err != nil || p.ID != "ntfy" || p.Kind != tzapkg.KindNotify || p.Source != "" {
		t.Fatalf("install: %+v %v", p, err)
	}
	if _, err := s.Install(markettest.PluginZip(t, "public-dns", "")); err != nil {
		t.Fatal(err)
	}
	list := s.List()
	if len(list) != 2 || list[0].ID != "ntfy" || list[1].Kind != tzapkg.KindTargets || len(list[1].Targets) == 0 {
		t.Fatalf("list: %+v", list)
	}
	// What gets stored is the plugin.json itself, and it parses back with its templates.
	got, err := s.Plugin("ntfy")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := got.CheckSettings(map[string]string{"topic": "probe-alerts"}); err != nil {
		t.Fatalf("stored plugin lost its settings: %v", err)
	}

	// Market install records the origin; a local upload over it forgets it.
	if _, err := s.InstallMarket(markettest.PluginZip(t, "ntfy", "1.1.0"), Origin{}); err == nil {
		t.Fatal("a market install needs its release")
	}
	p, err = s.InstallMarket(markettest.PluginZip(t, "ntfy", "1.1.0"), Origin{Version: "1.1.0", SHA256: strings.Repeat("a", 64)})
	if err != nil || p.Source != tzapkg.SourceMarket || p.Version != "1.1.0" {
		t.Fatalf("market install: %+v %v", p, err)
	}
	if o, ok := s.Origin("ntfy"); !ok || o.Version != "1.1.0" {
		t.Fatalf("origin: %+v", o)
	}
	if p, _ := s.Install(markettest.PluginZip(t, "ntfy", "")); p.Source != "" {
		t.Fatal("local upload kept the market origin")
	}

	// Rejects: a theme, extra files, an unknown field.
	for name, archive := range map[string][]byte{
		"theme":         markettest.Skin(t, "matcha", "1.0.0"),
		"extra file":    markettest.Zip(t, map[string]string{"plugin.json": string(markettest.PluginJSON(t, "gotify")), "x.css": "a{}"}),
		"unknown field": markettest.Zip(t, map[string]string{"plugin.json": strings.Replace(string(markettest.PluginJSON(t, "gotify")), `"hint"`, `"script":"x","hint"`, 1)}),
	} {
		if _, err := s.Install(archive); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}

	// A plugin.json that no longer parses is an error that says which plugin.
	os.WriteFile(filepath.Join(s.Dir, "ntfy", "plugin.json"), []byte(`{"id":"ntfy"}`), 0o644)
	if _, err := s.Plugin("ntfy"); err == nil || !strings.Contains(err.Error(), "ntfy") || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("broken plugin: %v", err)
	}
	if _, err := s.Plugin("nope"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing plugin: %v", err)
	}
	if err := s.Remove("ntfy"); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove("ntfy"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("second remove: %v", err)
	}
	var nilStore *Store
	if nilStore.List() != nil || nilStore.Remove("x") == nil {
		t.Fatal("nil store")
	}
}
