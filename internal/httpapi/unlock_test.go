package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/storage"
	"github.com/leeska/TZA-Probe/web"
)

// TestUnlockCatalogAndNodeVerdicts covers the public unlock data: the catalogue
// the panel groups by, and each node's verdicts on /nodes — empty before the
// check ran, private nodes invisible to an anonymous caller.
func TestUnlockCatalogAndNodeVerdicts(t *testing.T) {
	s, err := storage.Open(filepath.Join(t.TempDir(), "unlock.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := model.Secret()
	api := &API{Store: s, AdminToken: admin, Assets: web.Handler()}
	server := httptest.NewServer(api.Handler())
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

	// The catalogue is public: the anonymous panel draws the same panel.
	code, body := get("/api/v1/unlock-catalog")
	if code != 200 {
		t.Fatalf("catalog: %d", code)
	}
	var catalog struct {
		Categories []string              `json:"categories"`
		Services   []model.UnlockService `json:"services"`
	}
	if err = json.Unmarshal(body, &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Services) != 24 || len(catalog.Categories) != 4 {
		t.Fatalf("catalog=%d services / %d categories", len(catalog.Services), len(catalog.Categories))
	}
	if catalog.Services[0].ID != "netflix" || catalog.Services[0].Category != model.UnlockCategoryStream {
		t.Fatalf("first entry=%+v", catalog.Services[0])
	}

	start := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	public, token, err := s.SaveNode(ctx, model.Node{Name: "公开", Public: true, BillingAt: "2026-09-01T00:00:00Z", TrafficMode: "sum"}, true)
	if err != nil {
		t.Fatal(err)
	}
	private, _, err := s.SaveNode(ctx, model.Node{Name: "私有", Public: false, BillingAt: "2026-09-01T00:00:00Z", TrafficMode: "sum"}, true)
	if err != nil {
		t.Fatal(err)
	}
	nodeID, err := s.Authenticate(ctx, token)
	if err != nil || nodeID != public {
		t.Fatalf("authenticate: %s %v", nodeID, err)
	}

	// 解锁结论只随 /nodes 下发（unlock 字段），单独的 /nodes/{id}/unlocks 已经去掉。
	if code, _ = get("/api/v1/nodes/" + public + "/unlocks"); code != 404 {
		t.Fatalf("removed endpoint still answers: %d", code)
	}
	// 未检测：unlock 字段为空；私有节点不出现在匿名列表里。
	code, body = get("/api/v1/nodes")
	if code != 200 {
		t.Fatalf("nodes: %d", code)
	}
	var before []model.NodeView
	if err = json.Unmarshal(body, &before); err != nil {
		t.Fatal(err)
	}
	for _, n := range before {
		if n.ID == private {
			t.Fatalf("private node leaked: %s", body)
		}
		if n.ID == public && n.Unlock != nil {
			t.Fatalf("untested node has a summary: %+v", n.Unlock)
		}
	}

	// 真跑一轮（直接走存储，等价于 Agent 上报）：结论出现在 /nodes 上。
	m := model.Metrics{CPU: 5, MemoryUsed: 10, MemoryTotal: 100, DiskUsed: 10, DiskTotal: 100, Capabilities: []string{model.CapabilityUnlock}}
	if err = s.Report(ctx, public, m, start); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveTask(ctx, model.Task{Name: "解锁检测", Kind: model.UnlockKind, Interval: model.UnlockMinInterval, Enabled: true, NodeIDs: []string{public}}, true); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.Lease(ctx, public, start.Add(time.Second))
	if err != nil || len(jobs) != 1 {
		t.Fatalf("lease: %+v %v", jobs, err)
	}
	if err = s.Finish(ctx, public, model.Result{JobID: jobs[0].ID, Status: "ok", Unlocks: []model.UnlockResult{
		{Service: "netflix", Status: model.UnlockOriginalsOnly, Detail: "Originals Only"},
		{Service: "youtube_premium", Status: model.UnlockYes, Region: "JP"},
	}}, start.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	// 匿名列表走 1 秒快照，上面那次读已经缓存了未检测的结果。
	api.nodesSnap = snapshot[[]model.NodeView]{}
	code, body = get("/api/v1/nodes")
	if code != 200 {
		t.Fatalf("nodes: %d", code)
	}
	var nodes []model.NodeView
	if err = json.Unmarshal(body, &nodes); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range nodes {
		if n.ID == public {
			found = n.Unlock != nil && n.Unlock.Score == 2 && len(n.ProbeTargets) == 1 && n.ProbeTargets[0].Kind == model.UnlockKind
		}
	}
	if !found {
		t.Fatalf("public node lost its unlock summary: %s", body)
	}
}

// The anonymous node list keeps alert rules and the server-kept bookkeeping
// fields (traffic correction, creation time) with the administrator, and a 409
// says which conflict it is instead of one catch-all line.
func TestPublicNodeRedactionAndConflictMessage(t *testing.T) {
	s, err := storage.Open(filepath.Join(t.TempDir(), "redact.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := model.Secret()
	server := httptest.NewServer((&API{Store: s, AdminToken: admin, Assets: web.Handler()}).Handler())
	defer server.Close()
	id, _, err := s.SaveNode(ctx, model.Node{Name: "公开", Public: true, BillingAt: "2026-09-01T00:00:00Z", TrafficMode: "sum",
		Alerts: model.Alerts{Offline: true, OfflineSeconds: 60}}, true)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	start, _ := model.Cycle(now, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	if err = s.CorrectTraffic(ctx, id, 5e9, start.Unix(), now); err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body string, asAdmin bool) (int, map[string]any, []byte) {
		t.Helper()
		req := httptest.NewRequest(method, server.URL+path, strings.NewReader(body))
		req.RequestURI = ""
		req.Header.Set("Content-Type", "application/json")
		if asAdmin {
			req.Header.Set("Authorization", "Bearer "+admin)
		}
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var obj map[string]any
		json.Unmarshal(raw, &obj)
		return resp.StatusCode, obj, raw
	}
	_, _, public := call("GET", "/api/v1/nodes", "", false)
	for _, leak := range []string{"traffic_adjustment", "adjustment_cycle", "created_at", `"offline":true`} {
		if strings.Contains(string(public), leak) {
			t.Fatalf("anonymous list leaks %s: %s", leak, public)
		}
	}
	_, _, private := call("GET", "/api/v1/admin/nodes", "", true)
	if !strings.Contains(string(private), "traffic_adjustment") || !strings.Contains(string(private), `"offline":true`) {
		t.Fatalf("admin list lost its fields: %s", private)
	}
	code, obj, _ := call("POST", "/api/v1/admin/nodes/"+id+"/traffic", `{"used":1,"cycle":1}`, true)
	if code != 409 || obj["error"] != "计费周期已变化，请刷新后重新校正" {
		t.Fatalf("conflict: %d %v", code, obj)
	}
}
