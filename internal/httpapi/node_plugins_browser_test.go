package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/account"
	runtimepkg "github.com/leeska/TZA-Probe/internal/executorruntime"
	"github.com/leeska/TZA-Probe/internal/ipsentinel"
	"github.com/leeska/TZA-Probe/internal/market"
	"github.com/leeska/TZA-Probe/internal/market/markettest"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/storage"
	"github.com/leeska/TZA-Probe/web"
)

func TestNodePluginMarketBrowserFixture(t *testing.T) {
	output := os.Getenv("TZA_NODE_PLUGIN_BROWSER_QA")
	if output == "" {
		t.Skip("TZA_NODE_PLUGIN_BROWSER_QA not set")
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(filepath.Join(t.TempDir(), "qa.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ready := ""
	enrollment := runtimepkg.Enrollment{ScopeSHA256: strings.Repeat("a", 64), Target: ipsentinel.Target{ControllerID: ipsentinel.NewUUID(), NodeID: ipsentinel.NewUUID()}, Host: runtimepkg.Host{Platform: "linux-amd64", ProbeVersion: "1.10.0", AgentVersion: "1.10.0"}}
	for _, name := range []string{"QA · 可生成计划", "QA · 尚未授权安装"} {
		node, _, err := store.SaveNode(t.Context(), model.Node{Name: name, Billing: model.Billing{ResetAt: "2026-11-01T00:00:00Z", TrafficMode: "sum"}}, true)
		if err != nil {
			t.Fatal(err)
		}
		if ready == "" {
			ready = node
		}
	}
	cc := markettest.New(t)
	cc.Add(&markettest.Package{ID: "ipsentinel-runtime", Type: "executor", Kind: "ipsentinel", Name: "IPSentinel", Releases: []markettest.Release{{Version: "0.4.0", Probe: ">=1.6.0"}}})
	client, err := market.New(market.Config{URL: cc.URL(), Keys: cc.Keys(), Dir: t.TempDir(), Probe: "1.10.0"})
	if err != nil {
		t.Fatal(err)
	}
	accountDir, password := t.TempDir(), model.Secret()
	if err := account.Save(accountDir, "qa", password); err != nil {
		t.Fatal(err)
	}
	api := &API{Store: store, AdminToken: model.Secret(), AccountDir: accountDir, Market: client, Assets: web.Handler()}
	handler := api.Handler()
	server := httptest.NewServer(handler)
	defer server.Close()
	raw, _ := json.Marshal(map[string]string{"username": "qa", "password": password})
	request := httptest.NewRequest(http.MethodPost, server.URL+"/api/v1/login", bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatal("QA login failed", response.Code)
	}
	cookies := []map[string]any{}
	for _, cookie := range response.Result().Cookies() {
		cookies = append(cookies, map[string]any{"name": cookie.Name, "value": cookie.Value, "domain": "127.0.0.1", "path": cookie.Path, "expires": -1, "httpOnly": cookie.HttpOnly, "secure": false, "sameSite": "Strict"})
	}
	state, _ := json.Marshal(map[string]any{"cookies": cookies, "origins": []any{}})
	for name, data := range map[string][]byte{"browser-state.json": state, "url": []byte(server.URL)} {
		if err := os.WriteFile(filepath.Join(output, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for deadline := time.Now().Add(5 * time.Minute); time.Now().Before(deadline); {
		if _, err := store.RuntimeExchange(t.Context(), ready, runtimepkg.ChannelReport{Enrollment: enrollment}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(output, "done")); err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	t.Log("browser fixture used isolated SQLite and catalog metadata; no runtime downloaded or executed")
}
