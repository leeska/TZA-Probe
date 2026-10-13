package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/storage"
	"github.com/leeska/TZA-Probe/web"
)

func TestMCPKeyLifecycleAndIsolation(t *testing.T) {
	s, err := storage.Open(filepath.Join(t.TempDir(), "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a := &API{Store: s, AdminToken: model.Secret(), Assets: web.Handler()}
	handler := a.Handler()
	w := httptest.NewRecorder()
	a.startSession(w, httptest.NewRequest("POST", "https://probe.example/api/v1/login", nil), time.Now())
	cookie := w.Result().Cookies()[0]
	call := func(method, path, key, body, origin string, session bool) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "https://probe.example"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if session {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, method := range []string{"GET", "POST", "DELETE"} {
		if w := call(method, "/api/v1/admin/mcp-key", "", "", "", false); w.Code != 401 {
			t.Fatalf("anonymous %s: %d", method, w.Code)
		}
	}
	if w := call("GET", "/api/v1/admin/mcp-key", "", "", "", true); w.Code != 200 || !strings.Contains(w.Body.String(), `"configured":false`) {
		t.Fatal(w.Code, w.Body.String())
	}
	generate := func() string {
		t.Helper()
		w := call("POST", "/api/v1/admin/mcp-key", "", "", "", true)
		var out struct {
			APIKey string `json:"api_key"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || !strings.HasPrefix(out.APIKey, "tza_mcp_") || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(w.Code, w.Body.String())
		}
		return out.APIKey
	}
	key := generate()
	value, err := s.Setting(context.Background(), mcpKeySetting)
	if err != nil || strings.Contains(value, key) || !strings.Contains(value, model.Hash(key)) {
		t.Fatalf("key storage must contain only digest: %v", err)
	}
	if w := call("GET", "/api/v1/admin/mcp-key", "", "", "", true); w.Code != 200 || strings.Contains(w.Body.String(), key) || strings.Contains(w.Body.String(), "hash") {
		t.Fatal("secret exposed", w.Code)
	}
	message := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	check := func(key string, code int) {
		t.Helper()
		if w := call("POST", "/mcp", key, message, "", false); w.Code != code {
			t.Fatalf("MCP: want %d got %d %s", code, w.Code, w.Body.String())
		}
	}
	check(key, 200)
	check(a.AdminToken, 401)
	if w := call("POST", "/mcp", "", message, "", true); w.Code != 401 {
		t.Fatal("browser session accepted by MCP")
	}
	if w := call("POST", "/mcp", key, message, "https://evil.example", false); w.Code != 403 {
		t.Fatal("cross-origin MCP accepted")
	}
	for _, path := range []string{"/api/v1/session", "/api/v1/admin/nodes", "/api/v1/admin/mcp-key"} {
		if w := call("GET", path, key, "", "", false); w.Code != 401 {
			t.Fatalf("MCP key accepted by %s: %d", path, w.Code)
		}
	}
	if w := call("POST", "/api/v1/login", "", `{"token":"`+key+`"}`, "", false); w.Code != 400 || len(w.Result().Cookies()) != 0 {
		t.Fatal("MCP key created browser session")
	}
	if w := call("POST", "/api/v1/admin/mcp-key", "", "", "https://evil.example", true); w.Code != 403 {
		t.Fatal("cross-origin rotation accepted")
	}
	check(key, 200)
	old := key
	key = generate()
	check(old, 401)
	check(key, 200)
	// New handler simulates a restart: the stored key survives, browser sessions do not.
	handler = a.Handler()
	check(key, 200)
	w = httptest.NewRecorder()
	a.startSession(w, httptest.NewRequest("POST", "https://probe.example/api/v1/login", nil), time.Now())
	cookie = w.Result().Cookies()[0]
	if w := call("DELETE", "/api/v1/admin/mcp-key", "", "", "", true); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	check(key, 401)
	if w := call("GET", "/api/v1/admin/mcp-key", "", "", "", true); !strings.Contains(w.Body.String(), `"configured":false`) {
		t.Fatal(w.Body.String())
	}
}
