package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

func TestBlockChecksAgentLifecycle(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	cn, _ := makeNode(t, s)
	global, _ := makeNode(t, s)
	task := model.Task{Name: "block", Type: model.TaskBlock, Enabled: true, NodeIDs: []string{cn, global}, Block: &model.BlockSpec{Host: "1.1.1.1", Port: 443, Family: "ipv4", Interval: 60, MainlandNodeIDs: []string{cn}, OverseasNodeIDs: []string{global}}}
	var err error
	task.ID, err = s.SaveTask(ctx, task, true)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(-2 * time.Minute).Truncate(time.Second)
	check := func(at time.Time, want string) {
		t.Helper()
		views, err := s.BlockChecks(ctx, at)
		if err != nil || len(views) != 1 || views[0].Status != want {
			t.Fatalf("views=%+v err=%v, want %s", views, err, want)
		}
	}
	for round := 0; round < 2; round++ {
		at := start.Add(time.Duration(round) * time.Minute)
		for _, id := range []string{cn, global} {
			// No capabilities: a legacy Agent can run this ordinary TCP job.
			if err = s.Report(ctx, id, metrics(), at); err != nil {
				t.Fatal(err)
			}
			jobs, err := s.Lease(ctx, id, at)
			if err != nil || len(jobs) != 1 || jobs[0].Task.Kind != "tcp" {
				t.Fatalf("lease=%+v err=%v", jobs, err)
			}
			r := model.Result{JobID: jobs[0].ID, Status: "timeout", Reason: "connect_failed", Sent: 3}
			if id == global {
				ms := 5.0
				r.Status = "ok"
				r.Reason = ""
				r.Received = 3
				r.Latency = &ms
			}
			if err = s.Finish(ctx, id, r, at.Add(10*time.Second)); err != nil {
				t.Fatal(err)
			}
		}
		want := "unknown"
		if round == 1 {
			want = "suspected"
		}
		check(at.Add(11*time.Second), want)
	}
	at := start.Add(72 * time.Second)
	plan, err := s.ProbeTargets(ctx)
	if err != nil || len(plan[cn]) != 0 {
		t.Fatalf("block checks must not add public latency cells: %+v %v", plan, err)
	}
	if err = s.SetTaskEnabled(ctx, task.ID, false); err != nil {
		t.Fatal(err)
	}
	check(at, "paused")
	if err = s.SetTaskEnabled(ctx, task.ID, true); err != nil {
		t.Fatal(err)
	}
	check(at, "suspected")
	task.Name = "renamed block check"
	if _, err = s.SaveTask(ctx, task, false); err != nil {
		t.Fatal(err)
	}
	check(at, "suspected")
	if err = s.BindTasks(ctx, []string{task.ID}, []string{cn}); !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("must keep explicit roles, got %v", err)
	}
	check(at, "suspected")
	// Old failed/successful history remains until the async purge, but changing
	// the role assignments must immediately discard it from the comparison.
	task.Block.MainlandNodeIDs, task.Block.OverseasNodeIDs = task.Block.OverseasNodeIDs, task.Block.MainlandNodeIDs
	if _, err = s.SaveTask(ctx, task, false); err != nil {
		t.Fatal(err)
	}
	check(at, "unknown")
	if err = s.DeleteNode(ctx, cn); err != nil {
		t.Fatal(err)
	}
	check(at, "unknown")
	if err = s.DeleteTask(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	views, err := s.BlockChecks(ctx, at)
	if err != nil || len(views) != 0 {
		t.Fatalf("deleted task: %+v %v", views, err)
	}
}
