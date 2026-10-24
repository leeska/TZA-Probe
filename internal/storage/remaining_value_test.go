package storage

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

func TestRemainingValueUsesCurrentTimeAndSurvivesLiveView(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	_, _, err := s.SaveNode(ctx, model.Node{Name: "value", Public: true, Billing: model.Billing{ResetAt: "2026-10-01T00:00:00Z", TrafficMode: "sum", Price: 30, Currency: "USD", PricePeriod: "月", ExpiresAt: now.Add(15 * 24 * time.Hour).Format(time.RFC3339)}}, true)
	if err != nil {
		t.Fatal(err)
	}
	for days, want := range map[int]float64{0: 15, 1: 14, 15: 0, 16: 0} {
		nodes, err := s.Nodes(ctx, now.Add(time.Duration(days)*24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		n := nodes[0]
		if n.RemainingValue == nil || *n.RemainingValue != want {
			t.Fatalf("day=%d value=%v want=%g", days, n.RemainingValue, want)
		}
		live := n.Live()
		if live.RemainingValue == nil || *live.RemainingValue != want {
			t.Fatal("live response lost remaining value")
		}
		raw, _ := json.Marshal(n.Redact())
		var public map[string]any
		json.Unmarshal(raw, &public)
		if public["remaining_value"] != want {
			t.Fatalf("public value lost: %s", raw)
		}
	}
}
