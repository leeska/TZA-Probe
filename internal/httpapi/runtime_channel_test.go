package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/leeska/TZA-Probe/internal/agent"
	runtimepkg "github.com/leeska/TZA-Probe/internal/executorruntime"
	"github.com/leeska/TZA-Probe/internal/ipsentinel"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/storage"
	"github.com/leeska/TZA-Probe/web"
)

func TestRuntimeChannelAPIAndAgentApproval(t *testing.T) {
	store, err := storage.Open(t.TempDir() + "/server.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	node, token, err := store.SaveNode(t.Context(), model.Node{Name: "runtime", Billing: model.Billing{ResetAt: "2026-11-01T00:00:00Z", TrafficMode: "sum"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	admin := model.Secret()
	server := httptest.NewServer((&API{Store: store, AdminToken: admin, Assets: web.Handler()}).Handler())
	defer server.Close()
	enrollment := runtimepkg.Enrollment{ScopeSHA256: string(bytes.Repeat([]byte{'a'}, 64)), Target: ipsentinel.Target{ControllerID: ipsentinel.NewUUID(), NodeID: ipsentinel.NewUUID()}, Host: runtimepkg.Host{Platform: "linux-amd64", ProbeVersion: "1.9.2", AgentVersion: "1.9.2"}}
	report := runtimepkg.ChannelReport{Enrollment: enrollment}
	raw, _ := json.Marshal(report)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agent/runtime", bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Config.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatal("agent runtime exchange lacks authentication", response.Code)
	}
	transport, err := agent.NewTransport(server.URL, token, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.RuntimeExchange(t.Context(), report); err != nil {
		t.Fatal(err)
	}
	call := func(method, path string, body any, origin string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		request := httptest.NewRequest(method, "/api/v1"+path, bytes.NewReader(raw))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+admin)
		if origin != "" {
			request.Header.Set("Origin", origin)
		}
		response := httptest.NewRecorder()
		server.Config.Handler.ServeHTTP(response, request)
		return response
	}
	if response := call("POST", "/admin/nodes/"+node+"/runtime/requests", runtimepkg.ChannelInput{Action: "stage", Version: "0.4.0"}, "https://untrusted.test"); response.Code != http.StatusForbidden {
		t.Fatal("runtime creation accepted a cross-origin mutation", response.Code)
	}
	response = call("POST", "/admin/nodes/"+node+"/runtime/requests", runtimepkg.ChannelInput{Action: "stage", Version: "0.4.0"}, "")
	if response.Code != http.StatusAccepted {
		t.Fatal(response.Code, response.Body.String())
	}
	var operation runtimepkg.ChannelRequest
	if err := json.Unmarshal(response.Body.Bytes(), &operation); err != nil {
		t.Fatal(err)
	}
	reply, err := transport.RuntimeExchange(t.Context(), report)
	if err != nil || reply.Request == nil || reply.Request.ID != operation.ID {
		t.Fatal(reply, err)
	}
	plan := runtimepkg.Plan{FormatVersion: 1, Target: enrollment.Target, Package: "ipsentinel-runtime", Version: "0.4.0", ArchiveSHA256: string(bytes.Repeat([]byte{'b'}, 64)), DescriptorSHA256: string(bytes.Repeat([]byte{'c'}, 64)), SigningKeyID: "test", Host: enrollment.Host, Permissions: []string{"network.public_https", "browser.execute"}, Artifacts: []runtimepkg.Artifact{{ID: "runtime", Kind: "runtime", Version: "0.4.0", SHA256: string(bytes.Repeat([]byte{'d'}, 64)), SizeBytes: 1024}}, TotalBytes: 1024}
	digest, _ := ipsentinel.CanonicalSHA256(plan)
	review := runtimepkg.Review{Plan: plan, PlanSHA256: digest, ApprovalTemplate: runtimepkg.Approval{FormatVersion: 1, PlanSHA256: digest}}
	planResult := runtimepkg.ChannelResult{RequestID: operation.ID, State: "awaiting_approval", PlanSHA256: digest, Review: &review}
	if _, err := transport.RuntimeExchange(t.Context(), runtimepkg.ChannelReport{Enrollment: enrollment, Result: &planResult}); err != nil {
		t.Fatal(err)
	}
	if response := call("PUT", "/admin/nodes/"+node+"/runtime/requests/"+operation.ID, runtimepkg.ChannelApproval{Approved: true, PlanSHA256: digest}, ""); response.Code != http.StatusNoContent {
		t.Fatal(response.Code, response.Body.String())
	}
	approved, err := transport.RuntimeExchange(t.Context(), report)
	if err != nil || approved.Request == nil || approved.Request.State != "approved" {
		t.Fatal(approved, err)
	}
	viewsResponse := call("GET", "/admin/runtime", nil, "")
	if viewsResponse.Code != http.StatusOK {
		t.Fatal(viewsResponse.Code)
	}
	var views []runtimepkg.ChannelView
	if err := json.Unmarshal(viewsResponse.Body.Bytes(), &views); err != nil || len(views) != 1 || views[0].Requests[0].State != "approved" {
		t.Fatal(views, err)
	}
}
