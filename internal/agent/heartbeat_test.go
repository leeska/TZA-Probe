package agent

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/leeska/TZA-Probe/internal/model"
	"net/http"
	"net/http/httptest"
	"strings"
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
