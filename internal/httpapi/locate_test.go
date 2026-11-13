package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leeska/TZA-Probe/internal/ipguard"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/storage"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// 节点所在地区的自动检测：记下 Agent 连进来的公网地址（经本机反代时取 X-Forwarded-For），
// 后台按它查定位；还没收到公网连接时 409；只给管理员。
func TestLocateNode(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "locate.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	node, token, err := store.SaveNode(context.Background(), model.Node{Name: "exit", Billing: model.Billing{ResetAt: "2026-11-01T00:00:00Z", TrafficMode: "sum"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	locator := ipguard.NewLocator()
	asked := ""
	locator.Client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		asked = r.URL.Path
		body := `{"success":true,"ip":"8.8.8.8","country_code":"US","country":"美国","city":"山景城","latitude":37.4,"longitude":-122.1,"timezone":{"id":"America/Los_Angeles"}}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	admin := model.Secret()
	handler := (&API{Store: store, AdminToken: admin, Locator: locator, Assets: http.NotFoundHandler()}).Handler()
	call := func(method, path, key, forwarded string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "http://localhost/api/v1"+path, bytes.NewReader(raw))
		r.RemoteAddr = "127.0.0.1:5555"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+key)
		if forwarded != "" {
			r.Header.Set("X-Forwarded-For", forwarded)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	path := "/admin/nodes/" + node + "/locate"
	if w := call("GET", path, admin, "", nil); w.Code != 409 {
		t.Fatalf("还没收到连接：%d %s", w.Code, w.Body)
	}
	call("POST", "/agent/heartbeat", token, "10.0.0.5", map[string]any{})
	if w := call("GET", path, admin, "", nil); w.Code != 409 {
		t.Fatalf("内网地址不算公网连接：%d", w.Code)
	}
	call("POST", "/agent/heartbeat", token, "8.8.8.8", map[string]any{})
	if w := call("GET", path, token, "", nil); w.Code != 401 {
		t.Fatalf("Agent 不能调后台接口：%d", w.Code)
	}
	if w := call("GET", "/admin/nodes/"+node+"/address", admin, "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"ip":"8.8.8.8"`) {
		t.Fatalf("节点地址：%d %s", w.Code, w.Body)
	}
	w := call("GET", path, admin, "", nil)
	var place ipguard.IPPlace
	if err := json.Unmarshal(w.Body.Bytes(), &place); err != nil || w.Code != 200 || place.Region != "美国" || place.IP != "8.8.8.8" || asked != "/8.8.8.8" {
		t.Fatalf("定位：%d %s（查的是 %s）", w.Code, w.Body, asked)
	}
}

func TestPublicClientIP(t *testing.T) {
	for _, c := range []struct{ remote, forwarded, want string }{
		{"8.8.8.8:1234", "", "8.8.8.8"},
		{"8.8.8.8:1234", "1.1.1.1", "8.8.8.8"}, // 直连时不信 X-Forwarded-For
		{"127.0.0.1:1", "9.9.9.9, 1.1.1.1", "1.1.1.1"},
		{"127.0.0.1:1", "", ""},
		{"[::1]:1", "2606:4700:4700::1111", "2606:4700:4700::1111"},
		{"127.0.0.1:1", "192.168.1.2", ""},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.forwarded != "" {
			r.Header.Set("X-Forwarded-For", c.forwarded)
		}
		if got := publicClientIP(r); got != c.want {
			t.Errorf("%s / %q → %q, want %q", c.remote, c.forwarded, got, c.want)
		}
	}
}
