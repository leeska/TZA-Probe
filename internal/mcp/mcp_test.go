package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

type fakeSource struct {
	nodes        []model.NodeView
	tasks        []model.Task
	history      []model.Sample
	series       []model.NetworkSeries
	observations []model.Observation
	events       []model.Event
	samples      []model.LatencySample
	askedHours   []int
	askedLimit   int
	askedSince   time.Time

	// write side
	failure    error
	deleted    []string
	rotated    []string
	controlled []string
	alerted    []string
	savedNodes []model.Node
	savedTasks []model.Task
	runs       []model.TaskRun
	settings   map[string]string
	corrected  []int64
}

func (f *fakeSource) Nodes(context.Context, time.Time) ([]model.NodeView, error) { return f.nodes, nil }
func (f *fakeSource) Tasks(context.Context) ([]model.Task, error)                { return f.tasks, nil }
func (f *fakeSource) History(_ context.Context, _ string, hours int, _ time.Time) ([]model.Sample, error) {
	f.askedHours = append(f.askedHours, hours)
	return f.history, nil
}
func (f *fakeSource) NetworkHistory(_ context.Context, _ string, hours int, _ time.Time) ([]model.NetworkSeries, error) {
	f.askedHours = append(f.askedHours, hours)
	return f.series, nil
}
func (f *fakeSource) Observations(context.Context, string, time.Time) ([]model.Observation, error) {
	return f.observations, nil
}
func (f *fakeSource) Events(context.Context, model.EventQuery) ([]model.Event, error) {
	return f.events, nil
}
func (f *fakeSource) RecentLatencySamples(_ context.Context, ids []string, limit int, since time.Time) ([]model.LatencySample, error) {
	f.askedLimit = limit
	f.askedSince = since
	if len(ids) == 0 {
		return []model.LatencySample{}, nil
	}
	return f.samples, nil
}

func (f *fakeSource) SaveNode(_ context.Context, n model.Node, create bool) (string, string, error) {
	if f.failure != nil {
		return "", "", f.failure
	}
	f.savedNodes = append(f.savedNodes, n)
	if create {
		return "new-node", "new-token", nil
	}
	return n.ID, "", nil
}

func (f *fakeSource) DeleteNode(_ context.Context, id string) error {
	if f.failure != nil {
		return f.failure
	}
	if !f.hasNode(id) {
		return model.ErrNotFound
	}
	f.deleted = append(f.deleted, id)
	return nil
}

// hasNode mirrors storage: deleting something that is not there is not a no-op.
func (f *fakeSource) hasNode(id string) bool {
	for _, n := range f.nodes {
		if n.ID == id {
			return true
		}
	}
	return false
}

func (f *fakeSource) Rotate(_ context.Context, id string) (string, error) {
	if f.failure != nil {
		return "", f.failure
	}
	f.rotated = append(f.rotated, id)
	return "rotated-token", nil
}

func (f *fakeSource) CorrectTraffic(_ context.Context, id string, used, cycle int64, _ time.Time) error {
	if f.failure != nil {
		return f.failure
	}
	f.corrected = append(f.corrected, used, cycle)
	return nil
}

func (f *fakeSource) SaveTask(_ context.Context, task model.Task, create bool) (string, error) {
	if f.failure != nil {
		return "", f.failure
	}
	f.savedTasks = append(f.savedTasks, task)
	if create {
		return "new-task", nil
	}
	return task.ID, nil
}

func (f *fakeSource) DeleteTask(_ context.Context, id string) error {
	if f.failure != nil {
		return f.failure
	}
	found := false
	for _, task := range f.tasks {
		if task.ID == id {
			found = true
		}
	}
	if !found {
		return model.ErrNotFound
	}
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *fakeSource) control(action string) error {
	if f.failure != nil {
		return f.failure
	}
	f.controlled = append(f.controlled, action)
	return nil
}
func (f *fakeSource) SetTaskEnabled(_ context.Context, _ string, enabled bool) error {
	if enabled {
		return f.control("resume")
	}
	return f.control("pause")
}
func (f *fakeSource) RunTask(context.Context, string, time.Time) error { return f.control("run") }
func (f *fakeSource) BindTasks(context.Context, []string, []string) error {
	return f.control("bind")
}
func (f *fakeSource) SaveNodeOrder(context.Context, []string) error { return f.failure }
func (f *fakeSource) SetNodeAlerts(_ context.Context, ids []string, _ model.Alerts) error {
	if f.failure != nil {
		return f.failure
	}
	f.alerted = append(f.alerted, ids...)
	return nil
}

func (f *fakeSource) TaskRuns(context.Context, string, time.Time) ([]model.TaskRun, error) {
	if f.failure != nil {
		return nil, f.failure
	}
	return f.runs, nil
}

func (f *fakeSource) Setting(_ context.Context, key string) (string, error) {
	return f.settings[key], nil
}

func (f *fakeSource) SetSetting(_ context.Context, key, value string) error {
	if f.failure != nil {
		return f.failure
	}
	if f.settings == nil {
		f.settings = map[string]string{}
	}
	f.settings[key] = value
	return nil
}

func fixture() *fakeSource {
	return &fakeSource{
		nodes: []model.NodeView{
			{Node: model.Node{ID: "n1", Name: "香港", Public: true, BillingAt: "2026-09-01T00:00:00Z", TrafficMode: "sum", Alerts: model.Alerts{OfflineSeconds: 60}}, Online: true, TrafficUsed: 1234},
			{Node: model.Node{ID: "n2", Name: "东京"}, Online: false},
		},
		tasks: []model.Task{{
			ID: "t1", Name: "电信回程", Type: model.TaskProbe, Enabled: true, NodeIDs: []string{"n1"},
			Probe: &model.ProbeSpec{Checks: []string{"route"}, RouteInterval: 1800, Targets: []model.Target{
				{Host: "219.141.147.210", Port: 80, Family: "ipv4", Region: "上海", Carrier: "电信"},
			}},
		}},
		history: []model.Sample{{At: 1, Metrics: model.Metrics{CPU: 12, MemoryTotal: 100}}},
		series: []model.NetworkSeries{{
			Name: "电信回程", Family: "ipv4", Host: "1.1.1.1", Port: 443,
			Samples: []model.NetworkSample{{At: 1}},
		}},
		observations: []model.Observation{
			{
				Task: model.Probe{ID: "t1", Kind: "route", Host: "1.1.1.1"},
				At:   2,
				Result: model.Result{
					Status: "ok", Route: "CN2GT",
					Hops: []model.Hop{{TTL: 1, Address: "59.43.1.2", ASN: "4134"}, {TTL: 2, Address: "202.97.5.6", ASN: "as4134"}},
				},
			},
			{
				Task:   model.Probe{ID: "t1", Kind: "route", Host: "1.1.1.1"},
				At:     1,
				Stale:  true,
				Result: model.Result{Status: "ok", Route: "CN2GIA"},
			},
		},
		events:  []model.Event{{ID: 7, NodeID: "n1", NodeName: "香港", Kind: "offline", Message: "离线"}},
		samples: []model.LatencySample{{NodeID: "n1", Carrier: "电信", Family: "ipv4", At: 3, Sent: 3, Received: 3}},
	}
}

type stubSender struct{ sent []model.Event }

func (s *stubSender) Send(_ context.Context, _ string, event model.Event) error {
	s.sent = append(s.sent, event)
	return nil
}

type testEnv struct {
	server *httptest.Server
	source *fakeSource
	sender *stubSender
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	source := fixture()
	sender := &stubSender{}
	server := httptest.NewServer(&Server{Source: source, Notifier: sender, Version: "2.0.0-test"})
	t.Cleanup(server.Close)
	return &testEnv{server: server, source: source, sender: sender}
}

func (e *testEnv) raw(t *testing.T, body string, headers ...string) (int, string, http.Header) {
	t.Helper()
	req, err := http.NewRequest("POST", e.server.URL, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := e.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data), resp.Header
}

func (e *testEnv) post(t *testing.T, body string, headers ...string) (int, map[string]any, http.Header) {
	t.Helper()
	status, raw, header := e.raw(t, body, headers...)
	out := map[string]any{}
	if len(bytes.TrimSpace([]byte(raw))) > 0 {
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatalf("response is not JSON (%s): %s", err, raw)
		}
	}
	return status, out, header
}

// modern posts a request the way the 2026-07-28 revision requires: version in
// _meta, mirrored into the version, method and name headers.
func (e *testEnv) modern(t *testing.T, id, method, name, arguments string) (int, map[string]any) {
	t.Helper()
	params := `{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}`
	if method == "tools/call" {
		params = `{"name":"` + name + `","arguments":` + arguments + `,"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}`
	}
	body := `{"jsonrpc":"2.0","id":` + id + `,"method":"` + method + `","params":` + params + `}`
	headers := []string{"MCP-Protocol-Version", Modern, "Mcp-Method", method}
	if name != "" {
		headers = append(headers, "Mcp-Name", name)
	}
	status, out, _ := e.post(t, body, headers...)
	return status, out
}

func errorOf(t *testing.T, out map[string]any) (int, string) {
	t.Helper()
	e, ok := out["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected a JSON-RPC error, got %v", out)
	}
	code, _ := e["code"].(float64)
	message, _ := e["message"].(string)
	return int(code), message
}

func rpcResult(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	result, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected a JSON-RPC result, got %v", out)
	}
	return result
}

// codeOf reads the JSON-RPC error code of a response.
func codeOf(t *testing.T, out map[string]any) int {
	t.Helper()
	code, _ := errorOf(t, out)
	return code
}

// text reads the readable half of a tool result.
func text(t *testing.T, result map[string]any) string {
	t.Helper()
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("tool result has no content: %v", result)
	}
	block, _ := content[0].(map[string]any)
	value, _ := block["text"].(string)
	return value
}

func invoke(t *testing.T, env *testEnv, name, arguments string) map[string]any {
	t.Helper()
	code, out := env.modern(t, "3", "tools/call", name, arguments)
	if code != 200 {
		t.Fatalf("%s: status %d %v", name, code, out)
	}
	return rpcResult(t, out)
}

// structured reads the machine-readable half of a tool result; the text half
// carries the same JSON for models that only read text.
func structured(t *testing.T, result map[string]any) map[string]any {
	t.Helper()
	content, ok := result["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("tool result has no structured content: %v", result)
	}
	text, _ := result["content"].([]any)
	if len(text) != 1 {
		t.Fatalf("tool result must carry one text block: %v", result)
	}
	block, _ := text[0].(map[string]any)
	if block["type"] != "text" || !strings.Contains(block["text"].(string), `"`) {
		t.Fatalf("text block is not the readable payload: %v", block)
	}
	return content
}

// readOnly says which tools only read; the annotations must agree.
var readOnly = map[string]bool{
	"list_nodes": true, "list_tasks": true, "node_history": true, "node_network_history": true,
	"node_observations": true, "recent_events": true, "latency_samples": true,
	"list_catalog": true, "task_runs": true, "get_settings": true,
}

func TestModernRevisionRoundTrip(t *testing.T) {
	env := newEnv(t)

	code, out := env.modern(t, `"discover-1"`, "server/discover", "", "")
	if code != 200 {
		t.Fatalf("discover: %d %v", code, out)
	}
	discover := rpcResult(t, out)
	if discover["resultType"] != "complete" {
		t.Fatalf("discover must be a complete result: %v", discover)
	}
	versions, _ := discover["supportedVersions"].([]any)
	if len(versions) != 3 || versions[0] != Modern {
		t.Fatalf("supported versions: %v", versions)
	}
	meta, _ := discover["_meta"].(map[string]any)
	info, _ := meta[serverInfoKey].(map[string]any)
	if info["name"] != "tza-probe" || info["version"] != "2.0.0-test" {
		t.Fatalf("serverInfo: %v", meta)
	}

	code, out = env.modern(t, "2", "tools/list", "", "")
	if code != 200 {
		t.Fatalf("tools/list: %d %v", code, out)
	}
	list := rpcResult(t, out)
	if list["resultType"] != "complete" || list["cacheScope"] != "public" || list["ttlMs"] == nil {
		t.Fatalf("list result must be cacheable and complete: %v", list)
	}
	entries, _ := list["tools"].([]any)
	want := []string{
		"list_nodes", "list_tasks", "node_history", "node_network_history", "node_observations", "recent_events", "latency_samples",
		"create_node", "update_node", "delete_node", "rotate_node_token", "correct_node_traffic", "set_node_alerts", "list_catalog",
		"create_task", "update_task", "delete_task", "control_task", "task_runs",
		"get_settings", "update_settings", "test_notification",
	}
	if len(entries) != len(want) {
		t.Fatalf("tool count: %d", len(entries))
	}
	for i, name := range want {
		entry, _ := entries[i].(map[string]any)
		if entry["name"] != name {
			t.Fatalf("tool %d is %v, want %s", i, entry["name"], name)
		}
		schema, _ := entry["inputSchema"].(map[string]any)
		if schema["type"] != "object" || schema["additionalProperties"] != false {
			t.Fatalf("%s schema: %v", name, schema)
		}
		// The client decides whether to ask for confirmation from the hint, so a
		// tool that only reads must not be marked destructive and vice versa.
		notes, _ := entry["annotations"].(map[string]any)
		if notes["readOnlyHint"] != readOnly[name] {
			t.Fatalf("%s readOnlyHint: %v", name, notes)
		}
		if notes["destructiveHint"] == true && readOnly[name] {
			t.Fatalf("%s is read-only but destructive: %v", name, notes)
		}
	}

	result := invoke(t, env, "list_nodes", `{}`)
	nodes := structured(t, result)["nodes"].([]any)
	if len(nodes) != 2 {
		t.Fatalf("nodes: %v", nodes)
	}
	if result["isError"] != nil {
		t.Fatalf("list_nodes failed: %v", result)
	}
}

func TestModernHeaderValidation(t *testing.T) {
	env := newEnv(t)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`
	for _, tc := range []struct {
		name    string
		headers []string
		status  int
		code    int
	}{
		{"version header missing", []string{"Mcp-Method", "tools/list"}, 400, codeHeaderMismatch},
		{"method header missing", []string{"MCP-Protocol-Version", Modern}, 400, codeHeaderMismatch},
		{"method header mismatched", []string{"MCP-Protocol-Version", Modern, "Mcp-Method", "tools/call"}, 400, codeHeaderMismatch},
		{"version header mismatched", []string{"MCP-Protocol-Version", legacyLatest, "Mcp-Method", "tools/list"}, 400, codeHeaderMismatch},
		{"version unsupported", []string{"MCP-Protocol-Version", "1900-01-01", "Mcp-Method", "tools/list"}, 400, codeVersionUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, out, _ := env.post(t, body, tc.headers...)
			if status != tc.status {
				t.Fatalf("status %d, want %d (%v)", status, tc.status, out)
			}
			code, _ := errorOf(t, out)
			if code != tc.code {
				t.Fatalf("code %d, want %d (%v)", code, tc.code, out)
			}
		})
	}

	// Mcp-Name is required for tools/call and must match the body, including
	// when the client had to Base64-encode it.
	nameBody := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_nodes","arguments":{},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`
	status, out, _ := env.post(t, nameBody, "MCP-Protocol-Version", Modern, "Mcp-Method", "tools/call")
	if status != 400 {
		t.Fatalf("missing Mcp-Name: %d %v", status, out)
	}
	status, out, _ = env.post(t, nameBody, "MCP-Protocol-Version", Modern, "Mcp-Method", "tools/call", "Mcp-Name", "list_tasks")
	if status != 400 {
		t.Fatalf("mismatched Mcp-Name: %d %v", status, out)
	}
	if code, message := errorOf(t, out); code != codeHeaderMismatch || !strings.Contains(message, "list_tasks") {
		t.Fatalf("mismatch error must name both values: %v", out)
	}
	encoded := "=?base64?" + base64.StdEncoding.EncodeToString([]byte("list_nodes")) + "?="
	if status, out, _ = env.post(t, nameBody, "MCP-Protocol-Version", Modern, "Mcp-Method", "tools/call", "Mcp-Name", encoded); status != 200 {
		t.Fatalf("encoded Mcp-Name rejected: %d %v", status, out)
	}

	// The version error must advertise what this server does speak.
	status, out, _ = env.post(t, body, "MCP-Protocol-Version", "1900-01-01", "Mcp-Method", "tools/list")
	data, _ := out["error"].(map[string]any)["data"].(map[string]any)
	supported, _ := data["supported"].([]any)
	if status != 400 || len(supported) != 3 || data["requested"] != "1900-01-01" {
		t.Fatalf("unsupported version payload: %v", out)
	}

	// An unknown method is a 404 in the modern revision, so a client can tell
	// this apart from a legacy server answering 200.
	unknown := `{"jsonrpc":"2.0","id":1,"method":"resources/subscribe","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`
	status, out, _ = env.post(t, unknown, "MCP-Protocol-Version", Modern, "Mcp-Method", "resources/subscribe")
	if status != 404 {
		t.Fatalf("unknown method: %d %v", status, out)
	}
	if code, _ := errorOf(t, out); code != codeMethodNotFound {
		t.Fatalf("unknown method code: %v", out)
	}
}

func TestLegacyHandshake(t *testing.T) {
	env := newEnv(t)
	// The handshake revision declares its version in the body and sends no
	// version header; the answer must stay in that revision.
	init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`
	status, out, _ := env.post(t, init)
	if status != 200 {
		t.Fatalf("initialize: %d %v", status, out)
	}
	result := rpcResult(t, out)
	if result["protocolVersion"] != legacyLatest || result["resultType"] != nil || result["_meta"] != nil {
		t.Fatalf("legacy result must not carry modern fields: %v", result)
	}
	capabilities, _ := result["capabilities"].(map[string]any)
	if _, ok := capabilities["tools"]; !ok {
		t.Fatalf("capabilities: %v", capabilities)
	}

	// A newer client asking for the modern version inside initialize is answered
	// with a version both sides speak instead of being rejected.
	modernInit := strings.Replace(init, "2025-11-25", Modern, 1)
	if _, out, _ = env.post(t, modernInit); rpcResult(t, out)["protocolVersion"] != Modern {
		t.Fatalf("initialize echo: %v", out)
	}
	oldInit := strings.Replace(init, "2025-11-25", "1900-01-01", 1)
	status, out, _ = env.post(t, oldInit)
	if status != 400 {
		t.Fatalf("unknown initialize version: %d %v", status, out)
	}
	if code, _ := errorOf(t, out); code != codeVersionUnknown {
		t.Fatalf("initialize version code: %v", out)
	}

	// After the handshake the client sends the negotiated version as a header
	// and gets plain results, with no resultType or _meta.
	legacyList := `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`
	status, out, _ = env.post(t, legacyList, "MCP-Protocol-Version", legacyLatest)
	if status != 200 {
		t.Fatalf("legacy tools/list: %d %v", status, out)
	}
	result = rpcResult(t, out)
	if result["resultType"] != nil || result["ttlMs"] != nil {
		t.Fatalf("legacy list must not advertise modern caching: %v", result)
	}
	if entries, _ := result["tools"].([]any); len(entries) != len(tools) {
		t.Fatalf("legacy tool count: %d", len(entries))
	}

	// A client that never sends the header at all is treated as the oldest
	// handshake revision rather than rejected.
	status, out, _ = env.post(t, legacyList)
	if status != 200 {
		t.Fatalf("bare tools/list: %d %v", status, out)
	}

	// tools/call works in both eras and an unknown method stays a 200 error.
	status, out, _ = env.post(t, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_tasks","arguments":{}}}`, "MCP-Protocol-Version", legacyLatest)
	if status != 200 || structured(t, rpcResult(t, out))["count"].(float64) != 1 {
		t.Fatalf("legacy tools/call: %d %v", status, out)
	}
	status, out, _ = env.post(t, `{"jsonrpc":"2.0","id":4,"method":"resources/subscribe","params":{}}`, "MCP-Protocol-Version", legacyLatest)
	if status != 200 {
		t.Fatalf("legacy unknown method status: %d", status)
	}
}

func TestTransportRules(t *testing.T) {
	env := newEnv(t)
	resp, err := env.server.Client().Get(env.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 405 || resp.Header.Get("Allow") != "POST" {
		t.Fatalf("GET must be refused: %d %v", resp.StatusCode, resp.Header)
	}

	// A notification asks for nothing back, so the transport acknowledges it.
	notification := `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	status, out, _ := env.post(t, notification)
	if status != 202 || len(out) != 0 {
		t.Fatalf("notification: %d %v", status, out)
	}

	// An SSE-only client still gets the one message it asked for.
	status, body, headers := env.raw(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`,
		"Accept", "text/event-stream", "MCP-Protocol-Version", Modern, "Mcp-Method", "tools/list")
	if status != 200 || !strings.HasPrefix(headers.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("sse response: %d %v", status, headers)
	}
	if !strings.HasPrefix(body, "event: message\ndata: {") || !strings.Contains(body, `"resultType":"complete"`) {
		t.Fatalf("sse payload: %s", body)
	}

	// Malformed JSON is a parse error, not a panic or an empty 200.
	status, out, _ = env.post(t, `{"jsonrpc":`)
	if status != 400 {
		t.Fatalf("parse error status: %d", status)
	}
	if code, _ := errorOf(t, out); code != codeParseError {
		t.Fatalf("parse error code: %v", out)
	}
}

func TestToolsReadThePanelContract(t *testing.T) {
	env := newEnv(t)

	// An unknown tool or a misspelled argument is a protocol error, so the
	// client can fix the call instead of reading it as data.
	status, out := env.modern(t, "1", "tools/call", "list_nodess", `{}`)
	if status != 200 {
		t.Fatalf("unknown tool status: %d", status)
	}
	if code, _ := errorOf(t, out); code != codeInvalidParams {
		t.Fatalf("unknown tool: %v", out)
	}
	status, out = env.modern(t, "1", "tools/call", "node_history", `{"nodeid":"n1"}`)
	if code, message := errorOf(t, out); code != codeInvalidParams || !strings.Contains(message, "nodeid") {
		t.Fatalf("misspelled argument: %d %v", status, out)
	}

	// A node that does not exist comes back as a readable tool error.
	result := invoke(t, env, "node_history", `{"node_id":"n9"}`)
	if result["isError"] != true || !strings.Contains(result["content"].([]any)[0].(map[string]any)["text"].(string), "节点不存在") {
		t.Fatalf("missing node: %v", result)
	}
	if result = invoke(t, env, "node_observations", `{}`); result["isError"] != true {
		t.Fatalf("missing node_id must fail: %v", result)
	}

	// Host history: 0 means the documented default, anything else is clamped and
	// reported back as the window actually read.
	env.source.askedHours = nil
	if payload := structured(t, invoke(t, env, "node_history", `{"node_id":"n1"}`)); payload["hours"].(float64) != 24 {
		t.Fatalf("default window: %v", payload)
	}
	if payload := structured(t, invoke(t, env, "node_history", `{"node_id":"n1","hours":9999}`)); payload["hours"].(float64) != 720 {
		t.Fatalf("clamped window: %v", payload)
	}
	if env.source.askedHours[0] != 24 || env.source.askedHours[1] != 720 {
		t.Fatalf("windows asked of storage: %v", env.source.askedHours)
	}
	if payload := structured(t, invoke(t, env, "node_network_history", `{"node_id":"n1","hours":500}`)); payload["hours"].(float64) != 168 {
		t.Fatalf("network window: %v", payload)
	}

	// Observations carry the same masking as the HTTP contract: no node
	// bindings, masked hop addresses and no route label for a stale probe.
	payload := structured(t, invoke(t, env, "node_observations", `{"node_id":"n1"}`))
	observations := payload["observations"].([]any)
	if len(observations) != 2 {
		t.Fatalf("observations: %v", observations)
	}
	fresh := observations[0].(map[string]any)
	task := fresh["task"].(map[string]any)
	if task["node_ids"] != nil {
		t.Fatalf("node bindings leaked: %v", task)
	}
	hops := fresh["result"].(map[string]any)["hops"].([]any)
	first := hops[0].(map[string]any)
	second := hops[1].(map[string]any)
	if first["address"] != "59.43.*.*" || second["address"] != "202.97.*.*" {
		t.Fatalf("hop addresses not masked: %v", hops)
	}
	if first["asn"] != "AS4134" || second["asn"] != "AS4134" {
		t.Fatalf("asn not normalised: %v", hops)
	}
	if fresh["result"].(map[string]any)["route"] != "CN2GT" {
		t.Fatalf("fresh route label: %v", fresh)
	}
	stale := observations[1].(map[string]any)
	if stale["result"].(map[string]any)["route"] != "未知" {
		t.Fatalf("stale route label: %v", stale)
	}

	// Latency samples only cover the nodes that were listed, and the limit is
	// clamped the same way the HTTP endpoint clamps it.
	if payload := structured(t, invoke(t, env, "latency_samples", `{"limit":200}`)); payload["limit"].(float64) != 60 {
		t.Fatalf("latency limit: %v", payload)
	}
	if env.source.askedLimit != 60 {
		t.Fatalf("limit asked of storage: %d", env.source.askedLimit)
	}
	if payload := structured(t, invoke(t, env, "recent_events", `{}`)); payload["count"].(float64) != 1 {
		t.Fatalf("events: %v", payload)
	}
}

func TestAdminToolsChangeTheFleet(t *testing.T) {
	env := newEnv(t)

	// create_node is the panel's node form, and the one place a node key leaves
	// the Server: the caller must see it exactly once.
	created := structured(t, invoke(t, env, "create_node",
		`{"name":"东京","billing_at":"2026-09-01T00:00:00Z","traffic_mode":"sum","region":"日本","public":true,"traffic_limit":1000000000000,"line_mbps":1000,"throttle_mbps":100,"throttle_hours":24,"throttle_on":"quota","alerts":{"throttle":true}}`))
	if created["id"] != "new-node" || created["token"] != "new-token" {
		t.Fatalf("create_node: %v", created)
	}
	want := "curl -fsSL " + env.server.URL + "/install.sh | sh -s -- --endpoint " + env.server.URL + " --token new-token"
	if created["install_command"] != want {
		t.Fatalf("install command: %v", created["install_command"])
	}
	if _, ok := created["warning"]; !ok {
		t.Fatalf("a plain-HTTP endpoint must warn like the panel does: %v", created)
	}
	if len(env.source.savedNodes) != 1 || env.source.savedNodes[0].Name != "东京" || !env.source.savedNodes[0].Public {
		t.Fatalf("stored node: %+v", env.source.savedNodes)
	}
	// 达量降速的三项线路规格、触发条件与它的告警开关都要落库（面板表单与 MCP 同一形状）。
	if n := env.source.savedNodes[0]; n.LineMbps != 1000 || n.ThrottleMbps != 100 || n.ThrottleHours != 24 ||
		n.ThrottleOn != "quota" || n.TrafficLimit != 1_000_000_000_000 || !n.Alerts.Throttle {
		t.Fatalf("throttle fields did not persist: %+v", n)
	}

	// update_node merges into the stored node: a field the caller did not mention
	// keeps its value, and an object such as alerts is merged field by field.
	env.source.savedNodes = nil
	if structured(t, invoke(t, env, "update_node", `{"node_id":"n1","public":false,"traffic_limit":1000000000000,"line_mbps":1000,"throttle_mbps":100,"throttle_on":"quota","alerts":{"offline":true}}`))["id"] != "n1" {
		t.Fatalf("update_node did not report the node")
	}
	if len(env.source.savedNodes) != 1 {
		t.Fatalf("update_node stored nothing: %+v", env.source.savedNodes)
	}
	node := env.source.savedNodes[0]
	if node.Name != "香港" || node.Region != "" || node.BillingAt != "2026-09-01T00:00:00Z" || node.TrafficMode != "sum" {
		t.Fatalf("update blanked fields it was not given: %+v", node)
	}
	if node.Public || !node.Alerts.Offline || node.Alerts.OfflineSeconds != 60 {
		t.Fatalf("update did not apply the patch: %+v", node.Alerts)
	}
	// 逐字段合并：这次提到的线路规格与触发条件写进去，没提到的（降速窗口）不被动过。
	if node.LineMbps != 1000 || node.ThrottleMbps != 100 || node.ThrottleHours != 0 || node.ThrottleOn != "quota" {
		t.Fatalf("throttle patch did not merge: %+v", node)
	}
	if _, out := env.modern(t, "1", "tools/call", "update_node", `{"node_id":"n1","pbulic":true}`); codeOf(t, out) != codeInvalidParams {
		t.Fatalf("misspelled patch: %v", out)
	}

	// The panel's 接入 button is rotate: the old key dies, the new one comes back
	// with the command.
	rotated := structured(t, invoke(t, env, "rotate_node_token", `{"node_id":"n1"}`))
	if rotated["token"] != "rotated-token" || !strings.Contains(rotated["install_command"].(string), "rotated-token") {
		t.Fatalf("rotate_node_token: %v", rotated)
	}
	if len(env.source.rotated) != 1 || env.source.rotated[0] != "n1" {
		t.Fatalf("rotated: %v", env.source.rotated)
	}

	// Traffic correction refuses a stale cycle, like the HTTP endpoint does.
	if !structured(t, invoke(t, env, "correct_node_traffic", `{"node_id":"n1","used":5000000000,"cycle":100}`))["corrected"].(bool) {
		t.Fatalf("correction did not report success")
	}
	if len(env.source.corrected) != 2 || env.source.corrected[0] != 5000000000 || env.source.corrected[1] != 100 {
		t.Fatalf("correction payload: %v", env.source.corrected)
	}
	env.source.failure = model.ErrConflict
	result := invoke(t, env, "correct_node_traffic", `{"node_id":"n1","used":1,"cycle":2}`)
	if result["isError"] != true || !strings.Contains(text(t, result), "周期已变化") {
		t.Fatalf("stale cycle: %v", result)
	}
	// Input storage rejects (an unknown id, a misspelled field) is a protocol
	// error the caller has to fix, never data it should read as a result.
	env.source.failure = model.ErrInvalid
	for _, rejected := range []struct{ tool, arguments string }{
		{"delete_node", `{"node_id":"n1"}`},
		{"control_task", `{"task_id":"t1","action":"bind","node_ids":["n9"]}`},
	} {
		if _, out := env.modern(t, "1", "tools/call", rejected.tool, rejected.arguments); codeOf(t, out) != codeInvalidParams {
			t.Fatalf("%s rejected input must be -32602: %v", rejected.tool, out)
		}
	}
	env.source.failure = nil

	// delete_node and delete_task report what was removed.
	if !structured(t, invoke(t, env, "delete_node", `{"node_id":"n2"}`))["deleted"].(bool) {
		t.Fatalf("delete_node did not report success")
	}
	if !structured(t, invoke(t, env, "delete_task", `{"task_id":"t1"}`))["deleted"].(bool) {
		t.Fatalf("delete_task did not report success")
	}
	if len(env.source.deleted) != 2 || env.source.deleted[0] != "n2" || env.source.deleted[1] != "t1" {
		t.Fatalf("deleted: %v", env.source.deleted)
	}
	if result = invoke(t, env, "delete_node", `{"node_id":"n9"}`); result["isError"] != true {
		t.Fatalf("deleting an unknown node must fail: %v", result)
	}

	// create_task takes the same definition the task editor builds.
	createdTask := structured(t, invoke(t, env, "create_task",
		`{"name":"上海联通","type":"probe","probe":{"checks":["tcp"],"targets":[{"host":"112.64.235.107","port":443,"family":"ipv4","region":"上海","carrier":"联通"}],"interval":60},"enabled":true,"node_ids":["n1"]}`))
	if createdTask["id"] != "new-task" {
		t.Fatalf("create_task: %v", createdTask)
	}
	if len(env.source.savedTasks) != 1 || env.source.savedTasks[0].Probe.Interval != 60 || env.source.savedTasks[0].NodeIDs[0] != "n1" {
		t.Fatalf("stored task: %+v", env.source.savedTasks)
	}
	// update_task merges too: only the submitted fields change.
	env.source.savedTasks = nil
	if !structured(t, invoke(t, env, "update_task", `{"task_id":"t1","enabled":false}`))["updated"].(bool) {
		t.Fatalf("update_task did not report success")
	}
	if len(env.source.savedTasks) != 1 {
		t.Fatalf("update_task stored nothing: %+v", env.source.savedTasks)
	}
	task := env.source.savedTasks[0]
	if task.ID != "t1" || task.Name != "电信回程" || task.Probe.RouteInterval != 1800 || len(task.NodeIDs) != 1 || task.Enabled {
		t.Fatalf("update_task lost fields: %+v", task)
	}

	// The task controls are the panel's pause / resume / run / bind buttons.
	if !structured(t, invoke(t, env, "control_task", `{"task_id":"t1","action":"pause"}`))["ok"].(bool) {
		t.Fatalf("control_task did not report success")
	}
	if len(env.source.controlled) != 1 || env.source.controlled[0] != "pause" {
		t.Fatalf("control: %+v", env.source.controlled)
	}
	if _, out := env.modern(t, "1", "tools/call", "control_task", `{"task_id":"t1","action":"delete"}`); codeOf(t, out) != codeInvalidParams {
		t.Fatalf("unknown action: %v", out)
	}
	env.source.failure = model.ErrConflict
	if result = invoke(t, env, "control_task", `{"task_id":"t1","action":"run"}`); result["isError"] != true || !strings.Contains(text(t, result), "未启用") {
		t.Fatalf("run on a stopped task: %v", result)
	}
	env.source.failure = nil

	// task_runs lists what each bound node last did.
	env.source.runs = []model.TaskRun{{NodeID: "n1", NodeName: "香港", Kind: "route", Status: "ok"}}
	if payload := structured(t, invoke(t, env, "task_runs", `{"task_id":"t1"}`)); payload["count"].(float64) != 1 {
		t.Fatalf("task_runs: %v", payload)
	}
	if result = invoke(t, env, "task_runs", `{"task_id":"n9"}`); result["isError"] != true {
		t.Fatalf("runs for an unknown task must fail: %v", result)
	}
}

func TestCatalogAndNotificationTools(t *testing.T) {
	env := newEnv(t)

	// The catalog is the same built-in snapshot the task editor searches.
	all := structured(t, invoke(t, env, "list_catalog", `{}`))
	total := int(all["total"].(float64))
	if total == 0 || all["count"].(float64) != all["total"].(float64) {
		t.Fatalf("unfiltered catalog: total=%v count=%v", all["total"], all["count"])
	}
	telecom := structured(t, invoke(t, env, "list_catalog", `{"carrier":"电信"}`))
	if int(telecom["count"].(float64)) == 0 || int(telecom["count"].(float64)) >= total {
		t.Fatalf("carrier filter: %v", telecom["count"])
	}
	for _, entry := range telecom["targets"].([]any) {
		if entry.(map[string]any)["carrier"] != "电信" {
			t.Fatalf("carrier filter leaked: %v", entry)
		}
	}
	narrow := structured(t, invoke(t, env, "list_catalog", `{"region":"上海","family":"ipv6"}`))
	if int(narrow["count"].(float64)) > int(telecom["count"].(float64)) {
		t.Fatalf("region and family filters did not narrow: %v", narrow["count"])
	}
	if _, out := env.modern(t, "1", "tools/call", "list_catalog", `{"carrier":"电信运营商"}`); codeOf(t, out) != codeInvalidParams {
		t.Fatalf("invalid carrier enum: %v", out)
	}

	// An unconfigured notification channel reads as none, and never echoes a
	// credential the Server holds.
	settings := structured(t, invoke(t, env, "get_settings", `{}`))
	if settings["channel"] != "none" || settings["webhook_configured"] != false || settings["telegram_configured"] != false {
		t.Fatalf("fresh settings: %v", settings)
	}
	if _, ok := settings["bot_token"]; ok {
		t.Fatalf("settings leaked a credential: %v", settings)
	}
	if settings["latency_window_minutes"] != float64(model.DefaultLatencyWindowMinutes) {
		t.Fatalf("延迟色带默认窗口: %v", settings["latency_window_minutes"])
	}
	if result := invoke(t, env, "test_notification", `{}`); result["isError"] != true || !strings.Contains(text(t, result), "请先配置") {
		t.Fatalf("test without a channel: %v", result)
	}

	// update_settings keeps the stored Telegram token when the caller leaves it
	// blank, drops the other channel's credentials, and answers with the public
	// view only.
	if _, out := env.modern(t, "1", "tools/call", "update_settings", `{"channel":"webhook"}`); codeOf(t, out) != codeInvalidParams {
		t.Fatalf("webhook channel without a URL: %v", out)
	}
	updated := structured(t, invoke(t, env, "update_settings", `{"channel":"webhook","webhook":"https://example.com/hook"}`))
	if updated["channel"] != "webhook" || updated["webhook_configured"] != true {
		t.Fatalf("update_settings: %v", updated)
	}
	if !strings.Contains(env.source.settings["notification"], "https://example.com/hook") || strings.Contains(updated["channel"].(string), "https://") {
		t.Fatalf("stored settings: %q result %v", env.source.settings["notification"], updated)
	}
	// A Telegram token has a fixed shape; build it here so no secret-looking
	// literal has to live in the test.
	token := "123456789:" + strings.Repeat("Ab1", 12)
	updated = structured(t, invoke(t, env, "update_settings", `{"channel":"telegram","bot_token":"`+token+`","chat_id":"@channel"}`))
	if updated["telegram_configured"] != true || updated["chat_id"] != "@channel" {
		t.Fatalf("telegram settings: %v", updated)
	}
	carried := structured(t, invoke(t, env, "update_settings", `{"channel":"telegram","chat_id":"-1001234567890"}`))
	if carried["telegram_configured"] != true || carried["chat_id"] != "-1001234567890" {
		t.Fatalf("telegram fields: %v", carried)
	}
	if !strings.Contains(env.source.settings["notification"], token) {
		t.Fatalf("a blank token must keep the stored one: %q", env.source.settings["notification"])
	}
	if _, out := env.modern(t, "1", "tools/call", "update_settings", `{"channel":"telegram","bot_token":"nope","chat_id":"@channel"}`); codeOf(t, out) != codeInvalidParams {
		t.Fatalf("invalid token format: %v", out)
	}

	// 面板时间窗与通知出口共用一个设置工具，但只报时间窗的调用不该动通知配置，
	// 越界值按协议错误回。
	window := structured(t, invoke(t, env, "update_settings", `{"latency_window_minutes":120}`))
	if window["latency_window_minutes"] != float64(120) || window["channel"] != "telegram" {
		t.Fatalf("只改时间窗的调用动了通知配置: %v", window)
	}
	if !strings.Contains(env.source.settings["notification"], token) {
		t.Fatalf("通知配置被清掉了: %q", env.source.settings["notification"])
	}
	if _, out := env.modern(t, "1", "tools/call", "update_settings", `{"latency_window_minutes":0}`); codeOf(t, out) != codeInvalidParams {
		t.Fatalf("越界时间窗该被拒: %v", out)
	}
	if again := structured(t, invoke(t, env, "get_settings", `{}`)); again["latency_window_minutes"] != float64(120) {
		t.Fatalf("时间窗没保存: %v", again)
	}

	// 只传想改的那一项：换 chat_id 不能把整个 Telegram 配置清成 none，空参数也不能。
	partial := structured(t, invoke(t, env, "update_settings", `{"chat_id":"@other"}`))
	if partial["channel"] != "telegram" || partial["chat_id"] != "@other" || !strings.Contains(env.source.settings["notification"], token) {
		t.Fatalf("只改 chat_id 动了别的通知配置: %v / %q", partial, env.source.settings["notification"])
	}
	if _, out := env.modern(t, "1", "tools/call", "update_settings", `{}`); codeOf(t, out) != codeInvalidParams {
		t.Fatalf("空参数该被拒: %v", out)
	}
	if !strings.Contains(env.source.settings["notification"], token) {
		t.Fatalf("空参数清掉了通知配置: %q", env.source.settings["notification"])
	}

	// 访客默认外观与面板一致可读写，非法值按协议错误回，且整次调用什么都不写。
	look := structured(t, invoke(t, env, "update_settings", `{"default_theme":"dark","default_skin":"press"}`))
	if look["default_theme"] != "dark" || look["default_skin"] != "press" || look["channel"] != "telegram" {
		t.Fatalf("默认外观: %v", look)
	}
	if _, out := env.modern(t, "1", "tools/call", "update_settings", `{"default_theme":"light","default_skin":"bogus"}`); codeOf(t, out) != codeInvalidParams {
		t.Fatalf("未知皮肤该被拒: %v", out)
	}
	if again := structured(t, invoke(t, env, "get_settings", `{}`)); again["default_theme"] != "dark" || again["default_skin"] != "press" {
		t.Fatalf("被拒的调用改了外观: %v", again)
	}

	// The explicit test sends one event and reports the channel, not the URL.
	if sent := structured(t, invoke(t, env, "test_notification", `{}`)); sent["sent"] != true || sent["channel"] != "telegram" {
		t.Fatalf("test_notification: %v", sent)
	}
	if len(env.sender.sent) != 1 || env.sender.sent[0].Kind != "test" {
		t.Fatalf("sender events: %+v", env.sender.sent)
	}

	// A Server wired without a sender says so instead of pretending to send.
	bare := httptest.NewServer(&Server{Source: env.source})
	defer bare.Close()
	bareEnv := &testEnv{server: bare}
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"test_notification","arguments":{},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`
	status, out, _ := bareEnv.post(t, body, "MCP-Protocol-Version", Modern, "Mcp-Method", "tools/call", "Mcp-Name", "test_notification")
	if status != 200 {
		t.Fatalf("bare server: %d %v", status, out)
	}
	if !strings.Contains(text(t, rpcResult(t, out)), "没有可用的通知发送器") {
		t.Fatalf("missing sender: %v", out)
	}
}
