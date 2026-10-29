package model

import (
	"encoding/json"
	"testing"
	"time"
)

func blockTask() Task {
	return Task{Name: "IP check", Type: TaskBlock, Enabled: true, NodeIDs: []string{"cn", "global"}, Block: &BlockSpec{Host: "1.1.1.1", Port: 443, Family: "ipv4", Interval: 60, MainlandNodeIDs: []string{"cn"}, OverseasNodeIDs: []string{"global"}}}
}

func TestBlockTaskValidationAndLegacyWire(t *testing.T) {
	task := blockTask()
	if err := task.Validate(); err != nil {
		t.Fatal(err)
	}
	leaf := task.Leaves()[0]
	if leaf.Kind != "tcp" || leaf.Host != task.Block.Host || leaf.Port != 443 {
		t.Fatalf("ordinary TCP job: %+v", leaf)
	}
	// A pre-feature agent sees only its existing Probe fields.
	data, _ := json.Marshal(leaf)
	var decoded Probe
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatal(err)
	}
	key := leaf.ProbeKey()
	task.Block.MainlandNodeIDs, task.Block.OverseasNodeIDs = task.Block.OverseasNodeIDs, task.Block.MainlandNodeIDs
	if task.Leaves()[0].ProbeKey() == key {
		t.Fatal("role changes must isolate old evidence before async purge")
	}
	for _, mutate := range []func(*Task){
		func(t *Task) { t.Block.Host = "example.com" },
		func(t *Task) { t.Block.Host = "127.0.0.1" },
		func(t *Task) { t.Block.Host = "10.0.0.1" },
		func(t *Task) { t.Block.Host = "::1" },
		func(t *Task) { t.Block.Family = "ipv6" },
		func(t *Task) { t.Block.Port = 0 },
		func(t *Task) { t.Block.Interval = 30 },
		func(t *Task) { t.Block.OverseasNodeIDs = []string{"cn"} },
		func(t *Task) { t.Block.MainlandNodeIDs = nil },
		func(t *Task) { t.NodeIDs = append(t.NodeIDs, "unassigned") },
		func(t *Task) { t.Unlock = &UnlockSpec{} },
	} {
		task := blockTask()
		mutate(&task)
		if task.Validate() == nil {
			t.Fatalf("accepted invalid configuration: %+v %+v", task, task.Block)
		}
	}
	task = blockTask()
	task.Block.Host = "2606:4700:4700::1111"
	task.Block.Family = "ipv6"
	if err := task.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestBlockEvidenceRejectsMissingAndTransientFailures(t *testing.T) {
	now := time.Unix(10000, 0)
	failure := func(at int64) BlockAttempt {
		return BlockAttempt{Issued: at, Finished: at + 10, Result: Result{Status: "timeout", Reason: "connect_failed", Sent: 3}}
	}
	a, b := failure(9980), failure(9920)
	for _, tc := range []struct {
		name     string
		attempts []BlockAttempt
		want     string
	}{
		{"two rounds", []BlockAttempt{a, b}, "failed"},
		{"one round", []BlockAttempt{a}, "unconfirmed"},
		{"no lease", nil, "pending"},
		{"pending lease", []BlockAttempt{{Issued: 9980}}, "pending"},
		{"lost report", []BlockAttempt{{Issued: 9900}}, "unavailable"},
		{"expired", []BlockAttempt{failure(9800)}, "stale"},
		{"rapid retry", []BlockAttempt{a, failure(9970)}, "unconfirmed"},
		{"intervening lost report", []BlockAttempt{a, {Issued: 9920}}, "unconfirmed"},
		{"DNS failure", []BlockAttempt{{Issued: 9980, Finished: 9990, Result: Result{Status: "failed", Reason: "dns"}}, b}, "unavailable"},
		{"no connections attempted", []BlockAttempt{{Issued: 9980, Finished: 9990, Result: Result{Status: "timeout", Reason: "timeout"}}, b}, "unavailable"},
		{"recovered", []BlockAttempt{{Issued: 9980, Finished: 9990, Result: Result{Status: "ok", Sent: 3, Received: 1}}, b}, "reachable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := BlockEvidenceState(tc.attempts, 60, now); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestBlockVerdicts(t *testing.T) {
	evidence := func(role, state string, at int64) BlockEvidence {
		return BlockEvidence{Role: role, State: state, Attempts: []BlockAttempt{{Issued: at}}}
	}
	cn := evidence("mainland", "failed", 9980)
	global := evidence("overseas", "reachable", 9970)
	for _, tc := range []struct {
		name    string
		list    []BlockEvidence
		enabled bool
		want    string
	}{
		{"suspected", []BlockEvidence{cn, global}, true, "suspected"},
		{"asynchronous", []BlockEvidence{cn, evidence("overseas", "reachable", 9800)}, true, "unknown"},
		{"no overseas control", []BlockEvidence{cn, evidence("overseas", "offline", 0)}, true, "unknown"},
		{"all down", []BlockEvidence{cn, evidence("overseas", "failed", 9970)}, true, "unreachable"},
		{"mainland reachable", []BlockEvidence{evidence("mainland", "reachable", 9980), global}, true, "reachable"},
		{"regional", []BlockEvidence{cn, evidence("mainland", "reachable", 9970), global}, true, "partial"},
		{"missing mainland evidence", []BlockEvidence{cn, evidence("mainland", "pending", 0), global}, true, "unknown"},
		{"paused", []BlockEvidence{cn, global}, false, "paused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := BlockCheckView{Evidence: tc.list}
			v.Evaluate(tc.enabled, 60)
			if v.Status != tc.want {
				t.Fatalf("got %+v, want %s", v, tc.want)
			}
		})
	}
}
