package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/storage"
)

// billingAt keeps the old "reset on the 1st at 00:00 UTC" behaviour for a test
// running at now, without pinning the test to a calendar date.
func billingAt(now time.Time) string {
	return time.Date(now.UTC().Year(), now.UTC().Month(), 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
}

func TestDebounceDedupRecoveryAndDelivery(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(filepath.Join(t.TempDir(), "alerts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	n := model.Node{Name: "test", BillingAt: billingAt(now), TrafficMode: "sum", Alerts: model.Alerts{Load: true, CPUPercent: 80, MemoryPercent: 80, HoldSeconds: 30}}
	token, err := s.SaveNode(ctx, n, true)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := s.Authenticate(ctx, token)
	m := model.Metrics{CPU: 90, MemoryUsed: 10, MemoryTotal: 100}
	if err = s.Report(ctx, id, m, now); err != nil {
		t.Fatal(err)
	}
	count := 0
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { count++; w.WriteHeader(204) }))
	defer receiver.Close()
	if err = s.SetSetting(ctx, "webhook", receiver.URL); err != nil {
		t.Fatal(err)
	}
	engine := Engine{Repository: s, Sender: Webhook{}}
	for _, seconds := range []int{0, 15, 30, 35} {
		if err = engine.Tick(ctx, now.Add(time.Duration(seconds)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	events, err := s.Events(ctx, false, now.Add(time.Minute))
	if err != nil || len(events) != 1 || count != 1 || !events[0].Active || !events[0].Delivered {
		t.Fatal(events, count, err)
	}
	m.CPU = 10
	if err = s.Report(ctx, id, m, now.Add(40*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = engine.Tick(ctx, now.Add(40*time.Second)); err != nil {
		t.Fatal(err)
	}
	events, _ = s.Events(ctx, false, now.Add(time.Minute))
	if len(events) != 2 || events[0].Active || count != 2 {
		t.Fatal("recovery not delivered exactly once", events, count)
	}
}
func TestWebhookDoesNotFollowRedirect(t *testing.T) {
	called := false
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer dest.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, dest.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	if err := (Webhook{}).Send(context.Background(), source.URL, model.Event{}); err == nil || called {
		t.Fatal("redirect leaked notification")
	}
}

func TestRenewalTrafficOfflineRulesHaveIndependentSwitches(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(filepath.Join(t.TempDir(), "rules.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	node := model.Node{Name: "billing", BillingAt: billingAt(now), TrafficMode: "sum", TrafficLimit: 1000, ExpiresAt: now.Add(2 * 24 * time.Hour).UTC().Format(time.RFC3339), Alerts: model.Alerts{Offline: true, Renewal: true, RenewalDays: 7, Traffic: true, TrafficPercent: 80}}
	token, err := s.SaveNode(ctx, node, true)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := s.Authenticate(ctx, token)
	m := model.Metrics{MemoryTotal: 100, NetIn: 100, Uptime: 100}
	if err = s.Report(ctx, id, m, now); err != nil {
		t.Fatal(err)
	}
	m.NetIn += 900
	m.Uptime += 5
	if err = s.Report(ctx, id, m, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	engine := Engine{Repository: s, Sender: Webhook{}}
	if err = engine.Tick(ctx, now.Add(60*time.Second)); err != nil {
		t.Fatal(err)
	}
	events, err := s.Events(ctx, false, now.Add(time.Minute))
	if err != nil || len(events) != 3 {
		t.Fatalf("expected renewal, traffic, offline: %+v %v", events, err)
	}
	views, _ := s.Nodes(ctx, now)
	node = views[0].Node
	node.Alerts = model.Alerts{}
	if _, err = s.SaveNode(ctx, node, false); err != nil {
		t.Fatal(err)
	}
	if err = engine.Tick(ctx, now.Add(75*time.Second)); err != nil {
		t.Fatal(err)
	}
	events, _ = s.Events(ctx, false, now.Add(2*time.Minute))
	if len(events) != 6 {
		t.Fatal("disabled rules did not clear state", events)
	}
}

func TestOfflineGraceAndRecovery(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(filepath.Join(t.TempDir(), "offline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	token, err := s.SaveNode(ctx, model.Node{Name: "grace", BillingAt: billingAt(time.Now()), TrafficMode: "sum", Alerts: model.Alerts{Offline: true, OfflineSeconds: 60}}, true)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	m := model.Metrics{MemoryTotal: 100}
	if err = s.Report(ctx, id, m, now); err != nil {
		t.Fatal(err)
	}
	engine := Engine{Repository: s}
	for _, sec := range []int{60, 90, 119} {
		if err = engine.Tick(ctx, now.Add(time.Duration(sec)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	events, _ := s.Events(ctx, false, now)
	if len(events) != 0 {
		t.Fatal("alert during grace")
	}
	for _, sec := range []int{120, 135} {
		if err = engine.Tick(ctx, now.Add(time.Duration(sec)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	events, _ = s.Events(ctx, false, now)
	if len(events) != 1 || !events[0].Active {
		t.Fatal(events)
	}
	if err = s.Report(ctx, id, m, now.Add(140*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = engine.Tick(ctx, now.Add(140*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = engine.Tick(ctx, now.Add(150*time.Second)); err != nil {
		t.Fatal(err)
	}
	events, _ = s.Events(ctx, false, now)
	if len(events) != 2 || events[0].Active {
		t.Fatal("recovery must occur once", events)
	}
}
