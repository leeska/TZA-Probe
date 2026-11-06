package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	runtimepkg "github.com/leeska/TZA-Probe/internal/executorruntime"
	"github.com/leeska/TZA-Probe/internal/ipsentinel"
	"github.com/leeska/TZA-Probe/internal/market"
	"github.com/leeska/TZA-Probe/internal/market/markettest"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/storage"
	"github.com/leeska/TZA-Probe/pkg/tzapkg"
	"github.com/leeska/TZA-Probe/web"
)

func TestMarketNodePlansRemainExplicitAndScoped(t *testing.T) {
	store, err := storage.Open(t.TempDir() + "/server.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	node, token, err := store.SaveNode(t.Context(), model.Node{Name: "node", Billing: model.Billing{ResetAt: "2026-11-01T00:00:00Z", TrafficMode: "sum"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	enrollment := runtimepkg.Enrollment{ScopeSHA256: strings.Repeat("a", 64), Target: ipsentinel.Target{ControllerID: ipsentinel.NewUUID(), NodeID: ipsentinel.NewUUID()}, Host: runtimepkg.Host{Platform: "linux-amd64", ProbeVersion: "1.10.0", AgentVersion: "1.10.0"}}
	if _, err := store.RuntimeExchange(t.Context(), node, runtimepkg.ChannelReport{Enrollment: enrollment}); err != nil {
		t.Fatal(err)
	}
	cc := markettest.New(t)
	cc.Add(&markettest.Package{ID: tzapkg.OfficialExecutorID, Type: tzapkg.TypeExecutor, Kind: "ipsentinel", Name: "IPSentinel", Releases: []markettest.Release{
		{Version: "0.5.0", Probe: ">=99.0.0"},
		{Version: "0.4.0", Probe: ">=1.6.0"},
	}})
	client, err := market.New(market.Config{URL: cc.URL(), Keys: cc.Keys(), Dir: t.TempDir(), Probe: "1.10.0"})
	if err != nil {
		t.Fatal(err)
	}
	admin := model.Secret()
	handler := (&API{Store: store, AdminToken: admin, Market: client, Assets: web.Handler()}).Handler()
	body := map[string]string{"id": tzapkg.OfficialExecutorID, "version": "0.4.0", "node_id": node, "scope_sha256": enrollment.ScopeSHA256}
	call := func(auth, origin string, input map[string]string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(input)
		request := httptest.NewRequest(http.MethodPost, "https://probe.example/api/v1/admin/market/node-plans", bytes.NewReader(raw))
		request.Header.Set("Content-Type", "application/json")
		if auth != "" {
			request.Header.Set("Authorization", "Bearer "+auth)
		}
		if origin != "" {
			request.Header.Set("Origin", origin)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	for _, auth := range []string{"", token} {
		if response := call(auth, "", body); response.Code != http.StatusUnauthorized {
			t.Fatal("non-admin created a plan", response.Code)
		}
	}
	if response := call(admin, "https://attacker.example", body); response.Code != http.StatusForbidden {
		t.Fatal("cross-origin plan accepted", response.Code)
	}
	for _, test := range []struct {
		key, value string
		status     int
	}{
		{"id", "third-party-executor", 400},
		{"version", "latest", 400},
		{"version", "0.5.0", 400},
		{"version", "0.6.0", 400},
		{"scope_sha256", strings.Repeat("b", 64), 409},
		{"node_id", "missing", 404},
		{"command", "shell", 400},
	} {
		input := map[string]string{}
		for key, value := range body {
			input[key] = value
		}
		input[test.key] = test.value
		if response := call(admin, "", input); response.Code != test.status {
			t.Fatalf("%s=%s: %d %s", test.key, test.value, response.Code, response.Body.String())
		}
	}
	response := call(admin, "", body)
	if response.Code != http.StatusAccepted {
		t.Fatal(response.Code, response.Body.String())
	}
	var plan runtimepkg.ChannelRequest
	if err := json.Unmarshal(response.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.State != "requested" || plan.Action != "stage" || plan.ApprovedAt != 0 || plan.Enrollment != enrollment {
		t.Fatal("plan did not preserve approval boundary", plan)
	}
	if response := call(admin, "", body); response.Code != http.StatusConflict {
		t.Fatal("duplicate request accepted", response.Code)
	}
	reply, err := store.RuntimeExchange(t.Context(), node, runtimepkg.ChannelReport{Enrollment: enrollment})
	if err != nil || reply.Request == nil || reply.Request.ID != plan.ID {
		t.Fatal("agent cannot retrieve market plan", reply, err)
	}
	cc.Lock()
	defer cc.Unlock()
	for _, request := range cc.Seen {
		if strings.Contains(request.Path, "/download") || strings.Contains(request.Path, "/artifacts/") {
			t.Fatal("plan creation downloaded runtime", request.Path)
		}
	}
}
