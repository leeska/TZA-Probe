package mcp

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leeska/TZA-Probe/internal/pkgtest"
	"github.com/leeska/TZA-Probe/internal/plugin"
	"github.com/leeska/TZA-Probe/internal/theme"
)

// MCP 覆盖面板的插件操作：列出已装插件、把通知插件设成渠道（密钥不回显）、
// 目标集出现在 list_catalog 里。
func TestPluginTools(t *testing.T) {
	dir := t.TempDir()
	themes := &theme.Store{Dir: filepath.Join(dir, "themes")}
	plugins := &plugin.Store{Dir: filepath.Join(dir, "plugins")}
	for _, id := range []string{"ntfy", "public-dns"} {
		if _, err := plugins.Install(pkgtest.PluginZip(t, id, "")); err != nil {
			t.Fatal(err)
		}
	}
	env := newEnv(t)
	server := httptest.NewServer(&Server{Source: env.source, Notifier: env.sender, Themes: themes, Plugins: plugins, Version: "1.0.0-test"})
	t.Cleanup(server.Close)
	env.server = server

	listed := structured(t, invoke(t, env, "list_plugins", `{}`))
	if listed["count"].(float64) != 2 {
		t.Fatalf("list_plugins: %v", listed)
	}

	if _, out := env.modern(t, "1", "tools/call", "update_settings", `{"channel":"plugin","plugin":"ntfy","plugin_settings":{"topic":"bad topic!"}}`); codeOf(t, out) != codeInvalidParams {
		t.Fatalf("invalid plugin setting: %v", out)
	}
	set := structured(t, invoke(t, env, "update_settings", `{"channel":"plugin","plugin":"ntfy","plugin_settings":{"topic":"probe","token":"tk_secret"}}`))
	raw, _ := json.Marshal(set)
	if set["channel"] != "plugin" || set["plugin"] != "ntfy" || strings.Contains(string(raw), "tk_secret") || !strings.Contains(string(raw), `"token":true`) {
		t.Fatalf("update_settings: %s", raw)
	}
	// Only the topic changes; the token stays.
	invoke(t, env, "update_settings", `{"plugin_settings":{"topic":"probe2"}}`)
	if s := env.source.settings["notification"]; !strings.Contains(s, "tk_secret") || !strings.Contains(s, "probe2") {
		t.Fatalf("stored: %s", s)
	}
	got := structured(t, invoke(t, env, "get_settings", `{}`))
	if got["plugin_name"] != "ntfy" || got["plugin_settings"].(map[string]any)["topic"] != "probe2" {
		t.Fatalf("get_settings: %v", got)
	}

	catalog := structured(t, invoke(t, env, "list_catalog", `{"region":"公共 DNS"}`))
	if catalog["count"].(float64) == 0 || catalog["targets"].([]any)[0].(map[string]any)["plugin"] != "public-dns" {
		t.Fatalf("list_catalog: %v", catalog["count"])
	}
}
