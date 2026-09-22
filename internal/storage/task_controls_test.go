package storage

import (
	"context"
	"github.com/leeska/TZA-Probe/internal/model"
	"testing"
	"time"
)

func TestTaskControlsPreserveHistoryAndSchedule(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id, _ := makeNode(t, s)
	other, _ := makeNode(t, s)
	task := model.Task{Name: "test", Kind: "tcp", Host: "example.com", Port: 443, Family: "ipv4", Interval: 3600, Enabled: true, NodeIDs: []string{id}}
	if _, err := s.SaveTask(ctx, task, true); err != nil {
		t.Fatal(err)
	}
	tasks, _ := s.Tasks(ctx)
	task = tasks[0]
	now := time.Now()
	jobs, err := s.Lease(ctx, id, now)
	if err != nil || len(jobs) != 1 {
		t.Fatal(jobs, err)
	}
	latency := 10.0
	if err = s.Finish(ctx, id, model.Result{JobID: jobs[0].ID, Status: "ok", Sent: 1, Received: 1, Latency: &latency}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	control := func(action string, ids []string, at time.Time) error {
		return s.ControlTask(ctx, task.ID, model.TaskAction{Action: action, NodeIDs: ids}, at)
	}
	if err = control("pause", nil, now); err != nil {
		t.Fatal(err)
	}
	jobs, err = s.Lease(ctx, id, now.Add(2*time.Hour))
	if err != nil || len(jobs) != 0 {
		t.Fatal("paused leased", err)
	}
	if err = control("run", nil, now); err != model.ErrConflict {
		t.Fatal(err)
	}
	if err = control("resume", nil, now); err != nil {
		t.Fatal(err)
	}
	jobs, err = s.Lease(ctx, id, now.Add(10*time.Second))
	if err != nil || len(jobs) != 0 {
		t.Fatal("resume reset interval", err)
	}
	if err = control("run", nil, now.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	jobs, err = s.Lease(ctx, id, now.Add(11*time.Second))
	if err != nil || len(jobs) != 1 {
		t.Fatal("manual request not scheduled", err)
	}
	again, err := s.Lease(ctx, id, now.Add(12*time.Second))
	if err != nil || len(again) != 0 {
		t.Fatal("concurrency bound", err)
	}
	if err = s.Finish(ctx, id, model.Result{JobID: jobs[0].ID, Status: "failed", Reason: "connect_failed", Sent: 1}, now.Add(13*time.Second)); err != nil {
		t.Fatal(err)
	}
	again, err = s.Lease(ctx, id, now.Add(14*time.Second))
	if err != nil || len(again) != 0 {
		t.Fatal("manual request repeated", err)
	}
	runs, err := s.TaskRuns(ctx, task.ID, now.Add(15*time.Second))
	if err != nil || len(runs) != 1 || runs[0].Reason != "connect_failed" {
		t.Fatal(runs, err)
	}
	if err = control("bind", []string{id, "missing"}, now); err != model.ErrInvalid {
		t.Fatal(err)
	}
	if err = control("bind", []string{other}, now); err != nil {
		t.Fatal(err)
	}
	again, err = s.Lease(ctx, id, now.Add(2*time.Hour))
	if err != nil || len(again) != 0 {
		t.Fatal("removed binding leased", err)
	}
	var count int
	if err = s.db.QueryRow("SELECT count(*) FROM jobs WHERE task_id=?", task.ID).Scan(&count); err != nil || count != 2 {
		t.Fatal("history deleted", count, err)
	}
	runs, err = s.TaskRuns(ctx, task.ID, now)
	if err != nil || len(runs) != 1 || runs[0].Status != "never" {
		t.Fatal(runs, err)
	}
	if err = control("bind", []string{}, now); err != nil {
		t.Fatal(err)
	}
	again, err = s.Lease(ctx, other, now.Add(2*time.Hour))
	if err != nil || len(again) != 0 {
		t.Fatal("empty bindings leased", err)
	}
}

func TestManualRunCoversEveryLeafOnce(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id, _ := makeNode(t, s)
	task := model.Task{Name: "combined", Checks: []string{"tcp", "route"}, Targets: []model.Target{{Host: "1.1.1.1", Port: 443, Family: "ipv4", Region: "test", Carrier: "其他"}, {Host: "8.8.8.8", Port: 443, Family: "ipv4", Region: "test", Carrier: "其他"}}, Interval: 3600, RouteInterval: 3600, Enabled: true, NodeIDs: []string{id}}
	if _, err := s.SaveTask(ctx, task, true); err != nil {
		t.Fatal(err)
	}
	tasks, _ := s.Tasks(ctx)
	task = tasks[0]
	now := time.Now()
	for round := 0; round < 2; round++ {
		for batch := 0; batch < 2; batch++ {
			at := now.Add(time.Duration(round*10+batch*2) * time.Second)
			jobs, err := s.Lease(ctx, id, at)
			if err != nil || len(jobs) != 2 {
				t.Fatal(round, batch, jobs, err)
			}
			for _, j := range jobs {
				if err = s.Finish(ctx, id, model.Result{JobID: j.ID, Status: "failed", Sent: 1}, at.Add(time.Second)); err != nil {
					t.Fatal(err)
				}
			}
		}
		if round == 0 {
			if err := s.ControlTask(ctx, task.ID, model.TaskAction{Action: "run"}, now.Add(9*time.Second)); err != nil {
				t.Fatal(err)
			}
		}
	}
	jobs, err := s.Lease(ctx, id, now.Add(20*time.Second))
	if err != nil || len(jobs) != 0 {
		t.Fatal("leaf repeated", err)
	}
	runs, err := s.TaskRuns(ctx, task.ID, now.Add(20*time.Second))
	if err != nil || len(runs) != 4 {
		t.Fatal(runs, err)
	}
}

func TestManualRequestInSameSecondAsPreviousLease(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id, _ := makeNode(t, s)
	task := model.Task{Name: "same second", Kind: "tcp", Host: "example.com", Port: 443, Family: "ipv4", Interval: 3600, Enabled: true, NodeIDs: []string{id}}
	if _, err := s.SaveTask(ctx, task, true); err != nil {
		t.Fatal(err)
	}
	tasks, _ := s.Tasks(ctx)
	now := time.Now().Truncate(time.Second)
	jobs, err := s.Lease(ctx, id, now)
	if err != nil || len(jobs) != 1 {
		t.Fatal(jobs, err)
	}
	if err = s.Finish(ctx, id, model.Result{JobID: jobs[0].ID, Status: "failed", Sent: 1}, now); err != nil {
		t.Fatal(err)
	}
	if err = s.ControlTask(ctx, tasks[0].ID, model.TaskAction{Action: "run"}, now); err != nil {
		t.Fatal(err)
	}
	jobs, err = s.Lease(ctx, id, now.Add(time.Second))
	if err != nil || len(jobs) != 1 {
		t.Fatal("request swallowed", jobs, err)
	}
}
