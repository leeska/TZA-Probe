package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/market"
	"github.com/leeska/TZA-Probe/internal/market/markettest"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/storage"
	"github.com/leeska/TZA-Probe/internal/theme"
	"github.com/leeska/TZA-Probe/web"
)

type marketEnv struct {
	t       *testing.T
	handler http.Handler
	token   string
	themes  *theme.Store
	dir     string
}

func newMarketEnv(t *testing.T, client func(dir string, themes *theme.Store) *market.Client) *marketEnv {
	t.Helper()
	dir := t.TempDir()
	s, err := storage.Open(filepath.Join(dir, "market.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	themes := &theme.Store{Dir: filepath.Join(dir, "themes")}
	token := model.Secret()
	api := &API{Store: s, AdminToken: token, Assets: web.Handler(), Themes: themes, Market: client(dir, themes)}
	return &marketEnv{t: t, handler: api.Handler(), token: token, themes: themes, dir: dir}
}

func (e *marketEnv) do(method, path string, body any, auth bool) *httptest.ResponseRecorder {
	e.t.Helper()
	var reader *bytes.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}
	r := httptest.NewRequest(method, "https://probe.example"+path, reader)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if auth {
		r.Header.Set("Authorization", "Bearer "+e.token)
	}
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, r)
	return w
}

func (e *marketEnv) json(w *httptest.ResponseRecorder) map[string]any {
	e.t.Helper()
	out := map[string]any{}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		e.t.Fatalf("not JSON (%d): %s", w.Code, w.Body.String())
	}
	return out
}

// 没配置 --cc-url 时状态接口照常回答（configured=false），其余市场接口 503 并说清怎么配。
func TestMarketRoutesWithoutCC(t *testing.T) {
	e := newMarketEnv(t, func(string, *theme.Store) *market.Client { return nil })
	if w := e.do("GET", "/api/v1/admin/market/status", nil, false); w.Code != 401 {
		t.Fatalf("anonymous status: %d", w.Code)
	}
	w := e.do("GET", "/api/v1/admin/market/status", nil, true)
	if w.Code != 200 || e.json(w)["configured"] != false {
		t.Fatalf("status: %d %s", w.Code, w.Body)
	}
	for _, path := range []string{"/api/v1/admin/market/catalog", "/api/v1/admin/market/packages/matcha", "/api/v1/admin/market/updates", "/api/v1/admin/market/media?url=x"} {
		if w := e.do("GET", path, nil, true); w.Code != 503 || !strings.Contains(w.Body.String(), "市场已关闭") {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
	}
	if w := e.do("POST", "/api/v1/admin/market/install", map[string]string{"id": "matcha"}, true); w.Code != 503 {
		t.Fatalf("install: %d", w.Code)
	}
}

func TestMarketRoutes(t *testing.T) {
	cc := markettest.New(t)
	cc.Add(&markettest.Package{ID: "matcha", Type: "skin", Name: "抹茶", Releases: []markettest.Release{
		{Version: "1.1.0", Archive: markettest.Skin(t, "matcha", "1.1.0")},
	}})
	clock := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	e := newMarketEnv(t, func(dir string, themes *theme.Store) *market.Client {
		c, err := market.New(market.Config{URL: cc.URL(), Keys: cc.Keys(), Dir: dir, Probe: model.Version, Themes: themes, Now: func() time.Time { return clock }})
		if err != nil {
			t.Fatal(err)
		}
		return c
	})

	if w := e.do("GET", "/api/v1/admin/market/catalog", nil, false); w.Code != 401 {
		t.Fatalf("anonymous catalog: %d", w.Code)
	}
	w := e.do("GET", "/api/v1/admin/market/catalog?type=skin&q=mat", nil, true)
	if w.Code != 200 {
		t.Fatalf("catalog: %d %s", w.Code, w.Body)
	}
	items := e.json(w)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != "matcha" {
		t.Fatalf("catalog items: %v", items)
	}
	cover := items[0].(map[string]any)["cover"].(string)
	if w := e.do("GET", "/api/v1/admin/market/catalog?page=x", nil, true); w.Code != 400 {
		t.Fatalf("bad page: %d", w.Code)
	}
	if w := e.do("GET", "/api/v1/admin/market/packages/matcha", nil, true); w.Code != 200 || !strings.Contains(w.Body.String(), `"compatible":true`) {
		t.Fatalf("detail: %d %s", w.Code, w.Body)
	}
	if w := e.do("GET", "/api/v1/admin/market/packages/nope", nil, true); w.Code != 404 {
		t.Fatalf("missing package: %d", w.Code)
	}

	// Install, then the theme list marks it as a market install.
	if w := e.do("POST", "/api/v1/admin/market/install", map[string]string{"id": "matcha", "version": "x"}, true); w.Code != 400 {
		t.Fatalf("bad version: %d %s", w.Code, w.Body)
	}
	w = e.do("POST", "/api/v1/admin/market/install", map[string]string{"id": "matcha"}, true)
	if w.Code != 200 || e.json(w)["source"] != "market" {
		t.Fatalf("install: %d %s", w.Code, w.Body)
	}
	if w := e.do("GET", "/api/v1/admin/themes", nil, true); !strings.Contains(w.Body.String(), `"source":"market"`) {
		t.Fatalf("themes list: %s", w.Body)
	}
	cc.Lock()
	cc.Tamper = true
	cc.Unlock()
	if w := e.do("POST", "/api/v1/admin/market/install", map[string]string{"id": "matcha"}, true); w.Code != 502 || !strings.Contains(w.Body.String(), "SHA-256") {
		t.Fatalf("tampered install: %d %s", w.Code, w.Body)
	}
	cc.Lock()
	cc.Tamper = false
	cc.Updates = []map[string]string{{"id": "matcha", "version": "1.1.0", "latest": "1.2.0"}}
	cc.Unlock()
	if w := e.do("GET", "/api/v1/admin/market/updates", nil, true); w.Code != 200 || !strings.Contains(w.Body.String(), `"latest":"1.2.0"`) {
		t.Fatalf("updates: %d %s", w.Code, w.Body)
	}

	// Media proxy: only CC's origin, only images, served as the image itself.
	w = e.do("GET", "/api/v1/admin/market/media?url="+url.QueryEscape(cover), nil, true)
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || w.Body.String() != string(markettest.PNG) || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("media: %d %q %q", w.Code, w.Header().Get("Content-Type"), w.Body)
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "img-src 'self' data:") {
		t.Fatalf("CSP changed: %q", csp)
	}
	for path, want := range map[string]int{
		"https://elsewhere.example/a.png":                              400,
		"http://169.254.169.254/latest/meta":                           400,
		strings.Replace(cc.URL(), "http", "https", 1) + "/media/a.png": 400,
		cc.URL() + "/media/evil.svg":                                   502,
		cc.URL() + "/media/big.png":                                    502,
	} {
		if w := e.do("GET", "/api/v1/admin/market/media?url="+url.QueryEscape(path), nil, true); w.Code != want {
			t.Fatalf("media %s: %d %s", path, w.Code, w.Body)
		}
	}
	if w := e.do("GET", "/api/v1/admin/market/media?url="+url.QueryEscape(cover), nil, false); w.Code != 401 {
		t.Fatalf("anonymous media: %d", w.Code)
	}

	// Binding: start, poll (pending, then bound), status, disconnect.
	w = e.do("POST", "/api/v1/admin/market/connect", nil, true)
	if w.Code != 200 || e.json(w)["user_code"] != "ABCD-EFGH" || strings.Contains(w.Body.String(), "dev-code") {
		t.Fatalf("connect: %d %s", w.Code, w.Body)
	}
	if w := e.do("POST", "/api/v1/admin/market/connect", map[string]string{"name": "家里"}, true); w.Code != 200 {
		t.Fatalf("connect with a name: %d %s", w.Code, w.Body)
	}
	if w := e.do("POST", "/api/v1/admin/market/connect/poll", nil, true); e.json(w)["status"] != "pending" {
		t.Fatalf("early poll: %s", w.Body)
	}
	cc.Lock()
	cc.Device = "approved"
	cc.Unlock()
	clock = clock.Add(10 * time.Second)
	if w := e.do("POST", "/api/v1/admin/market/connect/poll", nil, true); e.json(w)["status"] != "bound" {
		t.Fatalf("poll: %s", w.Body)
	}
	status := e.json(e.do("GET", "/api/v1/admin/market/status", nil, true))
	if status["bound"] != true || status["account"] != "alice" || strings.Contains(status["url"].(string), "token") {
		t.Fatalf("status: %v", status)
	}
	if raw, _ := json.Marshal(status); strings.Contains(string(raw), markettest.Token) {
		t.Fatal("status leaked the instance token")
	}
	if info, err := os.Stat(filepath.Join(e.dir, market.InstanceFile)); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("credentials: %v %v", info, err)
	}
	if w := e.do("DELETE", "/api/v1/admin/market/connect", nil, true); w.Code != 204 {
		t.Fatalf("disconnect: %d %s", w.Code, w.Body)
	}
	if e.json(e.do("GET", "/api/v1/admin/market/status", nil, true))["bound"] != false {
		t.Fatal("still bound after disconnect")
	}

	// A local upload over the market package turns it back into 本地.
	w = httptest.NewRecorder()
	r := httptest.NewRequest("POST", "https://probe.example/api/v1/admin/themes", bytes.NewReader(markettest.Skin(t, "matcha", "2.0.0")))
	r.Header.Set("Authorization", "Bearer "+e.token)
	e.handler.ServeHTTP(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), `"source"`) {
		t.Fatalf("local upload: %d %s", w.Code, w.Body)
	}
}
