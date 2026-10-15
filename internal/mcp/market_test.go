package mcp

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leeska/TZA-Probe/internal/market"
	"github.com/leeska/TZA-Probe/internal/market/markettest"
	"github.com/leeska/TZA-Probe/internal/theme"
)

// 市场工具与面板「市场」页同一条路：搜索标出已装与更新，安装走同一套验签。
func TestMarketTools(t *testing.T) {
	// Without a configured CC both tools answer a readable tool error.
	bare := newEnv(t)
	for _, name := range []string{"search_market", "install_market_package"} {
		if result := invoke(t, bare, name, `{"id":"matcha"}`); result["isError"] != true || !strings.Contains(text(t, result), "市场已关闭") {
			t.Fatalf("%s without CC: %v", name, result)
		}
	}

	cc := markettest.New(t)
	cc.Add(&markettest.Package{ID: "matcha", Type: "skin", Name: "抹茶", Releases: []markettest.Release{
		{Version: "1.1.0", Archive: markettest.Skin(t, "matcha", "1.1.0")},
	}})
	dir := t.TempDir()
	themes := &theme.Store{Dir: filepath.Join(dir, "themes")}
	client, err := market.New(market.Config{URL: cc.URL(), Keys: cc.Keys(), Dir: dir, Probe: "1.1.0", Themes: themes})
	if err != nil {
		t.Fatal(err)
	}
	env := newEnv(t)
	server := httptest.NewServer(&Server{Source: env.source, Themes: themes, Market: client, Version: "1.0.0-test"})
	t.Cleanup(server.Close)
	env.server = server

	found := structured(t, invoke(t, env, "search_market", `{"type":"skin","q":"mat"}`))
	if found["count"].(float64) != 1 || found["items"].([]any)[0].(map[string]any)["id"] != "matcha" {
		t.Fatalf("search: %v", found)
	}
	if _, out := env.modern(t, "1", "tools/call", "search_market", `{"type":"script"}`); codeOf(t, out) != codeInvalidParams {
		t.Fatalf("bad type: %v", out)
	}
	detail := structured(t, invoke(t, env, "search_market", `{"id":"matcha"}`))["package"].(map[string]any)
	if versions := detail["versions"].([]any); len(versions) != 1 || versions[0].(map[string]any)["compatible"] != true {
		t.Fatalf("detail: %v", detail)
	}
	if result := invoke(t, env, "search_market", `{"id":"nope"}`); result["isError"] != true {
		t.Fatalf("missing package: %v", result)
	}

	installed := structured(t, invoke(t, env, "install_market_package", `{"id":"matcha"}`))
	if installed["version"] != "1.1.0" || installed["source"] != "market" {
		t.Fatalf("install: %v", installed)
	}
	if !themes.Known("matcha") {
		t.Fatal("package not installed")
	}
	if _, out := env.modern(t, "1", "tools/call", "install_market_package", `{}`); codeOf(t, out) != codeInvalidParams {
		t.Fatalf("missing id: %v", out)
	}
	if _, out := env.modern(t, "1", "tools/call", "install_market_package", `{"id":"matcha","version":"latest"}`); codeOf(t, out) != codeInvalidParams {
		t.Fatalf("bad version: %v", out)
	}
	// A tampered archive is a tool error, and the installed package stays.
	cc.Lock()
	cc.Tamper = true
	cc.Unlock()
	if result := invoke(t, env, "install_market_package", `{"id":"matcha","version":"1.1.0"}`); result["isError"] != true || !strings.Contains(text(t, result), "SHA-256") {
		t.Fatalf("tampered: %v", result)
	}
	if !themes.Known("matcha") {
		t.Fatal("a rejected update removed the installed package")
	}
	cc.Lock()
	cc.Updates = []map[string]string{{"id": "matcha", "version": "1.1.0", "latest": "1.2.0"}}
	cc.Unlock()
	if up := structured(t, invoke(t, env, "search_market", `{"updates":true}`)); up["count"].(float64) != 1 {
		t.Fatalf("updates: %v", up)
	}
}
