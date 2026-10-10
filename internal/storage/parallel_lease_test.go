package storage

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

// parallelSetup binds one node to an unlock task and a combined task with
// twelve latency targets plus their routes, and reports the given capabilities.
func parallelSetup(t *testing.T, capabilities ...string) (*Store, string, time.Time) {
	t.Helper()
	s := openTest(t)
	ctx := context.Background()
	id, _ := makeNode(t, s)
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	m := metrics()
	m.Capabilities = capabilities
	if err := s.Report(ctx, id, m, now); err != nil {
		t.Fatal(err)
	}
	targets := []model.Target{}
	for i := 1; i <= 12; i++ {
		targets = append(targets, model.Target{Host: fmt.Sprintf("127.0.0.%d", i), Port: 80, Family: "ipv4", Region: "上海", Carrier: "电信"})
	}
	task := model.Task{Name: "三网", Checks: []string{"tcp", "route"}, Targets: targets, Interval: 30, RouteInterval: 900, Enabled: true, NodeIDs: []string{id}}
	if _, err := s.SaveTask(ctx, task, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveTask(ctx, unlockTask("解锁检测", id), true); err != nil {
		t.Fatal(err)
	}
	return s, id, now
}

func countKinds(jobs []model.Job) map[string]int {
	kinds := map[string]int{}
	for _, j := range jobs {
		kinds[j.Task.Kind]++
	}
	return kinds
}

// TestParallelLeaseBatchesLatencyAndSeparatesKinds pins the lease for an Agent
// that runs jobs side by side: a batch of latency probes plus one route and one
// unlock job, and a kind still in flight blocks only itself — the unlock round
// takes minutes and must not stall latency probing.
func TestParallelLeaseBatchesLatencyAndSeparatesKinds(t *testing.T) {
	s, id, now := parallelSetup(t, model.CapabilityUnlock, model.CapabilityParallelJobs)
	ctx := context.Background()
	at := now.Add(time.Second)
	jobs, err := s.Lease(ctx, id, at)
	if err != nil {
		t.Fatal(err)
	}
	if got := countKinds(jobs); got["tcp"] != tcpLeaseJobs || got["route"] != 1 || got[model.UnlockKind] != 1 {
		t.Fatalf("first lease kinds: %v", got)
	}
	if again, err := s.Lease(ctx, id, at); err != nil || len(again) != 0 {
		t.Fatalf("in-flight kinds leased again: %+v %v", again, err)
	}
	latency := 5.0
	var unlockJob model.Job
	for _, j := range jobs {
		switch j.Task.Kind {
		case "tcp":
			if err = s.Finish(ctx, id, model.Result{JobID: j.ID, Status: "ok", Sent: 3, Received: 3, Latency: &latency}, at.Add(5*time.Second)); err != nil {
				t.Fatal(err)
			}
		case "route":
			if err = s.Finish(ctx, id, model.Result{JobID: j.ID, Status: "failed"}, at.Add(5*time.Second)); err != nil {
				t.Fatal(err)
			}
		default:
			unlockJob = j
		}
	}
	// Two minutes into the unlock round: the remaining latency targets come out
	// even though the unlock job is still running.
	later := at.Add(2 * time.Minute)
	jobs, err = s.Lease(ctx, id, later)
	if err != nil {
		t.Fatal(err)
	}
	if got := countKinds(jobs); got["tcp"] != tcpLeaseJobs || got[model.UnlockKind] != 0 || got["route"] != 1 {
		t.Fatalf("lease during unlock round: %v", got)
	}
	// The unlock round, minutes late, still reports.
	if err = s.Finish(ctx, id, model.Result{JobID: unlockJob.ID, Status: "ok", Unlocks: []model.UnlockResult{{Service: "reddit", Status: model.UnlockYes}}}, later.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
}

// TestLegacyLeaseNeverExceedsTwoJobs pins the old lease for an Agent without
// CapabilityParallelJobs: it runs only two jobs of a lease and waits for them,
// so a third would be dropped and sit in the panel as a timeout.
func TestLegacyLeaseNeverExceedsTwoJobs(t *testing.T) {
	s, id, now := parallelSetup(t, model.CapabilityUnlock)
	ctx := context.Background()
	jobs, err := s.Lease(ctx, id, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != legacyLeaseJobs {
		t.Fatalf("legacy lease: %d jobs %v", len(jobs), countKinds(jobs))
	}
	for kind, n := range countKinds(jobs) {
		if n > 1 {
			t.Fatalf("legacy lease carries %d %s jobs", n, kind)
		}
	}
	if again, err := s.Lease(ctx, id, now.Add(2*time.Second)); err != nil || len(again) != 0 {
		t.Fatalf("legacy agent leased while busy: %+v %v", again, err)
	}
}
