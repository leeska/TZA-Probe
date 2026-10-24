package agent

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/leeska/TZA-Probe/internal/httpapi"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/storage"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type healthCollector func(context.Context) (model.Metrics, error)

func (f healthCollector) Collect(ctx context.Context) (model.Metrics, error) { return f(ctx) }

type healthTransport struct {
	fakeTransport
	statuses chan model.AgentStatus
	reports  atomic.Int64
}

func (f *healthTransport) Heartbeat(ctx context.Context, s model.AgentStatus) (model.ReportReply, error) {
	select {
	case f.statuses <- s:
	case <-ctx.Done():
	}
	return model.ReportReply{}, nil
}
func (f *healthTransport) Report(context.Context, model.Metrics) (model.ReportReply, error) {
	f.reports.Add(1)
	return model.ReportReply{}, nil
}

func TestHeartbeatContinuesDuringFailedOrBlockedCollection(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(map[bool]string{false: "failure", true: "blocked"}[blocked], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tr := &healthTransport{statuses: make(chan model.AgentStatus, 32)}
			collector := healthCollector(func(_ context.Context) (model.Metrics, error) {
				if blocked {
					// Ignore the per-collection deadline like a stuck OS adapter;
					// only shutdown releases this call.
					<-ctx.Done()
					return model.Metrics{}, ctx.Err()
				}
				return model.Metrics{}, collectionFailure("disk", errors.New("private path /secret/mount"))
			})
			a := Agent{Collector: collector, Transport: tr, reportInterval: time.Hour, heartbeatInterval: 5 * time.Millisecond, collectionTimeout: 20 * time.Millisecond}
			done := make(chan struct{})
			go func() { a.Run(ctx); close(done) }()
			timeout := time.NewTimer(time.Second)
			defer timeout.Stop()
			want := "collect_failed"
			if blocked {
				want = "collect_timeout"
			}
			for {
				select {
				case s := <-tr.statuses:
					if s.State != want {
						continue
					}
					b, _ := json.Marshal(s)
					if strings.Contains(string(b), "secret") {
						t.Fatal("raw error leaked")
					}
					if !blocked && s.Reason != "disk" {
						t.Fatalf("reason %+v", s)
					}
					cancel()
					select {
					case <-done:
					case <-time.After(time.Second):
						t.Fatal("agent did not stop")
					}
					if tr.reports.Load() != 0 {
						t.Fatal("failed collection fabricated report")
					}
					return
				case <-timeout.C:
					t.Fatal("no independent diagnostic heartbeat")
				}
			}
		})
	}
}

func TestHealthReportsFailureAndRecovery(t *testing.T) {
	h := initialHealth(nil)
	h.begin()
	h.collected(model.Metrics{}, nil)
	h.reported(errors.New("private endpoint"))
	s, skip := h.snapshot(time.Second)
	if skip || s.State != "report_failed" || s.Reason != "transport" {
		t.Fatalf("failure %+v %v", s, skip)
	}
	h.begin()
	s, skip = h.snapshot(time.Second)
	if skip || s.State != "report_failed" || s.Reason != "transport" {
		t.Fatalf("retry erased last failure %+v", s)
	}
	h.begin()
	h.collected(model.Metrics{}, nil)
	s, skip = h.snapshot(time.Second)
	if skip || s.State != "report_failed" {
		t.Fatalf("unacknowledged report cleared failure %+v", s)
	}
	h.reported(nil)
	s, skip = h.snapshot(time.Second)
	if !skip || s.State != "ok" || s.Reason != "" {
		t.Fatalf("recovery %+v %v", s, skip)
	}
}

func TestHeartbeatOldServerCompatibility(t *testing.T) {
	for _, code := range []int{404, 405, 401} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			var calls int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/agent/heartbeat" {
					calls++
					w.WriteHeader(code)
					return
				}
				w.WriteHeader(204)
			}))
			defer srv.Close()
			tr, err := NewTransport(srv.URL, model.Secret(), false)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				_, err = tr.Heartbeat(context.Background(), model.AgentStatus{State: "starting"})
				if (err != nil) != (code == 401) {
					t.Fatal(err)
				}
			}
			want := 1
			if code == 401 {
				want = 2
			}
			if calls != want {
				t.Fatalf("heartbeat calls %d", calls)
			}
			if _, err = tr.Report(context.Background(), model.Metrics{}); err != nil {
				t.Fatal("old reports broken", err)
			}
		})
	}
}

func TestHealthOrdersInFlightRecoveryAndNewFailures(t *testing.T) {
	h := initialHealth(nil)
	h.begin()
	h.collected(model.Metrics{}, collectionFailure("disk", errors.New("failed")))
	before, _ := h.snapshot(time.Second)
	h.begin()
	h.collected(model.Metrics{}, nil)
	var m model.Metrics
	h.prepareReport(&m)
	during, _ := h.snapshot(time.Second)
	if m.Session != before.Session || m.Sequence <= before.Sequence || during.Sequence != before.Sequence {
		t.Fatalf("in-flight recovery claimed a newer failure: before=%+v report=%+v during=%+v", before, m.AgentState, during)
	}
	h.reported(nil)
	after, _ := h.snapshot(time.Second)
	if after.Session != m.Session || after.Sequence <= m.Sequence || after.State != "ok" {
		t.Fatalf("recovery sequence: %+v", after)
	}
	h.begin()
	h.collected(model.Metrics{}, errors.New("next failure"))
	next, _ := h.snapshot(time.Second)
	if next.Sequence <= after.Sequence {
		t.Fatal("new failure did not advance state")
	}
	restart := initialHealth(nil)
	if restart.status.Session == h.status.Session {
		t.Fatal("Agent restarts must have distinct sessions")
	}
}

// Exercise the complete wire path, holding a real pre-recovery heartbeat in
// the HTTP handler until a newer valid report has committed to SQLite.
func TestDelayedHeartbeatCannotUndoRecoveryReport(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "wire.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	id, token, err := store.SaveNode(ctx, model.Node{Name: "wire", Billing: model.Billing{ResetAt: "2026-09-01T00:00:00Z", TrafficMode: "sum"}}, true)
	if err != nil {
		store.Close()
		cancel()
		t.Fatal(err)
	}
	handler := (&httpapi.API{Store: store, AdminToken: model.Secret(), Assets: http.NotFoundHandler()}).Handler()
	heartbeatSent, release, heartbeatDone, reportDone := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var sentOnce, heartbeatOnce, reportOnce, releaseOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/agent/heartbeat":
			sentOnce.Do(func() { close(heartbeatSent) })
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			handler.ServeHTTP(w, r)
			heartbeatOnce.Do(func() { close(heartbeatDone) })
		case "/api/v1/agent/report":
			handler.ServeHTTP(w, r)
			if nodes, _ := store.Nodes(r.Context(), time.Now()); len(nodes) == 1 && nodes[0].Metrics != nil {
				reportOnce.Do(func() { close(reportDone) })
			}
		default:
			handler.ServeHTTP(w, r)
		}
	}))
	transport, err := NewTransport(server.URL, token, false)
	if err != nil {
		server.Close()
		store.Close()
		cancel()
		t.Fatal(err)
	}
	var calls atomic.Int64
	collector := healthCollector(func(ctx context.Context) (model.Metrics, error) {
		switch calls.Add(1) {
		case 1:
			return model.Metrics{}, collectionFailure("disk", errors.New("failed"))
		case 2:
			select {
			case <-heartbeatSent:
			case <-ctx.Done():
				return model.Metrics{}, ctx.Err()
			}
			return model.Metrics{CPU: 50, MemoryUsed: 10, MemoryTotal: 100, DiskUsed: 10, DiskTotal: 100, Version: model.Version}, nil
		default:
			<-ctx.Done()
			return model.Metrics{}, ctx.Err()
		}
	})
	done := make(chan struct{})
	go func() {
		Agent{Collector: collector, Transport: transport, reportInterval: 20 * time.Millisecond, heartbeatInterval: 5 * time.Millisecond}.Run(ctx)
		close(done)
	}()
	defer func() { cancel(); releaseOnce.Do(func() { close(release) }); <-done; server.Close(); store.Close() }()
	wait := func(ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(3 * time.Second):
			t.Fatal("wire flow did not settle")
		}
	}
	wait(reportDone)
	fresh := func() {
		t.Helper()
		nodes, err := store.Nodes(context.Background(), time.Now())
		if err != nil || len(nodes) != 1 || nodes[0].ID != id || nodes[0].MetricsStale || nodes[0].Metrics == nil || nodes[0].AgentStatus != nil {
			t.Fatalf("recovery overwritten: %+v %v", nodes, err)
		}
	}
	fresh()
	releaseOnce.Do(func() { close(release) })
	wait(heartbeatDone)
	fresh()
}

func TestSessionChallengeRetryIsBoundedAndPreservesPayload(t *testing.T) {
	for _, path := range []string{"report", "heartbeat"} {
		for _, outcome := range []string{"accepted", "ordinary-conflict", "repeated-challenge", "malformed-challenge"} {
			t.Run(path+"/"+outcome, func(t *testing.T) {
				session, proof := model.Secret(), model.Secret()
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					var state model.AgentState
					if path == "report" {
						var body model.Report
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Error(err)
						}
						state = body.Metrics.AgentState
						if body.Metrics.CPU != 42 {
							t.Error("report payload lost")
						}
					} else {
						var body model.Heartbeat
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Error(err)
						}
						state = body.Status.AgentState
						if body.Status.Reason != "disk" {
							t.Error("heartbeat payload lost")
						}
					}
					if state.Session != session || state.Sequence != 3 {
						t.Error("session or generation changed")
					}
					if calls > 1 && state.Proof != proof {
						t.Error("challenge not echoed")
					}
					switch outcome {
					case "ordinary-conflict":
						w.WriteHeader(409)
					case "malformed-challenge":
						w.WriteHeader(409)
						json.NewEncoder(w).Encode(model.AgentSessionChallenge{Challenge: "bad"})
					default:
						if calls == 1 || outcome == "repeated-challenge" {
							w.WriteHeader(409)
							json.NewEncoder(w).Encode(model.AgentSessionChallenge{Challenge: proof})
						} else {
							w.WriteHeader(204)
						}
					}
				}))
				defer server.Close()
				tr, err := NewTransport(server.URL, model.Secret(), false)
				if err != nil {
					t.Fatal(err)
				}
				state := model.AgentState{Session: session, Sequence: 3}
				if path == "report" {
					_, err = tr.Report(context.Background(), model.Metrics{AgentState: state, CPU: 42})
				} else {
					_, err = tr.Heartbeat(context.Background(), model.AgentStatus{AgentState: state, State: "collect_failed", Reason: "disk"})
				}
				if (err == nil) != (outcome == "accepted") {
					t.Fatalf("unexpected result: %v", err)
				}
				want := 1
				if outcome == "accepted" {
					want = 2
				}
				if outcome == "repeated-challenge" {
					want = 3
				}
				if calls != want {
					t.Fatalf("requests=%d want=%d", calls, want)
				}
			})
		}
	}
}

func TestDelayedUnknownProcessCannotTakeOverHTTPNode(t *testing.T) {
	st, err := storage.Open(filepath.Join(t.TempDir(), "restart.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	id, token, err := st.SaveNode(ctx, model.Node{Name: "restart", Billing: model.Billing{ResetAt: "2026-09-01T00:00:00Z", TrafficMode: "sum"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	handler := (&httpapi.API{Store: st, AdminToken: model.Secret(), Assets: http.NotFoundHandler()}).Handler()
	arrived, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test-Old-Process") != "" {
			close(arrived)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	defer releaseOnce.Do(func() { close(release) })
	old := model.AgentStatus{AgentState: model.AgentState{Session: model.Secret(), Sequence: 2}, State: "collect_failed", Reason: "disk"}
	raw, _ := json.Marshal(model.Heartbeat{Protocol: model.Protocol, Status: old})
	req, _ := http.NewRequest("POST", server.URL+"/api/v1/agent/heartbeat", strings.NewReader(string(raw)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Test-Old-Process", "1")
	req.Header.Set("Content-Type", "application/json")
	oldDone := make(chan int, 1)
	go func() {
		// A packet already forwarded before process exit cannot respond to a challenge.
		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			oldDone <- 0
			return
		}
		resp.Body.Close()
		oldDone <- resp.StatusCode
	}()
	select {
	case <-arrived:
	case <-time.After(3 * time.Second):
		t.Fatal("old request did not arrive")
	}
	tr, err := NewTransport(server.URL, token, false)
	if err != nil {
		t.Fatal(err)
	}
	current := model.AgentState{Session: model.Secret(), Sequence: 3}
	if _, err := tr.Report(ctx, model.Metrics{AgentState: current, CPU: 42, MemoryUsed: 10, MemoryTotal: 100}); err != nil {
		t.Fatal(err)
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case code := <-oldDone:
		if code != 409 {
			t.Fatalf("old packet response=%d", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("old response timed out")
	}
	current.Sequence++
	if _, err := tr.Heartbeat(ctx, model.AgentStatus{AgentState: current, State: "ok"}); err != nil {
		t.Fatal(err)
	}
	nodes, err := st.Nodes(ctx, time.Now())
	if err != nil || len(nodes) != 1 {
		t.Fatalf("nodes: %+v %v", nodes, err)
	}
	n := nodes[0]
	if n.ID != id || n.MetricsStale || n.Metrics == nil || n.Metrics.Session != current.Session || n.Metrics.Proof != "" || n.AgentStatus == nil || n.AgentStatus.Session != current.Session || n.AgentStatus.Proof != "" {
		t.Fatalf("current process lost ownership: %+v", n)
	}
}
