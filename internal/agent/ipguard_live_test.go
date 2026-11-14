package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leeska/TZA-ControlCenter/internal/ipguard"
)

func TestCentralBrowserLive(t *testing.T) {
	directory := os.Getenv("TZA_CENTRAL_QA_DIR")
	expectedIP := os.Getenv("TZA_CENTRAL_QA_IP")
	if directory == "" || expectedIP == "" {
		t.Skip("requires isolated Linux browser service and approved QA exit")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 170*time.Second)
	defer cancel()
	store, err := ipguard.Open(filepath.Join(directory, "guard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	broker := ipguard.NewBroker(store)
	credential := ipguard.ID() + ipguard.ID()[:16]
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+credential {
			http.Error(writer, "unauthorized", 401)
			return
		}
		switch {
		case request.URL.Path == "/api/v1/agent/ipguard/status":
			var status ipguard.Status
			if json.NewDecoder(request.Body).Decode(&status) != nil {
				http.Error(writer, "bad request", 400)
				return
			}
			broker.Report("qa-node", status)
			writer.WriteHeader(204)
		case request.URL.Path == "/api/v1/agent/ipguard/streams":
			offer, err := broker.Poll(request.Context(), "qa-node")
			if err != nil {
				return
			}
			json.NewEncoder(writer).Encode(offer)
		case strings.HasPrefix(request.URL.Path, "/api/v1/agent/ipguard/streams/"):
			if err := broker.Accept(writer, request, "qa-node", strings.TrimPrefix(request.URL.Path, "/api/v1/agent/ipguard/streams/"), request.Header.Get("X-Stream-Secret")); err != nil {
				http.Error(writer, "denied", 403)
			}
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	transport, err := NewTransport(server.URL, credential, true)
	if err != nil {
		t.Fatal(err)
	}
	permission := EgressPolicy{Enabled: true, AllowedIPs: []string{expectedIP}, Services: []string{"google"}}
	raw, _ := json.Marshal(permission)
	policyFile := filepath.Join(directory, "egress.json")
	if err := os.WriteFile(policyFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
	egress := &Egress{Transport: transport, PolicyFile: policyFile}
	done := make(chan struct{})
	go func() { defer close(done); egress.Run(ctx) }()
	defer func() { cancel(); <-done }()
	workerDone := make(chan error, 1)
	go func() { workerDone <- broker.ServeWorker(ctx, filepath.Join(directory, "socket", "control.sock")) }()
	policy := ipguard.Policy{Enabled: true, ExpectedIP: expectedIP, Country: "US", Locale: "en-US", Timezone: "UTC", Services: []string{"google"}, TimeoutSeconds: 120, NetworkBytes: 20 << 20, DwellSeconds: 2}
	if _, err := store.SavePolicy(ctx, "qa-node", policy); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Enqueue(ctx, "qa-node", "google", "baseline", ""); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("browser QA timed out")
		case err := <-workerDone:
			t.Fatalf("worker listener stopped: %v", err)
		case <-ticker.C:
			jobs, err := store.Jobs(ctx, "qa-node")
			if err != nil {
				t.Fatal(err)
			}
			if len(jobs) != 1 || ipguard.Active(jobs[0].State) {
				continue
			}
			raw, _ := json.MarshalIndent(jobs[0], "", "  ")
			if err := os.WriteFile(filepath.Join(directory, "evidence.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			result := jobs[0].Result
			if result == nil || result.Execution != "completed" || !result.Sandbox || !result.NetworkIsolated || result.EgressBefore != expectedIP || result.EgressAfter != expectedIP || result.NetworkBytes <= 0 {
				t.Fatalf("invalid live evidence: %s", raw)
			}
			t.Logf("isolated browser through Agent: %s", raw)
			return
		}
	}
}
