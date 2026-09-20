package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/agentdist"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/storage"
	"github.com/leeska/TZA-Probe/web"
)

func TestHTTPBoundariesAndAgentFlow(t *testing.T) {
	s, err := storage.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	admin := model.Secret()
	server := httptest.NewServer((&API{Store: s, AdminToken: admin, Assets: web.Handler()}).Handler())
	defer server.Close()
	request := func(method, path, token string, body any) (int, []byte) {
		t.Helper()
		var data []byte
		if body != nil {
			data, _ = json.Marshal(body)
		}
		req, _ := http.NewRequest(method, server.URL+path, bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
	if code, _ := request("GET", "/api/v1/admin/nodes", "", nil); code != 401 {
		t.Fatal(code)
	}
	for _, token := range []string{"", model.Secret()} {
		if code, _ := request("POST", "/api/v1/admin/tasks/example/control", token, model.TaskAction{Action: "run"}); code != 401 {
			t.Fatal("task control authentication", code)
		}
		if code, _ := request("GET", "/api/v1/admin/tasks/example/runs", token, nil); code != 401 {
			t.Fatal("task history authentication", code)
		}
	}
	node := model.Node{Name: "private", BillingDay: 1, TrafficMode: "sum"}
	code, data := request("POST", "/api/v1/admin/nodes", admin, node)
	if code != 200 {
		t.Fatalf("create: %d %s", code, data)
	}
	var response struct {
		Token string `json:"token"`
	}
	json.Unmarshal(data, &response)
	id, _ := s.Authenticate(context.Background(), response.Token)
	if code, data = request("GET", "/api/v1/nodes", "", nil); code != 200 || strings.Contains(string(data), "private") {
		t.Fatal("private node leaked", code, string(data))
	}
	for _, suffix := range []string{"history", "observations", "network-history"} {
		if code, _ = request("GET", "/api/v1/nodes/"+id+"/"+suffix, "", nil); code != 404 {
			t.Fatal("private detail leaked", code)
		}
	}
	if code, _ = request("GET", "/api/v1/admin/nodes", response.Token, nil); code != 401 {
		t.Fatal("agent has admin access")
	}
	report := model.Report{Protocol: 1, Metrics: model.Metrics{CPU: 10, MemoryTotal: 100, MemoryUsed: 20}}
	if code, data = request("POST", "/api/v1/agent/report", response.Token, report); code != 204 {
		t.Fatal(code, string(data))
	}
	if code, _ = request("POST", "/api/v1/agent/report", model.Secret(), report); code != 401 {
		t.Fatal("invalid agent token accepted")
	}
	if code, _ = request("GET", "/api/v1/nodes/"+id+"/history", admin, nil); code != 200 {
		t.Fatal(code)
	}
	req, _ := http.NewRequest("POST", server.URL+"/api/v1/admin/nodes/"+id+"/token", nil)
	req.Header.Set("Authorization", "Bearer "+admin)
	req.Header.Set("Origin", "https://evil.example")
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("cross-origin write accepted")
	}
	if code, _ = request("POST", "/api/v1/admin/nodes/"+id+"/token", admin, nil); code != 200 {
		t.Fatal(code)
	}
	if code, _ = request("GET", "/api/v1/agent/jobs", response.Token, nil); code != 401 {
		t.Fatal("revoked token accepted")
	}
	if code, data = request("GET", "/", "", nil); code != 200 || !strings.Contains(string(data), "app.js") {
		t.Fatal("embedded page absent")
	}
	nodes, _ := s.Nodes(context.Background(), time.Now())
	if len(nodes) != 1 || !nodes[0].Online {
		t.Fatal(nodes)
	}
}
func TestLoginCookiesAndJSONValidation(t *testing.T) {
	s, err := storage.Open(filepath.Join(t.TempDir(), "login.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	token := model.Secret()
	handler := (&API{Store: s, AdminToken: token, Assets: web.Handler()}).Handler()
	r := httptest.NewRequest("POST", "https://probe.example/api/v1/login", strings.NewReader(`{"token":"`+token+`"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://probe.example")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal(cookies)
	}
	r = httptest.NewRequest("GET", "https://probe.example/api/v1/session", nil)
	r.AddCookie(cookies[0])
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("session failed", w.Code)
	}
	r = httptest.NewRequest("POST", "https://probe.example/api/v1/login", strings.NewReader(`{"token":"`+token+`"} {}`))
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal("trailing JSON accepted", w.Code)
	}
}

func TestAgentDistributionRoutes(t *testing.T) {
	s, err := storage.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, agentdist.Name("arm64")), []byte("ELF-arm64"), 0o755); err != nil {
		t.Fatal(err)
	}
	api := &API{Store: s, AdminToken: model.Secret(), Assets: web.Handler(), Agents: &agentdist.Dist{Dir: dir}}
	server := httptest.NewServer(api.Handler())
	defer server.Close()
	get := func(path string) (int, string, http.Header) {
		t.Helper()
		resp, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body), resp.Header
	}
	// Script and downloads are public: a fresh node has no credentials yet.
	if code, body, h := get("/install.sh"); code != 200 || !strings.HasPrefix(body, "#!/bin/sh") || !strings.HasPrefix(h.Get("Content-Type"), "text/plain") {
		t.Fatalf("install.sh: %d %q %s", code, body[:min(len(body), 20)], h.Get("Content-Type"))
	}
	if code, body, h := get("/dl/tza-agent-linux-arm64"); code != 200 || body != "ELF-arm64" || h.Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("binary: %d %q %s", code, body, h.Get("Content-Type"))
	}
	if code, body, _ := get("/dl/tza-agent-linux-arm64.sha256"); code != 200 || !strings.HasSuffix(strings.TrimSpace(body), "  tza-agent-linux-arm64") || len(strings.Fields(body)[0]) != 64 {
		t.Fatalf("checksum: %d %q", code, body)
	}
	// amd64 is not in dir; it is served only if this build embeds a copy.
	if code, _, _ := get("/dl/tza-agent-linux-amd64"); code != 404 && code != 200 {
		t.Fatalf("missing arch should be 404 (or 200 when embedded), got %d", code)
	}
	for _, path := range []string{"/dl/tza-agent-linux-mips", "/dl/..%2F..%2Fetc%2Fpasswd", "/dl/README.md"} {
		if code, _, _ := get(path); code != 404 {
			t.Fatalf("%s should be 404, got %d", path, code)
		}
	}
	if code, _, _ := get("/dl/tza-agent-linux-arm64.sha256"); code != 200 {
		t.Fatalf("dir copy must win over any embedded copy, got %d", code)
	}
	// A Server without distribution configured keeps both routes closed.
	bare := httptest.NewServer((&API{Store: s, AdminToken: model.Secret(), Assets: web.Handler()}).Handler())
	defer bare.Close()
	for _, path := range []string{"/install.sh", "/dl/tza-agent-linux-arm64"} {
		resp, err := http.Get(bare.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Fatalf("%s without Agents should be 404, got %d", path, resp.StatusCode)
		}
	}
}
