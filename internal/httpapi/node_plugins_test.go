package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leeska/TZA-Probe/internal/market"
	"github.com/leeska/TZA-Probe/internal/market/markettest"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/plugin"
	"github.com/leeska/TZA-Probe/internal/storage"
	"github.com/leeska/TZA-Probe/pkg/tzapkg"
	"github.com/leeska/TZA-Probe/web"
)

func officialManagerArchive(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "pkg", "tzapkg", "testdata", "runtime", "executor.json"))
	if err != nil {
		t.Fatal(err)
	}
	return markettest.Zip(t, map[string]string{"executor.json": string(raw)})
}

func TestCentralManagerInstallationAndRemoval(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cc := markettest.New(t)
	cc.Add(&markettest.Package{ID: tzapkg.OfficialExecutorID, Type: tzapkg.TypeExecutor, Kind: "ipsentinel", Name: "IPSentinel", Releases: []markettest.Release{{Version: "0.4.0", Archive: officialManagerArchive(t)}}})
	client, err := market.New(market.Config{URL: cc.URL(), Keys: cc.Keys(), Dir: t.TempDir(), Probe: "1.12.0"})
	if err != nil {
		t.Fatal(err)
	}
	admin := model.Secret()
	handler := (&API{Store: store, AdminToken: admin, Market: client, Plugins: &plugin.Store{Dir: filepath.Join(t.TempDir(), "plugins")}, Assets: web.Handler()}).Handler()
	call := func(method, path, key, origin string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		request := httptest.NewRequest(method, "https://probe.example/api/v1/admin/"+path, bytes.NewReader(raw))
		request.Header.Set("Content-Type", "application/json")
		if key != "" {
			request.Header.Set("Authorization", "Bearer "+key)
		}
		if origin != "" {
			request.Header.Set("Origin", origin)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	body := map[string]string{"id": tzapkg.OfficialExecutorID, "version": "0.4.0"}
	if response := call("POST", "market/install", "", "", body); response.Code != 401 {
		t.Fatal("anonymous install", response.Code)
	}
	if response := call("POST", "market/install", admin, "https://attacker.example", body); response.Code != 403 {
		t.Fatal("cross-origin install", response.Code)
	}
	if response := call("POST", "market/install", admin, "", body); response.Code != 200 {
		t.Fatal(response.Code, response.Body)
	}
	if response := call("GET", "plugins", admin, "", nil); !strings.Contains(response.Body.String(), `"kind":"agent"`) {
		t.Fatal("signed manager missing", response.Body)
	}
	for _, path := range []string{"runtime", "executors", "market/node-plans"} {
		if response := call("GET", path, admin, "", nil); response.Code != 404 {
			t.Fatal("retired route exposed", path, response.Code)
		}
	}
	if response := call("DELETE", "plugins/"+tzapkg.OfficialExecutorID, admin, "", nil); response.Code != 204 {
		t.Fatal("remove", response.Code, response.Body)
	}
}
