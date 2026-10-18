package notify

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/storage"
)

func TestResourceAlertsPauseAcrossUnavailableMetricsAndRestart(t *testing.T) {
	for _, kind := range []string{model.AlertCPU, model.AlertMemory} {
		for _, state := range []string{"offline", "collect_failed", "collect_timeout", "report_failed"} {
			t.Run(kind+"/"+state, func(t *testing.T) {
				ctx := context.Background()
				path := filepath.Join(t.TempDir(), "alerts.db")
				s, err := storage.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { s.Close() })
				now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
				addRules(t, s, model.AlertRule{Kind: kind, Threshold: 90, HoldSeconds: 30, RepeatSeconds: 300})
				id, _, err := s.SaveNode(ctx, model.Node{Name: "resource", Billing: model.Billing{ResetAt: billingAt(now), TrafficMode: "sum"}}, true)
				if err != nil {
					t.Fatal(err)
				}
				engine := Engine{Repository: s}
				m := model.Metrics{CPU: 95, MemoryUsed: 95, MemoryTotal: 100}
				report := func(sec int) {
					t.Helper()
					at := now.Add(time.Duration(sec) * time.Second)
					if err := s.Report(ctx, id, m, at); err != nil {
						t.Fatal(err)
					}
					if err := engine.Tick(ctx, at); err != nil {
						t.Fatal(err)
					}
				}
				report(0)
				report(30)
				before, err := s.Events(ctx, model.EventQuery{})
				if err != nil || len(before) != 1 || !before[0].Active {
					t.Fatalf("trigger %+v %v", before, err)
				}
				for _, sec := range []int{90, 600} {
					at := now.Add(time.Duration(sec) * time.Second)
					if state != "offline" {
						reason := "disk"
						if state == "collect_timeout" {
							reason = "timeout"
						}
						if state == "report_failed" {
							reason = "transport"
						}
						if err := s.Heartbeat(ctx, id, model.AgentStatus{State: state, Reason: reason}, at); err != nil {
							t.Fatal(err)
						}
					}
					if err := engine.Tick(ctx, at); err != nil {
						t.Fatal(err)
					}
				}
				firing, err := s.Events(ctx, model.EventQuery{Active: true})
				if err != nil || len(firing) != 1 || !firing[0].EvaluationPaused || firing[0].ID != before[0].ID {
					t.Fatalf("paused %+v %v", firing, err)
				}
				all, err := s.Events(ctx, model.EventQuery{})
				if err != nil || len(all) != 1 || all[0].EvaluationPaused || all[0].Message != before[0].Message {
					t.Fatalf("history changed or false recovery/repeat %+v %v", all, err)
				}
				queue, err := s.PendingEvents(ctx, now.Add(600*time.Second))
				if err != nil || len(queue) != 1 || queue[0].EvaluationPaused || queue[0].Message != before[0].Message {
					t.Fatalf("delivery snapshot mutated %+v %v", queue, err)
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				s, err = storage.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				engine.Repository = s
				if err := engine.Tick(ctx, now.Add(601*time.Second)); err != nil {
					t.Fatal(err)
				}
				firing, err = s.Events(ctx, model.EventQuery{Active: true})
				if err != nil || len(firing) != 1 || !firing[0].EvaluationPaused {
					t.Fatalf("pause lost on restart %+v %v", firing, err)
				}
				report(610)
				firing, err = s.Events(ctx, model.EventQuery{Active: true})
				if err != nil || len(firing) != 1 || firing[0].EvaluationPaused || !strings.Contains(firing[0].Message, "重复提醒") {
					t.Fatalf("valid high reading did not resume %+v %v", firing, err)
				}
				m.CPU = 20
				m.MemoryUsed = 20
				report(620)
				firing, err = s.Events(ctx, model.EventQuery{Active: true})
				if err != nil || len(firing) != 0 {
					t.Fatalf("valid normal reading did not resolve %+v %v", firing, err)
				}
				all, err = s.Events(ctx, model.EventQuery{})
				if err != nil || len(all) != 3 || all[0].Active {
					t.Fatalf("expected one trigger, repeat, recovery %+v %v", all, err)
				}
			})
		}
	}
}

func TestUnknownMetricsRestartPendingHoldAndDisabledRulesClear(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(filepath.Join(t.TempDir(), "pending.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	rule, err := s.SaveAlertRule(ctx, model.AlertRule{Kind: model.AlertCPU, Threshold: 90, HoldSeconds: 30, Scope: model.ScopeAll}, true)
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := s.SaveNode(ctx, model.Node{Name: "pending", Billing: model.Billing{ResetAt: billingAt(now), TrafficMode: "sum"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	e := Engine{Repository: s}
	m := model.Metrics{CPU: 95, MemoryTotal: 100}
	for _, sec := range []int{0, 15, 90, 110, 120} {
		at := now.Add(time.Duration(sec) * time.Second)
		if sec == 15 {
			err = s.Heartbeat(ctx, id, model.AgentStatus{State: "collect_failed", Reason: "disk"}, at)
		} else {
			err = s.Report(ctx, id, m, at)
		}
		if err != nil {
			t.Fatal(err)
		}
		if err = e.Tick(ctx, at); err != nil {
			t.Fatal(err)
		}
		events, err := s.Events(ctx, model.EventQuery{})
		if err != nil {
			t.Fatal(err)
		}
		want := 0
		if sec == 120 {
			want = 1
		}
		if len(events) != want {
			t.Fatalf("gap counted towards hold at %ds: %+v", sec, events)
		}
	}
	if err = s.Heartbeat(ctx, id, model.AgentStatus{State: "collect_timeout", Reason: "timeout"}, now.Add(130*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = e.Tick(ctx, now.Add(130*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteAlertRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	if err = e.Tick(ctx, now.Add(131*time.Second)); err != nil {
		t.Fatal(err)
	}
	events, err := s.Events(ctx, model.EventQuery{})
	if err != nil || len(events) != 2 || events[0].Active || !strings.Contains(events[0].Message, "规则已关闭、删除或不再覆盖") {
		t.Fatalf("disabled rule did not clear a paused alarm %+v %v", events, err)
	}
}
