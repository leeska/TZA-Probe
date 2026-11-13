package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/leeska/TZA-Probe/internal/ipguard"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/module"
	"github.com/leeska/TZA-Probe/internal/regiondrift"
	"github.com/leeska/TZA-Probe/internal/storage"
)

func TestCentralAPIAuthenticationAndLegacyRemoval(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	guard, err := ipguard.Open(filepath.Join(t.TempDir(), "guard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	ctx := context.Background()
	node, token, err := store.SaveNode(ctx, model.Node{Name: "exit", Billing: model.Billing{ResetAt: "2026-11-01T00:00:00Z", TrafficMode: "sum"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	admin := model.Secret()
	broker := ipguard.NewBroker(guard)
	handler := (&API{Store: store, AdminToken: admin, Modules: []module.Module{&regiondrift.Module{Broker: broker, Nodes: store}}, Assets: http.NotFoundHandler()}).Handler()
	call := func(method, path, key, origin string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		request := httptest.NewRequest(method, "http://localhost/api/v1"+path, bytes.NewReader(raw))
		request.Header.Set("Content-Type", "application/json")
		if key != "" {
			request.Header.Set("Authorization", "Bearer "+key)
		}
		if origin != "" {
			request.Header.Set("Origin", origin)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	for _, key := range []string{"", token} {
		if result := call("GET", "/admin/ipguard", key, "", nil); result.Code != 401 {
			t.Fatalf("admin disclosure %d", result.Code)
		}
	}
	if result := call("POST", "/agent/ipguard/status", admin, "", ipguard.Status{Enabled: true}); result.Code != 401 {
		t.Fatal("admin impersonated Agent", result.Code)
	}
	if result := call("POST", "/agent/ipguard/status", token, "", ipguard.Status{Enabled: true, ReceivedAt: 1}); result.Code != 204 {
		t.Fatal(result.Code, result.Body)
	}
	if broker.Status(node).ReceivedAt < time.Now().Add(-time.Second).Unix() {
		t.Fatal("Agent supplied clock accepted")
	}
	policy := ipguard.Policy{Enabled: true, ExpectedIP: "8.8.8.8", Country: "US", Locale: "en-US", Timezone: "UTC", Services: []string{"google"}, TimeoutSeconds: 60, NetworkBytes: 1 << 20, DwellSeconds: 2}
	path := "/admin/ipguard/node-settings/" + node
	broker.Report(node, ipguard.Status{Enabled: true, AllowedIPs: []string{"8.8.8.8"}})
	settings := ipguard.NodeSettings{Family: "ipv4", Country: policy.Country}
	if result := call("PUT", path, admin, "https://attacker.example", settings); result.Code != 403 {
		t.Fatal("cross-origin mutation", result.Code)
	}
	if result := call("PUT", path, admin, "", settings); result.Code != 200 {
		t.Fatal(result.Code, result.Body)
	}
	task := ipguard.Task{Name: "美国", Enabled: true, Services: policy.Services, NodeIDs: []string{node}}
	if result := call("POST", "/admin/ipguard/tasks", admin, "", task); result.Code != 201 {
		t.Fatal(result.Code, result.Body)
	}
	if result := call("POST", "/admin/ipguard/tasks", admin, "", ipguard.Task{Name: "未知节点", Services: policy.Services, NodeIDs: []string{"nope"}}); result.Code != 400 {
		t.Fatal("unknown node accepted", result.Code)
	}
	policies, _ := guard.Policies(ctx)
	if policies[node].Timezone != "America/New_York" || policies[node].Locale != "en-US" || policies[node].ExpectedIP != "8.8.8.8" || !policies[node].Enabled {
		t.Fatal("environment or exit not derived", policies[node])
	}
	settings.Family = "ipv6"
	if result := call("PUT", path, admin, "", settings); result.Code != 200 || !strings.Contains(result.Body.String(), "尚未开通") {
		t.Fatal("unauthorized family must be reported as pending", result.Code, result.Body)
	}
	if result := call("POST", "/admin/ipguard/jobs", admin, "", map[string]string{"node_id": node, "service": "google"}); result.Code != 409 {
		t.Fatal("offline browser admitted job", result.Code)
	}
	for _, path := range []string{"/admin/executors", "/admin/runtime", "/agent/runtime", "/admin/market/node-plans", "/agent/component-workflows/v1/capabilities", "/admin/nodes/" + node + "/executors/ipsentinel/schedule", "/agent/executors/ipsentinel"} {
		if result := call("GET", path, admin, "", nil); result.Code != 404 {
			t.Fatal("old executor exposed", path, result.Code)
		}
	}
	if result := call("POST", "/install/identity", token, "", map[string]string{"token": token}); result.Code != 404 {
		t.Fatal("removed installer route", result.Code)
	}
	if result := call("POST", "/admin/ipguard/jobs", admin, "", map[string]string{"node_id": node, "service": "google", "phase": "experiment"}); result.Code != 400 {
		t.Fatal("manual phase retained", result.Code)
	}
	if result := call("PUT", path, admin, "", policy); result.Code != 400 {
		t.Fatal("resource knobs retained", result.Code)
	}
	for _, old := range []string{"PUT", "DELETE"} {
		if result := call(old, "/admin/ipguard/nodes/"+node, admin, "", settings); result.Code != 404 && result.Code != 405 {
			t.Fatal("per-node policy editing removed", old, result.Code)
		}
	}
}

func TestCentralWebsocketSurvivesCompressionHeaders(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	guard, err := ipguard.Open(filepath.Join(t.TempDir(), "guard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	node, token, err := store.SaveNode(t.Context(), model.Node{Name: "exit", Billing: model.Billing{ResetAt: "2026-11-01T00:00:00Z", TrafficMode: "sum"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	policy := ipguard.Policy{Enabled: true, ExpectedIP: "8.8.8.8", Country: "US", Locale: "en-US", Timezone: "UTC", Services: []string{"google"}, TimeoutSeconds: 60, NetworkBytes: 1 << 20, DwellSeconds: 2}
	if _, err := guard.SavePolicy(t.Context(), node, policy); err != nil {
		t.Fatal(err)
	}
	if _, err := guard.Enqueue(t.Context(), node, "google", "baseline", ""); err != nil {
		t.Fatal(err)
	}
	job, err := guard.Claim(t.Context())
	if err != nil || job == nil {
		t.Fatal(err)
	}
	broker := ipguard.NewBroker(guard)
	broker.Report(node, ipguard.Status{Enabled: true})
	server := httptest.NewServer((&API{Store: store, AdminToken: model.Secret(), Modules: []module.Module{&regiondrift.Module{Broker: broker, Nodes: store}}, Assets: http.NotFoundHandler()}).Handler())
	defer server.Close()
	connected := make(chan *websocket.Conn, 1)
	go func() {
		connection, err := broker.Dial(t.Context(), *job, "www.google.com")
		if err != nil {
			t.Error(err)
			connected <- nil
			return
		}
		connected <- connection
	}()
	offer, err := broker.Poll(t.Context(), node)
	if err != nil || offer == nil {
		t.Fatal(err)
	}
	header := http.Header{"Authorization": []string{"Bearer " + token}, "X-Stream-Secret": []string{offer.Secret}, "Accept-Encoding": []string{"gzip"}}
	connection, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/v1/agent/ipguard/streams/"+offer.ID, header)
	if err != nil {
		t.Fatalf("upgrade through API: %v %v", err, response)
	}
	defer connection.Close()
	remote := <-connected
	if remote == nil {
		t.Fatal("missing remote")
	}
	defer remote.Close()
	if response.Header.Get("Content-Encoding") != "" {
		t.Fatal("compressed upgraded stream")
	}
}
