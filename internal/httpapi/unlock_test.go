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
	public, token, err := s.SaveNode(ctx, model.Node{Name: "公开", Public: true, Billing: model.Billing{ResetAt: "2026-09-01T00:00:00Z", TrafficMode: "sum"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	private, _, err := s.SaveNode(ctx, model.Node{Name: "私有", Public: false, Billing: model.Billing{ResetAt: "2026-09-01T00:00:00Z", TrafficMode: "sum"}}, true)
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
	if _, err = s.SaveTask(ctx, model.Task{Name: "解锁检测", Enabled: true, NodeIDs: []string{public}, Type: model.TaskUnlock, Unlock: &model.UnlockSpec{Interval: model.UnlockMinInterval}}, true); err != nil {
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
	id, _, err := s.SaveNode(ctx, model.Node{Name: "公开", Public: true, Alerts: model.Alerts{{Kind: model.AlertOffline, HoldSeconds: 60}}, Billing: model.Billing{ResetAt: "2026-09-01T00:00:00Z", TrafficMode: "sum"}}, true)
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
	for _, leak := range []string{"traffic_adjustment", "adjustment_cycle", "created_at", `"kind":"offline"`} {
		if strings.Contains(string(public), leak) {
			t.Fatalf("anonymous list leaks %s: %s", leak, public)
		}
	}
	// 服务端自记的校正与创建时间不在节点配置里，管理员视图也不带；提醒规则只给管理员。
	_, _, private := call("GET", "/api/v1/admin/nodes", "", true)
	if strings.Contains(string(private), "traffic_adjustment") || strings.Contains(string(private), "created_at") || !strings.Contains(string(private), `"kind":"offline"`) {
		t.Fatalf("admin list: %s", private)
	}
	code, obj, _ := call("POST", "/api/v1/admin/nodes/"+id+"/traffic", `{"used":1,"cycle":1}`, true)
	if code != 409 || obj["error"] != "计费周期已变化，请刷新后重新校正" {
		t.Fatalf("conflict: %d %v", code, obj)
	}
}

// The step-2 routes: task enable / run / batch bind, batch alert rules, the
// two settings resources, and the removed catch-all routes answering 404.
func TestResourceRoutes(t *testing.T) {
	s, err := storage.Open(filepath.Join(t.TempDir(), "routes.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	admin := model.Secret()
	server := httptest.NewServer((&API{Store: s, AdminToken: admin, Assets: web.Handler()}).Handler())
	defer server.Close()
	call := func(method, path, body string) (int, string) {
		t.Helper()
		req := httptest.NewRequest(method, server.URL+path, strings.NewReader(body))
		req.RequestURI = ""
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+admin)
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}
	node := model.Node{Name: "甲", Billing: model.Billing{ResetAt: "2026-09-01T00:00:00Z", TrafficMode: "sum"}}
	a, _, err := s.SaveNode(ctx, node, true)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := s.SaveNode(ctx, node, true)
	if err != nil {
		t.Fatal(err)
	}
	var tasks []string
	for _, name := range []string{"一", "二"} {
		id, err := s.SaveTask(ctx, model.Task{Name: name, Enabled: true, Type: model.TaskProbe, Probe: &model.ProbeSpec{Checks: []string{"tcp"}, Targets: []model.Target{{Host: "example.com", Port: 443, Family: "ipv4"}}, Interval: 3600}}, true)
		if err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, id)
	}
	if code, body := call("POST", "/api/v1/admin/tasks/"+tasks[0]+"/run", ""); code != 409 || !strings.Contains(body, "未绑定节点") {
		t.Fatalf("run unbound: %d %s", code, body)
	}
	if code, body := call("PUT", "/api/v1/admin/tasks/bindings", `{"task_ids":["`+tasks[0]+`","`+tasks[1]+`"],"node_ids":["`+a+`","`+b+`"]}`); code != 204 {
		t.Fatalf("bind: %d %s", code, body)
	}
	if code, body := call("PUT", "/api/v1/admin/tasks/bindings", `{"task_ids":["`+tasks[0]+`"],"node_ids":["nope"]}`); code != 400 || !strings.Contains(body, "节点不存在") {
		t.Fatalf("bind missing node: %d %s", code, body)
	}
	// add / remove 只增删给出的节点；与 node_ids 混用是 400。
	if code, body := call("PUT", "/api/v1/admin/tasks/bindings", `{"task_ids":["`+tasks[0]+`"],"node_ids":[],"add":["`+a+`"]}`); code != 400 {
		t.Fatalf("mixed binding modes: %d %s", code, body)
	}
	if code, body := call("PUT", "/api/v1/admin/tasks/bindings", `{"task_ids":["`+tasks[0]+`","`+tasks[1]+`"],"remove":["`+b+`"]}`); code != 204 {
		t.Fatalf("remove binding: %d %s", code, body)
	}
	if code, body := call("PUT", "/api/v1/admin/tasks/bindings", `{"task_ids":["`+tasks[0]+`","`+tasks[1]+`"],"add":["`+b+`"]}`); code != 204 {
		t.Fatalf("add binding: %d %s", code, body)
	}
	if code, _ := call("POST", "/api/v1/admin/tasks/"+tasks[0]+"/run", ""); code != 204 {
		t.Fatalf("run: %d", code)
	}
	if code, _ := call("PUT", "/api/v1/admin/tasks/"+tasks[1]+"/enabled", `{"enabled":false}`); code != 204 {
		t.Fatalf("pause: %d", code)
	}
	if code, _ := call("PUT", "/api/v1/admin/tasks/"+tasks[1]+"/enabled", `{}`); code != 400 {
		t.Fatalf("enabled missing: %d", code)
	}
	list, _ := s.Tasks(ctx)
	for _, task := range list {
		if len(task.NodeIDs) != 2 || task.Enabled != (task.ID == tasks[0]) {
			t.Fatalf("task %s: enabled=%v nodes=%v", task.Name, task.Enabled, task.NodeIDs)
		}
	}
	if code, body := call("PUT", "/api/v1/admin/nodes/alerts", `{"node_ids":["`+a+`","`+b+`"],"alerts":[{"kind":"offline","hold_seconds":90}]}`); code != 204 {
		t.Fatalf("alerts: %d %s", code, body)
	}
	if code, body := call("PUT", "/api/v1/admin/nodes/alerts", `{"node_ids":["`+a+`"],"alerts":[{"kind":"throttle"}]}`); code != 400 || !strings.Contains(body, "甲：") {
		t.Fatalf("alerts invalid: %d %s", code, body)
	}
	// PATCH 只改提交的字段，与 MCP 的 update_node / update_task 是同一个合并。
	if code, body := call("PATCH", "/api/v1/admin/nodes/"+a, `{"group":"HK","alerts":[{"kind":"cpu","threshold":80,"hold_seconds":60}]}`); code != 200 || !strings.Contains(body, `"group":"HK"`) || !strings.Contains(body, `"kind":"cpu"`) || strings.Contains(body, `"kind":"offline"`) {
		t.Fatalf("patch node: %d %s", code, body)
	}
	if code, body := call("PATCH", "/api/v1/admin/nodes/"+a, `{"gruop":"HK"}`); code != 400 {
		t.Fatalf("patch typo: %d %s", code, body)
	}
	if code, body := call("PATCH", "/api/v1/admin/tasks/"+tasks[1], `{"name":"二改"}`); code != 200 || !strings.Contains(body, "二改") || !strings.Contains(body, b) {
		t.Fatalf("patch task: %d %s", code, body)
	}
	if code, _ := call("PATCH", "/api/v1/admin/tasks/nope", `{}`); code != 404 {
		t.Fatalf("patch missing task: %d", code)
	}
	if code, body := call("GET", "/api/v1/admin/notification", ""); code != 200 || !strings.Contains(body, `"channel"`) || strings.Contains(body, "latency_window") {
		t.Fatalf("notification: %d %s", code, body)
	}
	// PATCH 通知出口只改传了的项（与 MCP update_settings 同规则）：一项都不传是 400，关闭要显式传 none。
	if code, body := call("PATCH", "/api/v1/admin/notification", `{}`); code != 400 || !strings.Contains(body, "channel=none") {
		t.Fatalf("empty notification patch: %d %s", code, body)
	}
	if code, body := call("PATCH", "/api/v1/admin/notification", `{"channel":"none"}`); code != 200 || !strings.Contains(body, `"channel":"none"`) {
		t.Fatalf("notification patch: %d %s", code, body)
	}
	if code, body := call("GET", "/api/v1/admin/panel", ""); code != 200 || !strings.Contains(body, "latency_window_minutes") || strings.Contains(body, "channel") {
		t.Fatalf("panel: %d %s", code, body)
	}
	for _, old := range [][2]string{{"POST", "/api/v1/admin/tasks/" + tasks[0] + "/control"}, {"GET", "/api/v1/admin/settings"}, {"POST", "/api/v1/admin/settings/test"}} {
		if code, _ := call(old[0], old[1], `{"action":"run"}`); code != 404 && code != 405 {
			t.Fatalf("removed route %s still answers %d", old[1], code)
		}
	}
}

// /nodes/live carries only the per-second state of the same nodes /nodes
// shows: no configuration, no probe plan, and no private node for a visitor.
func TestLiveNodes(t *testing.T) {
	s, err := storage.Open(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	server := httptest.NewServer((&API{Store: s, AdminToken: model.Secret(), Assets: web.Handler()}).Handler())
	defer server.Close()
	billing := model.Billing{ResetAt: "2026-09-01T00:00:00Z", TrafficMode: "sum", Price: 9.9}
	public, token, err := s.SaveNode(ctx, model.Node{Name: "公开", Public: true, Billing: billing}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.SaveNode(ctx, model.Node{Name: "私有", Billing: billing}, true); err != nil {
		t.Fatal(err)
	}
	id, _ := s.Authenticate(ctx, token)
	if err = s.Report(ctx, id, model.Metrics{CPU: 7, MemoryUsed: 1, MemoryTotal: 2, DiskTotal: 1}, time.Now()); err != nil {
		t.Fatal(err)
	}
	resp, err := server.Client().Get(server.URL + "/api/v1/nodes/live")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var live []map[string]any
	if err = json.NewDecoder(resp.Body).Decode(&live); err != nil || len(live) != 1 {
		t.Fatalf("live: %v %v", live, err)
	}
	got := live[0]
	if got["id"] != public || got["online"] != true || got["metrics"].(map[string]any)["cpu"] != 7.0 {
		t.Fatalf("live entry: %v", got)
	}
	want := map[string]bool{"id": true, "online": true, "last_seen": true, "metrics": true, "traffic_used": true, "avg_bps": true, "covered_hours": true, "window_hours": true, "throttled": true}
	for key := range got {
		if !want[key] {
			t.Fatalf("live view carries %q: %v", key, got)
		}
	}
}
