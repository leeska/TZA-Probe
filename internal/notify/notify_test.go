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
	n := model.Node{Name: "test", Alerts: model.Alerts{{Kind: model.AlertCPU, Threshold: 80, HoldSeconds: 30}, {Kind: model.AlertMemory, Threshold: 80, HoldSeconds: 30}}, Billing: model.Billing{ResetAt: billingAt(now), TrafficMode: "sum"}}
	_, token, err := s.SaveNode(ctx, n, true)
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
	events, err := s.Events(ctx, model.EventQuery{})
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
	events, _ = s.Events(ctx, model.EventQuery{})
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
	node := model.Node{Name: "billing", Alerts: model.Alerts{{Kind: model.AlertOffline}, {Kind: model.AlertRenewal, Threshold: 7}, {Kind: model.AlertTraffic, Threshold: 80}}, Billing: model.Billing{ResetAt: billingAt(now), TrafficMode: "sum", TrafficLimit: 1000, ExpiresAt: now.Add(2 * 24 * time.Hour).UTC().Format(time.RFC3339)}}
	_, token, err := s.SaveNode(ctx, node, true)
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
	events, err := s.Events(ctx, model.EventQuery{})
	if err != nil || len(events) != 3 {
		t.Fatalf("expected renewal, traffic, offline: %+v %v", events, err)
	}
	views, _ := s.Nodes(ctx, now)
	node = views[0].Node
	node.Alerts = model.Alerts{}
	if _, _, err = s.SaveNode(ctx, node, false); err != nil {
		t.Fatal(err)
	}
	if err = engine.Tick(ctx, now.Add(75*time.Second)); err != nil {
		t.Fatal(err)
	}
	events, _ = s.Events(ctx, model.EventQuery{})
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
	_, token, err := s.SaveNode(ctx, model.Node{Name: "grace", Alerts: model.Alerts{{Kind: model.AlertOffline, HoldSeconds: 60}}, Billing: model.Billing{ResetAt: billingAt(time.Now()), TrafficMode: "sum"}}, true)
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
	events, _ := s.Events(ctx, model.EventQuery{})
	if len(events) != 0 {
		t.Fatal("alert during grace")
	}
	for _, sec := range []int{120, 135} {
		if err = engine.Tick(ctx, now.Add(time.Duration(sec)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	events, _ = s.Events(ctx, model.EventQuery{})
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
	events, _ = s.Events(ctx, model.EventQuery{})
	if len(events) != 2 || events[0].Active {
		t.Fatal("recovery must occur once", events)
	}
}

// Each rule carries its own repeat interval, and CPU and memory are separate
// rules: here both fire, then only CPU (repeat 300s) is re-sent while memory
// (repeat 0) is reported once.
func TestPerRuleRepeatAndSplitLoad(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(filepath.Join(t.TempDir(), "repeat.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	node := model.Node{Name: "busy", Billing: model.Billing{ResetAt: billingAt(now), TrafficMode: "sum"}, Alerts: model.Alerts{
		{Kind: model.AlertCPU, Threshold: 80, HoldSeconds: 30, RepeatSeconds: 300},
		{Kind: model.AlertMemory, Threshold: 50, HoldSeconds: 30},
	}}
	_, token, err := s.SaveNode(ctx, node, true)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := s.Authenticate(ctx, token)
	engine := Engine{Repository: s}
	count := func(kind string) int {
		events, _ := s.Events(ctx, model.EventQuery{Kind: kind})
		return len(events)
	}
	for _, seconds := range []int{0, 30, 200, 330} {
		at := now.Add(time.Duration(seconds) * time.Second)
		if err = s.Report(ctx, id, model.Metrics{CPU: 90, MemoryUsed: 60, MemoryTotal: 100}, at); err != nil {
			t.Fatal(err)
		}
		if err = engine.Tick(ctx, at); err != nil {
			t.Fatal(err)
		}
		if seconds == 30 && (count(model.AlertCPU) != 1 || count(model.AlertMemory) != 1) {
			t.Fatalf("after hold: cpu=%d memory=%d", count(model.AlertCPU), count(model.AlertMemory))
		}
	}
	if count(model.AlertCPU) != 2 || count(model.AlertMemory) != 1 {
		t.Fatalf("repeat: cpu=%d (want 2) memory=%d (want 1)", count(model.AlertCPU), count(model.AlertMemory))
	}
}
