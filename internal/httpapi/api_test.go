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
	"time"

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
