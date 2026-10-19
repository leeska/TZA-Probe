package httpapi

import (
	"archive/zip"
	"bytes"
	"fmt"
	"github.com/leeska/TZA-Probe/internal/market"
	"github.com/leeska/TZA-Probe/internal/theme"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestThemeDialogScopeAndNodeBinding(t *testing.T) {
	e := newMarketEnv(t, func(string, *theme.Store) *market.Client { return nil })
	upload := func(path string, files map[string]string, auth bool) *httptest.ResponseRecorder {
		t.Helper()
		var b bytes.Buffer
		z := zip.NewWriter(&b)
		for name, data := range files {
			w, _ := z.Create(name)
			w.Write([]byte(data))
		}
		z.Close()
		r := httptest.NewRequest("POST", "https://probe.example/api/v1/admin/themes"+path, &b)
		r.Header.Set("Content-Type", "application/zip")
		if auth {
			r.Header.Set("Authorization", "Bearer "+e.token)
		}
		w := httptest.NewRecorder()
		e.handler.ServeHTTP(w, r)
		return w
	}
	skin := map[string]string{"theme.json": `{"id":"dialog","name":"Dialog","chrome":{"light":"#ffffff","dark":"#111111"},"swatch":["#ffffff","#eeeeee","#123456"],"styles":["theme.css"],"settings_ui":{"entry":"settings.html","api_version":1},"decorations":[{"id":"jp","name":"Japan","regions":["Japan"]}]}`, "theme.css": "body{}", "settings.html": "<script src='/theme-settings-sdk.js'></script>"}
	if w := upload("", skin, true); w.Code != 200 {
		t.Fatalf("skin upload: %d %s", w.Code, w.Body)
	}
	w := e.do("GET", "/themes/dialog/settings.html", nil, false)
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox allow-scripts;") || strings.Contains(w.Header().Get("Content-Security-Policy"), "allow-same-origin") {
		t.Fatalf("settings document isolation: %d %v", w.Code, w.Header())
	}
	addon := func(host string) map[string]string {
		return map[string]string{"theme.json": fmt.Sprintf(`{"id":"cast","type":"addon","for":%q,"name":"Cast","decorations":[{"id":"us","name":"US"}]}`, host)}
	}
	for _, c := range []struct {
		host string
		auth bool
		want int
	}{{"dialog", false, 401}, {"other", true, 400}, {"dialog", true, 200}} {
		if w := upload("/dialog/addons", addon(c.host), c.auth); w.Code != c.want {
			t.Fatalf("scoped upload %+v: %d %s", c, w.Code, w.Body)
		}
	}
	if w := upload("/dialog/addons", skin, true); w.Code != 400 {
		t.Fatal("settings bridge must reject skin uploads")
	}
	other := map[string]string{}
	for k, v := range skin {
		other[k] = strings.ReplaceAll(v, `"dialog"`, `"other"`)
	}
	if w := upload("", other, true); w.Code != 200 {
		t.Fatalf("other theme: %d %s", w.Code, w.Body)
	}
	conflict := addon("dialog")
	conflict["theme.json"] = strings.ReplaceAll(conflict["theme.json"], `"id":"cast"`, `"id":"other"`)
	if w := upload("/dialog/addons", conflict, true); w.Code != 400 {
		t.Fatal("scoped upload overwrote an unrelated theme")
	}
	created := e.do("POST", "/api/v1/admin/nodes", map[string]any{"name": "Preserve name", "public": true, "region": "Japan", "vendor": "Keep vendor", "billing": map[string]any{"traffic_mode": "sum", "reset_at": "2026-10-19T00:00:00Z"}}, true)
	if created.Code != 200 {
		t.Fatal(created.Body)
	}
	id := e.json(created)["id"].(string)
	path := "/api/v1/admin/themes/dialog/bindings/" + id
	if w := e.do("PUT", path, map[string]string{"decoration": "cast/us"}, false); w.Code != 401 {
		t.Fatal("anonymous binding allowed")
	}
	for _, deco := range []string{"dialog/jp", "cast/us", ""} {
		if w := e.do("PUT", path, map[string]string{"decoration": deco}, true); w.Code != 204 {
			t.Fatalf("bind %s: %d %s", deco, w.Code, w.Body)
		}
		list := e.do("GET", "/api/v1/admin/nodes", nil, true).Body.String()
		for _, want := range []string{`"name":"Preserve name"`, `"vendor":"Keep vendor"`, `"region":"Japan"`, `"public":true`} {
			if !strings.Contains(list, want) {
				t.Fatalf("binding clobbered node: %s", list)
			}
		}
		if deco != "" && !strings.Contains(list, `"decoration":"`+deco+`"`) {
			t.Fatalf("binding missing: %s", list)
		}
	}
	for _, deco := range []string{"other/us", "cast/missing", "dialog/missing"} {
		if w := e.do("PUT", path, map[string]string{"decoration": deco}, true); w.Code != 400 {
			t.Fatalf("cross-theme/unknown binding accepted: %d", w.Code)
		}
	}
}
