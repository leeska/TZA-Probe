package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/leeska/TZA-Probe/internal/market/markettest"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/notify"
	"github.com/leeska/TZA-Probe/internal/plugin"
	"github.com/leeska/TZA-Probe/internal/storage"
	"github.com/leeska/TZA-Probe/web"
)

// 插件从上传到当通知渠道：先预览权限、再装；配置出口选它（密钥不回显）；发送测试真的打到接收端；
// 正在用的删不掉；目标集并进任务目录。
func TestPluginRoutesAndChannel(t *testing.T) {
	dir := t.TempDir()
	s, err := storage.Open(filepath.Join(dir, "plugins.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	plugins := &plugin.Store{Dir: filepath.Join(dir, "plugins")}
	token := model.Secret()
	handler := (&API{Store: s, AdminToken: token, Assets: web.Handler(), Plugins: plugins, Notifier: notify.Transport{Plugins: plugins}}).Handler()
	call := func(method, path, ctype string, body []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://probe.example"+path, bytes.NewReader(body))
		if ctype != "" {
			r.Header.Set("Content-Type", ctype)
		}
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	jsonCall := func(method, path string, v any) *httptest.ResponseRecorder {
		data, _ := json.Marshal(v)
		return call(method, path, "application/json", data)
	}

	// The receiver: a self-hosted ntfy stand-in.
	var mu sync.Mutex
	var received []map[string]any
	var auth string
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v map[string]any
		json.NewDecoder(r.Body).Decode(&v)
		mu.Lock()
		received, auth = append(received, v), r.Header.Get("Authorization")
		mu.Unlock()
		w.Write([]byte(`{"id":"1"}`))
	}))
	defer receiver.Close()

	ntfy := markettest.PluginZip(t, "ntfy", "")
	w := call("POST", "/api/v1/admin/plugins/inspect", "application/zip", ntfy)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "permissions") || !strings.Contains(w.Body.String(), "你在设置里填写的地址") {
		t.Fatalf("inspect: %d %s", w.Code, w.Body)
	}
	if list := call("GET", "/api/v1/admin/plugins", "", nil); list.Body.String() != "[]\n" {
		t.Fatalf("inspect must not install: %s", list.Body)
	}
	if w := call("POST", "/api/v1/admin/plugins", "application/zip", ntfy); w.Code != 200 {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}
	if w := call("POST", "/api/v1/admin/plugins", "application/zip", markettest.Skin(t, "matcha", "1.0.0")); w.Code != 400 {
		t.Fatalf("theme as plugin: %d", w.Code)
	}
	if w := call("POST", "/api/v1/admin/plugins", "application/zip", markettest.PluginZip(t, "public-dns", "")); w.Code != 200 {
		t.Fatalf("upload targets: %d %s", w.Code, w.Body)
	}
	list := call("GET", "/api/v1/admin/plugins", "", nil).Body.String()
	if !strings.Contains(list, `"kind":"notify"`) || !strings.Contains(list, `"kind":"targets"`) || !strings.Contains(list, `"key":"topic"`) {
		t.Fatalf("list: %s", list)
	}

	// Channel: ntfy pointed at the receiver.
	if w := jsonCall("PUT", "/api/v1/admin/notification", map[string]any{"channel": "plugin", "plugin": "ntfy", "plugin_settings": map[string]string{"server": receiver.URL, "topic": "probe", "token": "tk_secret"}}); w.Code != 204 {
		t.Fatalf("save channel: %d %s", w.Code, w.Body)
	}
	view := call("GET", "/api/v1/admin/notification", "", nil).Body.String()
	if strings.Contains(view, "tk_secret") || !strings.Contains(view, `"plugin":"ntfy"`) || !strings.Contains(view, `"token":true`) || !strings.Contains(view, `"topic":"probe"`) {
		t.Fatalf("public view: %s", view)
	}
	// Resubmitted without the token (the form never echoes it): kept.
	if w := jsonCall("PUT", "/api/v1/admin/notification", map[string]any{"channel": "plugin", "plugin": "ntfy", "plugin_settings": map[string]string{"server": receiver.URL, "topic": "probe"}}); w.Code != 204 {
		t.Fatalf("resave: %d %s", w.Code, w.Body)
	}
	if w := call("POST", "/api/v1/admin/notification/test", "", nil); w.Code != 204 {
		t.Fatalf("test notification: %d %s", w.Code, w.Body)
	}
	mu.Lock()
	if len(received) != 1 || received[0]["topic"] != "probe" || received[0]["title"] != "TZA Probe · 连接测试" || auth != "Bearer tk_secret" {
		t.Fatalf("receiver got %v (auth %q)", received, auth)
	}
	mu.Unlock()

	if w := call("DELETE", "/api/v1/admin/plugins/ntfy", "", nil); w.Code != 409 || !strings.Contains(w.Body.String(), "配置出口") {
		t.Fatalf("delete in use: %d %s", w.Code, w.Body)
	}

	// Catalog: the target set is merged and labelled.
	catalog := call("GET", "/api/v1/admin/catalog", "", nil).Body.String()
	if !strings.Contains(catalog, `"plugin":"public-dns"`) || !strings.Contains(catalog, `"id":"plugin:public-dns:`) || !strings.Contains(catalog, `"snapshot_date"`) {
		t.Fatalf("catalog: %.300s", catalog)
	}
	if w := call("DELETE", "/api/v1/admin/plugins/public-dns", "", nil); w.Code != 204 {
		t.Fatalf("delete targets: %d", w.Code)
	}
	if strings.Contains(call("GET", "/api/v1/admin/catalog", "", nil).Body.String(), "public-dns") {
		t.Fatal("removed set still in the catalog")
	}
	if w := call("DELETE", "/api/v1/admin/plugins/public-dns", "", nil); w.Code != 404 {
		t.Fatalf("delete twice: %d", w.Code)
	}
}
