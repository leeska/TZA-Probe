package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/ipguard"
)

func TestEgressTunnelLive(t *testing.T) {
	path := os.Getenv("TZA_EGRESS_LIVE_POLICY")
	if path == "" {
		t.Skip("requires approved host egress policy")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Second)
	defer cancel()
	store, err := ipguard.Open(filepath.Join(t.TempDir(), "guard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	broker := ipguard.NewBroker(store)
	credential := ipguard.ID() + ipguard.ID()[:16]
	gateway := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+credential {
			http.Error(writer, "unauthorized", 401)
			return
		}
		switch {
		case request.URL.Path == "/api/v1/agent/ipguard/status":
			var status ipguard.Status
			if json.NewDecoder(request.Body).Decode(&status) != nil {
				http.Error(writer, "bad status", 400)
				return
			}
			broker.Report("qa", status)
			writer.WriteHeader(204)
		case request.URL.Path == "/api/v1/agent/ipguard/streams":
			offer, err := broker.Poll(request.Context(), "qa")
			if err == nil {
				json.NewEncoder(writer).Encode(offer)
			}
		case strings.HasPrefix(request.URL.Path, "/api/v1/agent/ipguard/streams/"):
			if err := broker.Accept(writer, request, "qa", strings.TrimPrefix(request.URL.Path, "/api/v1/agent/ipguard/streams/"), request.Header.Get("X-Stream-Secret")); err != nil {
				http.Error(writer, "denied", 403)
			}
		default:
			http.NotFound(writer, request)
		}
	}))
	defer gateway.Close()
	transport, err := NewTransport(gateway.URL, credential, true)
	if err != nil {
		t.Fatal(err)
	}
	egress := &Egress{Transport: transport, PolicyFile: path}
	permission, err := egress.policy()
	if err != nil || !permission.Enabled || len(permission.AllowedIPs) == 0 {
		t.Fatal("local permission", permission, err)
	}
	done := make(chan struct{})
	go func() { defer close(done); egress.Run(ctx) }()
	defer func() { cancel(); <-done }()
	worker := httptest.NewServer(broker.WorkerHandler())
	defer worker.Close()
	for !broker.Status("qa").Enabled {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	policy := ipguard.Policy{Enabled: true, Country: "US", Locale: "en-US", Timezone: "UTC", Services: []string{"google"}, TimeoutSeconds: 60, NetworkBytes: 1 << 20, DwellSeconds: 2}
	for _, expected := range permission.AllowedIPs {
		t.Run(expected, func(t *testing.T) {
			policy.ExpectedIP = expected
			policy, err = store.SavePolicy(ctx, "qa", policy)
			if err != nil {
				t.Fatal(err)
			}
			queued, err := store.Enqueue(ctx, "qa", "google", "baseline", "")
			if err != nil {
				t.Fatal(err)
			}
			defer store.Cancel(ctx, queued.ID)
			job, err := store.Claim(ctx)
			if err != nil || job == nil {
				t.Fatal("claim", err)
			}
			defer broker.Forget(job.ID)
			clientTransport := &http.Transport{DisableKeepAlives: true, DialContext: func(dialContext context.Context, network, address string) (net.Conn, error) {
				if address != "api64.ipify.org:443" {
					return nil, fmt.Errorf("unexpected target %s", address)
				}
				connection, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(dialContext, "tcp", worker.Listener.Addr().String())
				if err != nil {
					return nil, err
				}
				connection.SetDeadline(time.Now().Add(25 * time.Second))
				_, err = fmt.Fprintf(connection, "CONNECT /connect/%s HTTP/1.1\r\nHost: localhost\r\nX-Job-Lease: %s\r\nX-Destination: api64.ipify.org\r\n\r\n", job.ID, job.Lease)
				if err != nil {
					connection.Close()
					return nil, err
				}
				response, err := http.ReadResponse(bufio.NewReader(connection), nil)
				if err != nil {
					connection.Close()
					return nil, err
				}
				if response.StatusCode != 200 {
					connection.Close()
					return nil, fmt.Errorf("tunnel status %d", response.StatusCode)
				}
				return connection, nil
			}}
			defer clientTransport.CloseIdleConnections()
			client := &http.Client{Transport: clientTransport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			request, _ := http.NewRequestWithContext(ctx, "GET", "https://api64.ipify.org/", nil)
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			raw, err := io.ReadAll(io.LimitReader(response.Body, 128))
			if err != nil || response.StatusCode != 200 || strings.TrimSpace(string(raw)) != expected {
				t.Fatalf("exit status=%d actual=%q expected=%s error=%v", response.StatusCode, raw, expected, err)
			}
			t.Logf("verified production tunnel source=%s public_exit=%s", permission.SourceIPs[expected], expected)
		})
	}
}
