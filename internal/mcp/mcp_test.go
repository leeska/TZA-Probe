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
func (f *fakeSource) Events(context.Context, bool, time.Time) ([]model.Event, error) {
	return f.events, nil
}
func (f *fakeSource) RecentLatencySamples(_ context.Context, ids []string, limit int) ([]model.LatencySample, error) {
	f.askedLimit = limit
	if len(ids) == 0 {
		return []model.LatencySample{}, nil
	}
	return f.samples, nil
}

func fixture() *fakeSource {
	return &fakeSource{
		nodes: []model.NodeView{
			{Node: model.Node{ID: "n1", Name: "香港", Public: true}, Online: true, TrafficUsed: 1234},
			{Node: model.Node{ID: "n2", Name: "东京"}, Online: false},
		},
		tasks:   []model.Task{{ID: "t1", Name: "电信回程", Kind: "route", Enabled: true, NodeIDs: []string{"n1"}}},
		history: []model.Sample{{At: 1, Metrics: model.Metrics{CPU: 12, MemoryTotal: 100}}},
		series: []model.NetworkSeries{{
			Name: "电信回程", Family: "ipv4", Host: "1.1.1.1", Port: 443,
			Samples: []model.NetworkSample{{At: 1}},
		}},
		observations: []model.Observation{
			{
				Task: model.Task{ID: "t1", Kind: "route", Host: "1.1.1.1", NodeIDs: []string{"n1"}},
				At:   2,
				Result: model.Result{
					Status: "ok", Route: "CN2GT",
					Hops: []model.Hop{{TTL: 1, Address: "59.43.1.2", ASN: "4134"}, {TTL: 2, Address: "202.97.5.6", ASN: "as4134"}},
				},
			},
			{
				Task:   model.Task{ID: "t1", Kind: "route", Host: "1.1.1.1"},
				At:     1,
				Stale:  true,
				Result: model.Result{Status: "ok", Route: "CN2GIA"},
			},
		},
		events:  []model.Event{{ID: 7, NodeID: "n1", NodeName: "香港", Kind: "offline", Message: "离线"}},
		samples: []model.LatencySample{{NodeID: "n1", Carrier: "电信", Family: "ipv4", At: 3, Sent: 3, Received: 3}},
	}
}

type testEnv struct {
	server *httptest.Server
	source *fakeSource
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	source := fixture()
	server := httptest.NewServer(&Server{Source: source, Version: "2.0.0-test"})
	t.Cleanup(server.Close)
	return &testEnv{server: server, source: source}
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
	want := []string{"list_nodes", "list_tasks", "node_history", "node_network_history", "node_observations", "recent_events", "latency_samples"}
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
	if entries, _ := result["tools"].([]any); len(entries) != 7 {
		t.Fatalf("legacy tool count: %v", entries)
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
