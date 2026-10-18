package storage

import (
	"context"
	"errors"
	"github.com/leeska/TZA-Probe/internal/model"
	"testing"
	"time"
)

func TestHeartbeatSeparatesContactFromMetricsAndTraffic(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id, _ := makeNode(t, s)
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	m := metrics()
	if err := s.Report(ctx, id, m, now); err != nil {
		t.Fatal(err)
	}
	status := model.AgentStatus{State: "collect_failed", Reason: "disk", Version: "1.5.0", OS: "linux", Arch: "amd64"}
	for _, seconds := range []int{5, 60} {
		if err := s.Heartbeat(ctx, id, status, now.Add(time.Duration(seconds)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	views, err := s.Nodes(ctx, now.Add(61*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	n := views[0]
	if !n.Online || !n.MetricsStale || n.Metrics != nil || n.MetricsAt != now.Unix() || n.LastSeen != now.Unix()+60 || n.AgentStatus.Reason != "disk" || n.TrafficUsed != 0 {
		t.Fatalf("invalid diagnostic view: %+v", n)
	}
	var samples, baseline int64
	if err := s.db.QueryRow("SELECT count(*) FROM samples").Scan(&samples); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT last_seen FROM nodes WHERE id=?", id).Scan(&baseline); err != nil {
		t.Fatal(err)
	}
	if samples != 1 || baseline != now.Unix() {
		t.Fatalf("heartbeat changed samples/baseline: %d/%d", samples, baseline)
	}
	if err := s.Heartbeat(ctx, id, status, now.Add(61*time.Second)); !errors.Is(err, ErrConflict) {
		t.Fatalf("rate limit: %v", err)
	}
	// A report one second after a heartbeat is valid: it has its own baseline.
	m.NetIn += 500
	m.NetOut += 700
	m.Uptime += 61
	if err := s.Report(ctx, id, m, now.Add(61*time.Second)); err != nil {
		t.Fatal(err)
	}
	views, err = s.Nodes(ctx, now.Add(62*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	n = views[0]
	if !n.Online || n.MetricsStale || n.Metrics == nil || n.AgentStatus != nil || n.MetricsAt != now.Unix()+61 || n.TrafficUsed != 1200 {
		t.Fatalf("recovery: %+v", n)
	}
	// Within one timestamp second, transaction order still determines whether
	// a report cleared a diagnostic or a new failure followed that report.
	if err := s.Heartbeat(ctx, id, status, now.Add(61*time.Second)); err != nil {
		t.Fatal(err)
	}
	views, err = s.Nodes(ctx, now.Add(62*time.Second))
	if err != nil || !views[0].MetricsStale {
		t.Fatal("same-second failure lost", err)
	}
	if err := s.Heartbeat(ctx, id, status, now.Add(65*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := s.Report(ctx, id, m, now.Add(65*time.Second)); err != nil {
		t.Fatal(err)
	}
	views, err = s.Nodes(ctx, now.Add(66*time.Second))
	if err != nil || views[0].MetricsStale || views[0].AgentStatus != nil {
		t.Fatal("same-second recovery lost", err)
	}
	if err := s.Heartbeat(ctx, id, status, now.Add(70*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteNode(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT count(*) FROM node_health").Scan(&samples); err != nil || samples != 0 {
		t.Fatalf("orphan health %d: %v", samples, err)
	}
}

func TestFirstHeartbeatAndImmediateCollectionFailure(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id, _ := makeNode(t, s)
	now := time.Now().Truncate(time.Second)
	status := model.AgentStatus{State: "collect_failed", Reason: "interfaces"}
	if err := s.Heartbeat(ctx, id, status, now); err != nil {
		t.Fatal(err)
	}
	views, err := s.Nodes(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if !views[0].Online || !views[0].MetricsStale || views[0].MetricsAt != 0 || views[0].Metrics != nil {
		t.Fatalf("first contact %+v", views[0])
	}
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM samples").Scan(&count); err != nil || count != 0 {
		t.Fatal("fabricated samples", err)
	}
	if err := s.Report(ctx, id, metrics(), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := s.Heartbeat(ctx, id, status, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	views, err = s.Nodes(ctx, now.Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !views[0].MetricsStale || views[0].Metrics != nil {
		t.Fatal("fresh but failed metrics still advertised")
	}
	views, err = s.Nodes(ctx, now.Add(time.Minute))
	if err != nil || views[0].Online {
		t.Fatal("heartbeats prevent offline forever", err)
	}
}
