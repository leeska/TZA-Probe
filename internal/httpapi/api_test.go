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

// The MCP endpoint is part of the administrator surface: the same key, sent as a
// bearer token, and the same cross-origin rule as the admin API.
func TestMCPEndpointUsesTheAdministratorKey(t *testing.T) {
	s, err := storage.Open(filepath.Join(t.TempDir(), "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	admin := model.Secret()
	server := httptest.NewServer((&API{Store: s, AdminToken: admin, Assets: web.Handler()}).Handler())
	defer server.Close()
	message := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`
	call := func(token, origin string) (int, []byte) {
		t.Helper()
		req, _ := http.NewRequest("POST", server.URL+"/mcp", strings.NewReader(message))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("MCP-Protocol-Version", "2026-07-28")
		req.Header.Set("Mcp-Method", "tools/list")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, body
	}
	if code, _ := call("", ""); code != 401 {
		t.Fatalf("anonymous MCP request: %d", code)
	}
	if code, _ := call(model.Secret(), ""); code != 401 {
		t.Fatalf("non-administrator key: %d", code)
	}
	code, body := call(admin, "")
	if code != 200 || !strings.Contains(string(body), "list_nodes") {
		t.Fatalf("administrator MCP request: %d %s", code, body)
	}
	if code, _ := call(admin, "https://evil.example"); code != 403 {
		t.Fatalf("cross-origin MCP request: %d", code)
	}
	resp, err := server.Client().Get(server.URL + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 405 || resp.Header.Get("Allow") != "POST" {
		t.Fatalf("GET /mcp: %d %v", resp.StatusCode, resp.Header)
	}
}

// 公开卡片只画回程标签，跳表占了观测载荷的九成：slim=1 必须把跳表去掉，
// 而详情页用的默认形态仍然带着它。
func TestObservationsSlimDropsHopTables(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(filepath.Join(t.TempDir(), "slim.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	admin := model.Secret()
	server := httptest.NewServer((&API{Store: s, AdminToken: admin, Assets: web.Handler()}).Handler())
	defer server.Close()
	get := func(path string) (int, []byte) {
		t.Helper()
		resp, err := server.Client().Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, body
	}
	id, _, err := s.SaveNode(ctx, model.Node{Name: "slim", BillingAt: "2026-09-01T00:00:00Z", TrafficMode: "sum", Public: true}, true)
	if err != nil {
		t.Fatal(err)
	}
	task := model.Task{Name: "回程", Kind: "route", Host: "1.1.1.1", Port: 443, Family: "ipv4", Region: "上海", Carrier: "电信", Interval: 3600, Enabled: true, NodeIDs: []string{id}}
	if _, err = s.SaveTask(ctx, task, true); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	jobs, err := s.Lease(ctx, id, now)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("lease: %v %+v", err, jobs)
	}
	// 真实的回程有十几二十跳，这里照数量造，才量得出 slim 到底省了多少。
	hops := []model.Hop{}
	for i := 1; i <= 20; i++ {
		hops = append(hops, model.Hop{TTL: i, Address: "59.43.1.2", ASN: "AS4809", RTT: 3.5 + float64(i)})
	}
	latency := 8.0
	if err = s.Finish(ctx, id, model.Result{JobID: jobs[0].ID, Status: "ok", Sent: 1, Received: 1, Latency: &latency, Hops: hops}, now); err != nil {
		t.Fatal(err)
	}

	code, full := get("/api/v1/nodes/" + id + "/observations")
	// 跳表里的地址是脱敏过的（59.43.1.2 → 59.43.*.*），所以按 ASN / 跳数字段判形态。
	if code != 200 || !strings.Contains(string(full), "AS4809") || !strings.Contains(string(full), `"ttl"`) {
		t.Fatalf("默认形态必须带跳表: %d %s", code, full)
	}
	code, slim := get("/api/v1/nodes/" + id + "/observations?slim=1")
	if code != 200 || strings.Contains(string(slim), "AS4809") || strings.Contains(string(slim), `"ttl"`) {
		t.Fatalf("slim 必须去掉跳表: %d %s", code, slim)
	}
	// 卡片要用的字段一个不能少
	for _, field := range []string{`"route"`, `"status"`, `"region"`, `"carrier"`, `"stale"`} {
		if !strings.Contains(string(slim), field) {
			t.Fatalf("slim 少了 %s: %s", field, slim)
		}
	}
	if len(slim) >= len(full)/2 {
		t.Fatalf("slim 没有变瘦: %d -> %d", len(full), len(slim))
	}
}

// 延迟色带的时间窗是后台可控的：默认 60 分钟，PUT /admin/panel 改它，
// 越界被拒，样本查询按这个窗口取（而不是写死的 24 小时）。
func TestPanelLatencyWindowControlsSampleLookBack(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(filepath.Join(t.TempDir(), "window.db"))
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
	windowOf := func() float64 {
		t.Helper()
		code, body := request("GET", "/api/v1/admin/settings", admin, nil)
		if code != 200 {
			t.Fatalf("settings: %d %s", code, body)
		}
		var view struct {
			LatencyWindowMinutes float64 `json:"latency_window_minutes"`
		}
		json.Unmarshal(body, &view)
		return view.LatencyWindowMinutes
	}
	sampleCount := func() int {
		t.Helper()
		code, body := request("GET", "/api/v1/latency-samples?limit=20", admin, nil)
		if code != 200 {
			t.Fatalf("samples: %d %s", code, body)
		}
		var out struct {
			Samples []model.LatencySample `json:"samples"`
		}
		json.Unmarshal(body, &out)
		return len(out.Samples)
	}

	id, _, err := s.SaveNode(ctx, model.Node{Name: "window", BillingAt: "2026-09-01T00:00:00Z", TrafficMode: "sum", Public: true}, true)
	if err != nil {
		t.Fatal(err)
	}
	task := model.Task{Name: "延迟", Kind: "tcp", Host: "1.1.1.1", Port: 443, Family: "ipv4", Region: "上海", Carrier: "电信", Interval: 60, Enabled: true, NodeIDs: []string{id}}
	if _, err = s.SaveTask(ctx, task, true); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	// 两次探测：三小时前一次、刚刚一次。三小时前那次只在窗口 >= 3 小时时该出现。
	for _, at := range []time.Time{now.Add(-3 * time.Hour), now.Add(-time.Minute)} {
		jobs, err := s.Lease(ctx, id, at)
		if err != nil || len(jobs) != 1 {
			t.Fatalf("lease at %s: %v %+v", at, err, jobs)
		}
		latency := 20.0
		if err = s.Finish(ctx, id, model.Result{JobID: jobs[0].ID, Status: "ok", Sent: 1, Received: 1, Latency: &latency}, at); err != nil {
			t.Fatal(err)
		}
	}

	if got := windowOf(); got != model.DefaultLatencyWindowMinutes {
		t.Fatalf("未设置时该是默认 %d 分钟，得到 %v", model.DefaultLatencyWindowMinutes, got)
	}
	if got := sampleCount(); got != 1 {
		t.Fatalf("默认 60 分钟窗口只该取到最近一次探测，得到 %d 条", got)
	}

	if code, body := request("PUT", "/api/v1/admin/panel", admin, map[string]int{"latency_window_minutes": 1440}); code != 204 {
		t.Fatalf("PUT panel: %d %s", code, body)
	}
	if got := windowOf(); got != 1440 {
		t.Fatalf("设置没保存: %v", got)
	}
	if got := sampleCount(); got != 2 {
		t.Fatalf("24 小时窗口该取到两次探测，得到 %d 条", got)
	}

	for _, bad := range []int{0, model.MaxLatencyWindowMinutes + 1} {
		if code, _ := request("PUT", "/api/v1/admin/panel", admin, map[string]int{"latency_window_minutes": bad}); code != 400 {
			t.Fatalf("越界值 %d 该被拒绝，得到 %d", bad, code)
		}
	}
	if code, _ := request("PUT", "/api/v1/admin/panel", "", map[string]int{"latency_window_minutes": 30}); code != 401 {
		t.Fatalf("匿名不该能改面板设置: %d", code)
	}
}

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
	node := model.Node{Name: "private", BillingAt: "2026-09-01T00:00:00Z", TrafficMode: "sum"}
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
