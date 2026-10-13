package storage

import (
	"context"
	"errors"
	"github.com/leeska/TZA-Probe/internal/model"
	"strings"
	"testing"
	"time"
)

func TestTaskControlsPreserveHistoryAndSchedule(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id, _ := makeNode(t, s)
	other, _ := makeNode(t, s)
	task := model.Task{Name: "test", Enabled: true, NodeIDs: []string{id}, Type: model.TaskProbe, Probe: &model.ProbeSpec{Checks: []string{"tcp"}, Targets: []model.Target{{Host: "example.com", Port: 443, Family: "ipv4"}}, Interval: 3600}}
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
		switch action {
		case "pause", "resume":
			return s.SetTaskEnabled(ctx, task.ID, action == "resume")
		case "run":
			return s.RunTask(ctx, task.ID, at)
		default:
			return s.BindTasks(ctx, []string{task.ID}, ids)
		}
	}
	if err = control("pause", nil, now); err != nil {
		t.Fatal(err)
	}
	jobs, err = s.Lease(ctx, id, now.Add(2*time.Hour))
	if err != nil || len(jobs) != 0 {
		t.Fatal("paused leased", err)
	}
	if err = control("run", nil, now); !errors.Is(err, model.ErrConflict) {
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
	if err = control("bind", []string{id, "missing"}, now); !errors.Is(err, model.ErrInvalid) {
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
	task := model.Task{Name: "combined", Enabled: true, NodeIDs: []string{id}, Type: model.TaskProbe, Probe: &model.ProbeSpec{Checks: []string{"tcp", "route"}, Targets: []model.Target{{Host: "1.1.1.1", Port: 443, Family: "ipv4", Region: "test", Carrier: "其他"}, {Host: "8.8.8.8", Port: 443, Family: "ipv4", Region: "test", Carrier: "其他"}}, Interval: 3600, RouteInterval: 3600}}
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
			if err := s.RunTask(ctx, task.ID, now.Add(9*time.Second)); err != nil {
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
	task := model.Task{Name: "same second", Enabled: true, NodeIDs: []string{id}, Type: model.TaskProbe, Probe: &model.ProbeSpec{Checks: []string{"tcp"}, Targets: []model.Target{{Host: "example.com", Port: 443, Family: "ipv4"}}, Interval: 3600}}
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
	if err = s.RunTask(ctx, tasks[0].ID, now); err != nil {
		t.Fatal(err)
	}
	jobs, err = s.Lease(ctx, id, now.Add(time.Second))
	if err != nil || len(jobs) != 1 {
		t.Fatal("request swallowed", jobs, err)
	}
}

// Binding several tasks is one transaction: a task that cannot take the node
// list (here a missing one) leaves every task's bindings as they were.
func TestBindTasksIsAtomic(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	a, _ := makeNode(t, s)
	b, _ := makeNode(t, s)
	var ids []string
	for _, name := range []string{"one", "two"} {
		id, err := s.SaveTask(ctx, model.Task{Name: name, Enabled: true, NodeIDs: []string{a}, Type: model.TaskProbe, Probe: &model.ProbeSpec{Checks: []string{"tcp"}, Targets: []model.Target{{Host: "example.com", Port: 443, Family: "ipv4"}}, Interval: 3600}}, true)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := s.BindTasks(ctx, append(append([]string{}, ids...), "missing-task"), []string{b}); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("missing task: %v", err)
	}
	tasks, _ := s.Tasks(ctx)
	for _, task := range tasks {
		if len(task.NodeIDs) != 1 || task.NodeIDs[0] != a {
			t.Fatalf("partial bind leaked into %s: %v", task.Name, task.NodeIDs)
		}
	}
	if err := s.BindTasks(ctx, ids, []string{a, b}); err != nil {
		t.Fatal(err)
	}
	tasks, _ = s.Tasks(ctx)
	for _, task := range tasks {
		if len(task.NodeIDs) != 2 {
			t.Fatalf("bind missed %s: %v", task.Name, task.NodeIDs)
		}
	}
}

// Batch alert rules land on every node or none, and the failing node is named.
func TestSetNodeAlertsBatch(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	a, _ := makeNode(t, s)
	b, _ := makeNode(t, s)
	rules := model.Alerts{{Kind: model.AlertOffline, HoldSeconds: 120}, {Kind: model.AlertCPU, Threshold: 80, HoldSeconds: 60}, {Kind: model.AlertMemory, Threshold: 85, HoldSeconds: 60}}
	if err := s.SetNodeAlerts(ctx, []string{a, b}, rules); err != nil {
		t.Fatal(err)
	}
	// 降速告警需要节点上的降速阈值：这两台都没有，整批拒绝且不改任何一台。
	throttle := append(append(model.Alerts{}, rules...), model.AlertRule{Kind: model.AlertThrottle})
	err := s.SetNodeAlerts(ctx, []string{a, b}, throttle)
	if !errors.Is(err, model.ErrInvalid) || !strings.Contains(err.Error(), "node：") {
		t.Fatalf("throttle without threshold: %v", err)
	}
	nodes, _ := s.Nodes(ctx, time.Now())
	for _, n := range nodes {
		if encode(n.Alerts) != encode(rules) {
			t.Fatalf("%s alerts=%+v", n.ID, n.Alerts)
		}
	}
	if err = s.SetNodeAlerts(ctx, []string{"missing"}, rules); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("missing node: %v", err)
	}
}

// TaskHealth counts each bound target's latest attempt: ok, failed, or pending
// until its finish window passes (then failed), and only for bound nodes.
func TestTaskHealthCountsLatestAttempts(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id, _ := makeNode(t, s)
	task := model.Task{Name: "health", Enabled: true, NodeIDs: []string{id}, Type: model.TaskProbe, Probe: &model.ProbeSpec{Checks: []string{"tcp"}, Targets: []model.Target{
		{Host: "a.example", Port: 443, Family: "ipv4"},
		{Host: "b.example", Port: 443, Family: "ipv4"},
		{Host: "c.example", Port: 443, Family: "ipv4"},
	}, Interval: 3600}}
	taskID, err := s.SaveTask(ctx, task, true)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if got, err := s.TaskHealth(ctx, now); err != nil || len(got) != 0 {
		t.Fatal("never-run task must be absent", got, err)
	}
	// One TCP probe is leased at a time, so run them one after another.
	lease := func(at time.Time) model.Job {
		jobs, err := s.Lease(ctx, id, at)
		if err != nil || len(jobs) != 1 {
			t.Fatal(jobs, err)
		}
		return jobs[0]
	}
	latency := 10.0
	if err = s.Finish(ctx, id, model.Result{JobID: lease(now).ID, Status: "ok", Sent: 1, Received: 1, Latency: &latency}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = s.Finish(ctx, id, model.Result{JobID: lease(now.Add(time.Second)).ID, Status: "failed", Reason: "connect_failed", Sent: 1}, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	lease(now.Add(2 * time.Second))
	got, err := s.TaskHealth(ctx, now.Add(3*time.Second))
	want := model.TaskHealth{OK: 1, Failed: 1, Pending: 1, Last: now.Add(2 * time.Second).Unix()}
	if err != nil || got[taskID] != want {
		t.Fatalf("got %+v want %+v (%v)", got[taskID], want, err)
	}
	got, _ = s.TaskHealth(ctx, now.Add(time.Hour))
	if h := got[taskID]; h.Pending != 0 || h.Failed != 2 {
		t.Fatalf("an attempt past its finish window counts as failed: %+v", h)
	}
	if err = s.BindTasks(ctx, []string{taskID}, []string{}); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.TaskHealth(ctx, now); len(got) != 0 {
		t.Fatal("unbound nodes must not count", got)
	}
}

// ChangeBindings edits several tasks with different node lists: the added node
// joins each, the removed one leaves each, and everything else stays bound.
func TestChangeBindingsKeepsOtherBindings(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	a, _ := makeNode(t, s)
	b, _ := makeNode(t, s)
	c, _ := makeNode(t, s)
	spec := func(name string, nodes ...string) model.Task {
		return model.Task{Name: name, Enabled: true, NodeIDs: nodes, Type: model.TaskProbe, Probe: &model.ProbeSpec{Checks: []string{"tcp"}, Targets: []model.Target{{Host: "example.com", Port: 443, Family: "ipv4"}}, Interval: 3600}}
	}
	one, err := s.SaveTask(ctx, spec("one", a, b), true)
	if err != nil {
		t.Fatal(err)
	}
	two, err := s.SaveTask(ctx, spec("two", b), true)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ChangeBindings(ctx, []string{one, two}, []string{c}, []string{b}); err != nil {
		t.Fatal(err)
	}
	tasks, _ := s.Tasks(ctx)
	got := map[string]string{}
	for _, task := range tasks {
		got[task.ID] = strings.Join(task.NodeIDs, ",")
	}
	if got[one] != a+","+c || got[two] != c {
		t.Fatalf("one=%s (want %s,%s) two=%s (want %s)", got[one], a, c, got[two], c)
	}
	var rows int
	if err = s.db.QueryRow("SELECT count(*) FROM bindings WHERE node_id=?", b).Scan(&rows); err != nil || rows != 0 {
		t.Fatal("removed node still bound", rows, err)
	}
	if err = s.ChangeBindings(ctx, []string{one}, []string{a}, []string{a}); !errors.Is(err, model.ErrInvalid) {
		t.Fatal("add and remove of the same node must be refused", err)
	}
	if err = s.ChangeBindings(ctx, []string{one}, []string{"missing"}, nil); !errors.Is(err, model.ErrInvalid) {
		t.Fatal("unknown node must be refused", err)
	}
}
