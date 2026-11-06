package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPluginPreparationInvalidatesStaleReadiness(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "plugins-prepared")
	ready, restart, err := pluginReadiness(marker)
	if err != nil || ready || !restart {
		t.Fatalf("first startup: ready=%v restart=%v err=%v", ready, restart, err)
	}
	for _, previous := range []string{"ready", "restart", "corrupt"} {
		if err := writePrivate(marker, []byte(previous)); err != nil {
			t.Fatal(err)
		}
		if err := beginPluginPreparation(marker); err != nil {
			t.Fatal(err)
		}
		ready, restart, err := pluginReadiness(marker)
		if err != nil || ready || restart {
			t.Fatalf("failed preparation after %s must stay inactive without a restart loop: %v %v %v", previous, ready, restart, err)
		}
	}
	if err := writePrivate(marker, []byte("restart")); err != nil {
		t.Fatal(err)
	}
	ready, restart, err = pluginReadiness(marker)
	if err != nil || ready || !restart {
		t.Fatal("changed sandbox did not request restart", err)
	}
	ready, restart, err = pluginReadiness(marker)
	if err != nil || !ready || restart {
		t.Fatal("restart did not settle", err)
	}
	if err := writePrivate(marker, []byte("corrupt")); err != nil {
		t.Fatal(err)
	}
	if ready, restart, err := pluginReadiness(marker); err == nil || ready || restart {
		t.Fatal("corrupt readiness state accepted")
	}
}

func TestPluginOptOutAndConfigPreservation(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "agent.json")
	if err := writePrivate(path, []byte(`{"endpoint":"https://probe.example.com","token":"secret","interfaces":["eth0"],"plugins_disabled":true}`)); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil || !pluginsDisabled(path, cfg) || len(cfg.Interfaces) != 1 {
		t.Fatalf("config: %+v %v", cfg, err)
	}
	cfg.PluginsDisabled = false
	if pluginsDisabled(path, cfg) {
		t.Fatal("plugins must default to allowed")
	}
	if err := writePrivate(filepath.Join(directory, "plugins.disabled"), nil); err != nil {
		t.Fatal(err)
	}
	if !pluginsDisabled(path, cfg) {
		t.Fatal("local opt-out ignored")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("configuration is not private")
	}
	if ready, restart, err := integratedPluginsReady(path); ready || restart || err != nil {
		t.Fatal("custom installation attempted system service migration")
	}
}
