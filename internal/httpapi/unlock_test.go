package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/storage"
	"github.com/leeska/TZA-Probe/web"
)

// TestUnlockCatalogAndNodeVerdicts covers the two public unlock endpoints: the
// catalogue the panel groups by, and one node's verdicts — including that a
// node which never ran the check answers with an empty summary rather than 404,
// and that a private node stays invisible to an anonymous caller.
func TestUnlockCatalogAndNodeVerdicts(t *testing.T) {
	s, err := storage.Open(filepath.Join(t.TempDir(), "unlock.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
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

	// 未检测：空摘要，不是 404 —— 面板要能把「没跑过」和「无权查看」分开。
	code, body = get("/api/v1/nodes/" + public + "/unlocks")
	if code != 200 {
		t.Fatalf("empty summary: %d", code)
	}
	var summary model.UnlockSummary
	if err = json.Unmarshal(body, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Score != 0 || summary.Total != len(model.UnlockCatalog) || len(summary.Services) != 0 {
		t.Fatalf("summary=%+v", summary)
	}
	if code, _ = get("/api/v1/nodes/" + private + "/unlocks"); code != 404 {
		t.Fatalf("private node leaked: %d", code)
	}

	// 真跑一轮（直接走存储，等价于 Agent 上报）：结论出现在 /nodes 与 /unlocks 上。
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
	code, body = get("/api/v1/nodes/" + public + "/unlocks")
	if code != 200 {
		t.Fatalf("summary: %d", code)
	}
	if err = json.Unmarshal(body, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Score != 2 || len(summary.Services) != 2 {
		t.Fatalf("summary=%+v", summary)
	}
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
