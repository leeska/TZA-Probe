package storage

import (
	"context"
	"github.com/leeska/TZA-Probe/internal/model"
	"path/filepath"
	"testing"
	"time"
)

func TestCycleCorrectionPreservesCountersAndExpires(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id, _ := makeNode(t, s)
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	m := metrics()
	if err := s.Report(ctx, id, m, now); err != nil {
		t.Fatal(err)
	}
	m.NetIn += 100
	m.Uptime += 10
	if err := s.Report(ctx, id, m, now.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	start, _ := model.Cycle(now, 1)
	if err := s.CorrectTraffic(ctx, id, 500, start.Unix()-1, now); err != model.ErrConflict {
		t.Fatal(err)
	}
	if err := s.CorrectTraffic(ctx, id, 500, start.Unix(), now); err != nil {
		t.Fatal(err)
	}
	m.NetIn += 50
	m.Uptime += 10
	if err := s.Report(ctx, id, m, now.Add(20*time.Second)); err != nil {
		t.Fatal(err)
	}
	nodes, _ := s.Nodes(ctx, now)
	if nodes[0].TrafficUsed != 550 {
		t.Fatal(nodes[0].TrafficUsed)
	}
	n := nodes[0].Node
	n.Name = "renamed"
	n.TrafficAdjustment = 0
	if _, err := s.SaveNode(ctx, n, false); err != nil {
		t.Fatal(err)
	}
	nodes, _ = s.Nodes(ctx, now)
	if nodes[0].TrafficUsed != 550 {
		t.Fatal("edit lost correction")
	}
	nodes, _ = s.Nodes(ctx, now.AddDate(0, 1, 0))
	if nodes[0].TrafficUsed != 0 {
		t.Fatal("correction leaked across cycle")
	}
}

func TestRepeatReminderAndFailurePersistence(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id, _ := makeNode(t, s)
	now := time.Now()
	n := model.Node{ID: id, Name: "test", Alerts: model.Alerts{RepeatSeconds: 300}}
	for _, sec := range []int{0, 30, 299, 300, 301} {
		if err := s.Transition(ctx, n, "offline", "offline", true, 30, now.Add(time.Duration(sec)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	events, _ := s.Events(ctx, false, now)
	if len(events) != 1 {
		t.Fatal("repeat before cooldown", len(events))
	}
	if err := s.Transition(ctx, n, "offline", "offline", true, 30, now.Add(330*time.Second)); err != nil {
		t.Fatal(err)
	}
	events, _ = s.Events(ctx, false, now)
	if len(events) != 2 {
		t.Fatal(len(events))
	}
	e := events[0]
	e.LastError = "connection failed"
	if err := s.Delivery(ctx, e, false, now); err != nil {
		t.Fatal(err)
	}
	events, _ = s.Events(ctx, false, now)
	if events[0].LastError != e.LastError || events[0].Attempts != 1 {
		t.Fatal(events)
	}
	if err := s.Transition(ctx, n, "offline", "offline", false, 30, now.Add(340*time.Second)); err != nil {
		t.Fatal(err)
	}
	events, _ = s.Events(ctx, false, now)
	if len(events) != 3 || events[0].Active {
		t.Fatal("missing recovery")
	}
}

func TestUpgradeVersionOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upgrade.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("ALTER TABLE events DROP COLUMN last_error; DROP INDEX events_rule_time; PRAGMA user_version=1;"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.Events(context.Background(), false, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestNetworkHistorySeparatesTargetsAndNullLatency(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id, _ := makeNode(t, s)
	now := time.Now().Truncate(time.Minute)
	task := model.Task{ID: "task", Kind: "tcp", Family: "ipv4", Host: "example.com", Port: 443, Region: "test"}
	if _, err := s.db.Exec("INSERT INTO tasks VALUES(?,?)", task.ID, encode(task)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		latency := 20.0
		r := model.Result{Status: "ok", Sent: 1, Received: 1, Latency: &latency}
		if i == 1 {
			r = model.Result{Status: "failed", Sent: 1}
		}
		if i == 2 {
			task.Family = "ipv6"
		}
		at := now.Add(time.Duration(i) * time.Minute).Unix()
		if _, err := s.db.Exec("INSERT INTO jobs(id,task_id,node_id,task,issued,finished,result) VALUES(?,?,?,?,?,?,?)", string(rune('a'+i)), task.ID, id, encode(task), at, at, encode(r)); err != nil {
			t.Fatal(err)
		}
	}
	series, err := s.NetworkHistory(ctx, id, 1, now.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 2 || len(series[0].Samples) != 2 {
		t.Fatal(series)
	}
	failed := series[0].Samples[1]
	if failed.Latency != nil || failed.Failure == nil || *failed.Failure != 100 {
		t.Fatal(failed)
	}
}
