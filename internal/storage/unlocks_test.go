package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

// unlockTask is the shape the panel creates for the unlock check: a kind of its
// own, no targets, a coarse interval.
func unlockTask(name, nodeID string) model.Task {
	return model.Task{Name: name, Kind: model.UnlockKind, Interval: model.UnlockMinInterval, Enabled: true, NodeIDs: []string{nodeID}}
}

func capableMetrics() model.Metrics {
	m := metrics()
	m.Capabilities = []string{model.CapabilityUnlock}
	return m
}

// TestUnlockLeaseRequiresTheCapability pins the upgrade order: a Server that
// hands an unlock job to an Agent that cannot run it would fill the panel with
// failures for every service, so an unreported capability means "no".
func TestUnlockLeaseRequiresTheCapability(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id, _ := makeNode(t, s)
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	if err := s.Report(ctx, id, metrics(), now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveTask(ctx, unlockTask("解锁检测", id), true); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.Lease(ctx, id, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Fatalf("an Agent without the unlock capability was handed %d jobs", len(jobs))
	}
	// The same fleet, upgraded: the report now carries the capability.
	if err = s.Report(ctx, id, capableMetrics(), now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	jobs, err = s.Lease(ctx, id, now.Add(6*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Task.Kind != model.UnlockKind {
		t.Fatalf("capable Agent leased %+v", jobs)
	}
	if jobs[0].Task.Host != "" || jobs[0].Task.Port != 0 {
		t.Fatalf("unlock leaf carries a target: %+v", jobs[0].Task)
	}
}

// TestUnlockVerdictsScoreAndFollowTheTask covers the whole write path: verdicts
// land per node, the score counts 仅自制剧 as a hit the way the panel it came
// from does, and editing the task takes the stale verdicts with it.
func TestUnlockVerdictsScoreAndFollowTheTask(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id, _ := makeNode(t, s)
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	if err := s.Report(ctx, id, capableMetrics(), now); err != nil {
		t.Fatal(err)
	}
	taskID, err := s.SaveTask(ctx, unlockTask("解锁检测", id), true)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := s.Lease(ctx, id, now.Add(time.Second))
	if err != nil || len(jobs) != 1 {
		t.Fatalf("lease: %+v %v", jobs, err)
	}
	result := model.Result{JobID: jobs[0].ID, Status: "ok", Unlocks: []model.UnlockResult{
		{Service: "youtube_premium", Status: model.UnlockYes, Region: "JP"},
		{Service: "netflix", Status: model.UnlockOriginalsOnly, Detail: "Originals Only"},
		{Service: "disneyplus", Status: model.UnlockBanned, Detail: "forbidden-location"},
		{Service: "apple", Status: model.UnlockYes, Region: "JP"},
	}}
	if err = s.Finish(ctx, id, result, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	summary, err := s.Unlocks(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if summary == nil || len(summary.Services) != 4 {
		t.Fatalf("summary=%+v", summary)
	}
	if summary.Score != 3 || summary.Total != len(model.UnlockCatalog) {
		t.Fatalf("score=%d/%d", summary.Score, summary.Total)
	}
	if summary.TestedAt != now.Add(2*time.Second).Unix() {
		t.Fatalf("tested_at=%d", summary.TestedAt)
	}
	// Catalogue order, not the order the Agent happened to report in.
	if summary.Services[0].Service != "netflix" || summary.Services[len(summary.Services)-1].Service != "apple" {
		t.Fatalf("order=%v", []string{summary.Services[0].Service, summary.Services[len(summary.Services)-1].Service})
	}
	views, err := s.Nodes(ctx, now.Add(3*time.Second))
	if err != nil || len(views) != 1 || views[0].Unlock == nil || views[0].Unlock.Score != 3 {
		t.Fatalf("views=%+v %v", views, err)
	}
	// 改任务即清历史：结论是从结果派生的，旧结论不能留在一个没有任务在跑的节点上。
	edited := unlockTask("解锁检测（改过）", id)
	edited.ID = taskID
	if _, err = s.SaveTask(ctx, edited, false); err != nil {
		t.Fatal(err)
	}
	if summary, err = s.Unlocks(ctx, id); err != nil || summary != nil {
		t.Fatalf("verdicts survived an edit: %+v %v", summary, err)
	}
}

// TestUnlockVerdictsGoWithTheTaskAndTheNode covers the two cleanups: deleting
// the task, and deleting the node (the table cascades).
func TestUnlockVerdictsGoWithTheTaskAndTheNode(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id, _ := makeNode(t, s)
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	if err := s.Report(ctx, id, capableMetrics(), now); err != nil {
		t.Fatal(err)
	}
	taskID, err := s.SaveTask(ctx, unlockTask("解锁检测", id), true)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := s.Lease(ctx, id, now.Add(time.Second))
	if err != nil || len(jobs) != 1 {
		t.Fatalf("lease: %+v %v", jobs, err)
	}
	if err = s.Finish(ctx, id, model.Result{JobID: jobs[0].ID, Status: "ok", Unlocks: []model.UnlockResult{
		{Service: "reddit", Status: model.UnlockYes},
	}}, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteTask(ctx, taskID); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err = s.queryRow(ctx, "SELECT count(*) FROM unlocks WHERE node_id=?", id).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("rows=%d %v", rows, err)
	}
	// A round again, then the node itself goes.
	taskID, err = s.SaveTask(ctx, unlockTask("解锁检测2", id), true)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err = s.Lease(ctx, id, now.Add(10*time.Second))
	if err != nil || len(jobs) != 1 {
		t.Fatalf("lease: %+v %v", jobs, err)
	}
	if err = s.Finish(ctx, id, model.Result{JobID: jobs[0].ID, Status: "ok", Unlocks: []model.UnlockResult{
		{Service: "reddit", Status: model.UnlockYes},
	}}, now.Add(11*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteNode(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err = s.queryRow(ctx, "SELECT count(*) FROM unlocks").Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("rows after node delete=%d %v", rows, err)
	}
	_ = taskID
}

// TestUnlockTaskValidation pins the kind's own rules: no target, no port, a
// coarse interval, and an address family that may be left to the Agent.
func TestUnlockTaskValidation(t *testing.T) {
	id := "node-1"
	cases := []struct {
		name string
		task model.Task
		ok   bool
	}{
		{"default", model.Task{Name: "解锁检测", Kind: model.UnlockKind, Interval: 3600, Enabled: true, NodeIDs: []string{id}}, true},
		{"auto family", model.Task{Name: "解锁检测", Kind: model.UnlockKind, Interval: 3600, Family: "", NodeIDs: []string{id}}, true},
		{"ipv6", model.Task{Name: "解锁检测", Kind: model.UnlockKind, Interval: 3600, Family: "ipv6", NodeIDs: []string{id}}, true},
		{"too fast", model.Task{Name: "解锁检测", Kind: model.UnlockKind, Interval: 60, NodeIDs: []string{id}}, false},
		{"no name", model.Task{Kind: model.UnlockKind, Interval: 3600, NodeIDs: []string{id}}, false},
		{"bad family", model.Task{Name: "解锁检测", Kind: model.UnlockKind, Interval: 3600, Family: "ipv5", NodeIDs: []string{id}}, false},
	}
	for _, c := range cases {
		err := c.task.Validate()
		if (err == nil) != c.ok {
			t.Fatalf("%s: err=%v", c.name, err)
		}
	}
	// 解锁任务不需要 host / port，但也没有目标可言：给了目标就是另一种任务。
	withTarget := model.Task{Name: "解锁检测", Kind: model.UnlockKind, Interval: 3600, Host: "example.com", Port: 443, NodeIDs: []string{id}}
	if err := withTarget.Validate(); err != nil {
		t.Fatalf("a stray target is ignored, not fatal: %v", err)
	}
}

// TestUnlockResultValidation keeps the wire contract closed: an unknown service,
// an unknown verdict or an over-long field is rejected at the door.
func TestUnlockResultValidation(t *testing.T) {
	base := model.Result{JobID: "j", Status: "ok"}
	if err := (model.Result{JobID: "j", Status: "ok", Unlocks: []model.UnlockResult{{Service: "netflix", Status: model.UnlockYes}}}).Validate(); err != nil {
		t.Fatalf("valid result rejected: %v", err)
	}
	for _, bad := range []model.UnlockResult{
		{Service: "spotify", Status: model.UnlockYes},
		{Service: "netflix", Status: "maybe"},
	} {
		r := base
		r.Unlocks = []model.UnlockResult{bad}
		if err := r.Validate(); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	// 只有结果的解锁任务不需要 latency，但带宽那一套的约束仍然生效。
	ok := base
	ok.Unlocks = nil
	ok.Status = "ok"
	if err := ok.Validate(); err == nil {
		t.Fatal("an empty ok result must still be rejected for latency jobs")
	}
}

// TestUnlockRoundMayReportLate pins the reporting window: a latency probe that
// answers minutes late is a stale lease and stays rejected, while an unlock
// round takes minutes by design and must still land.
func TestUnlockRoundMayReportLate(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id, _ := makeNode(t, s)
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	if err := s.Report(ctx, id, capableMetrics(), now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveTask(ctx, unlockTask("解锁检测", id), true); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.Lease(ctx, id, now.Add(time.Second))
	if err != nil || len(jobs) != 1 {
		t.Fatalf("lease: %+v %v", jobs, err)
	}
	late := now.Add(5 * time.Minute)
	if err = s.Finish(ctx, id, model.Result{JobID: jobs[0].ID, Status: "ok", Unlocks: []model.UnlockResult{
		{Service: "claude", Status: model.UnlockYes},
	}}, late); err != nil {
		t.Fatalf("a five-minute unlock round was rejected: %v", err)
	}
	if summary, err := s.Unlocks(ctx, id); err != nil || summary == nil || summary.Score != 1 {
		t.Fatalf("summary=%+v %v", summary, err)
	}
	// 延迟探测的同一条路仍然会过期：窗口只对 unlock 放宽。
	latency := model.Task{Name: "延迟", Kind: "tcp", Host: "1.1.1.1", Port: 443, Family: "ipv4", Interval: 60, Enabled: true, NodeIDs: []string{id}}
	if _, err = s.SaveTask(ctx, latency, true); err != nil {
		t.Fatal(err)
	}
	jobs, err = s.Lease(ctx, id, late.Add(10*time.Second))
	if err != nil || len(jobs) != 1 {
		t.Fatalf("lease: %+v %v", jobs, err)
	}
	stale := late.Add(10*time.Second + 5*time.Minute)
	ms := 12.5
	if err = s.Finish(ctx, id, model.Result{JobID: jobs[0].ID, Status: "ok", Sent: 3, Received: 3, Latency: &ms}, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("a stale latency result was accepted: %v", err)
	}
}

// TestUnlockLeaseFollowsTheHostAddressFamily: an unlock task with no family
// runs over IPv4, so an IPv6-only host must be handed it as IPv6 (otherwise
// every service fails), and a family the host cannot use is not handed out.
func TestUnlockLeaseFollowsTheHostAddressFamily(t *testing.T) {
	cases := []struct {
		name       string
		families   []string
		taskFamily string
		want       string // leased family; "-" means nothing leased
	}{
		{"dual stack keeps IPv4 default", []string{"ipv4", "ipv6"}, "", ""},
		{"unreported families keep the default", nil, "", ""},
		{"IPv6-only host is moved to IPv6", []string{"ipv6"}, "", "ipv6"},
		{"host without any family gets nothing", []string{}, "", "-"},
		{"explicit family the host lacks is skipped", []string{"ipv4"}, "ipv6", "-"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := openTest(t)
			ctx := context.Background()
			id, _ := makeNode(t, s)
			now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
			m := capableMetrics()
			m.IPFamilies = tc.families
			if err := s.Report(ctx, id, m, now); err != nil {
				t.Fatal(err)
			}
			task := unlockTask("解锁检测", id)
			task.Family = tc.taskFamily
			if _, err := s.SaveTask(ctx, task, true); err != nil {
				t.Fatal(err)
			}
			jobs, err := s.Lease(ctx, id, now.Add(time.Second))
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "-" {
				if len(jobs) != 0 {
					t.Fatalf("leased %+v", jobs)
				}
				return
			}
			if len(jobs) != 1 || jobs[0].Task.Family != tc.want {
				t.Fatalf("leased %+v, want family %q", jobs, tc.want)
			}
		})
	}
}

// 用「绑定」把节点移出解锁任务：那台机器的解锁结论跟着清掉（不再有人更新它），仍绑定的
// 节点不受影响；暂停只改管理状态，结论保留。
func TestUnlockVerdictsGoWithAnUnbinding(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	a, _ := makeNode(t, s)
	b, _ := makeNode(t, s)
	task := unlockTask("解锁检测", a)
	task.NodeIDs = []string{a, b}
	taskID, err := s.SaveTask(ctx, task, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a, b} {
		if err = s.Report(ctx, id, capableMetrics(), now); err != nil {
			t.Fatal(err)
		}
		jobs, err := s.Lease(ctx, id, now.Add(time.Second))
		if err != nil || len(jobs) != 1 {
			t.Fatalf("lease: %+v %v", jobs, err)
		}
		if err = s.Finish(ctx, id, model.Result{JobID: jobs[0].ID, Status: "ok", Unlocks: []model.UnlockResult{
			{Service: "reddit", Status: model.UnlockYes},
		}}, now.Add(2*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	count := func(id string) int {
		t.Helper()
		var rows int
		if err := s.queryRow(ctx, "SELECT count(*) FROM unlocks WHERE node_id=?", id).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	if err = s.ControlTask(ctx, taskID, model.TaskAction{Action: "pause"}, now); err != nil {
		t.Fatal(err)
	}
	if count(a) != 1 || count(b) != 1 {
		t.Fatalf("暂停不该清结论: a=%d b=%d", count(a), count(b))
	}
	if err = s.ControlTask(ctx, taskID, model.TaskAction{Action: "bind", NodeIDs: []string{a}}, now); err != nil {
		t.Fatal(err)
	}
	if count(a) != 1 || count(b) != 0 {
		t.Fatalf("解绑后: a=%d b=%d，want 1/0", count(a), count(b))
	}
}

// 一轮解锁检测要几分钟，Finish 收它 15 分钟：这段时间里执行记录与面板都该写「进行中」，
// 过了上报窗口才算超时（延迟探测仍是 90 秒）。
func TestUnlockRoundInFlightIsNotATimeout(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	id, _ := makeNode(t, s)
	if err := s.Report(ctx, id, capableMetrics(), now); err != nil {
		t.Fatal(err)
	}
	taskID, err := s.SaveTask(ctx, unlockTask("解锁检测", id), true)
	if err != nil {
		t.Fatal(err)
	}
	if jobs, err := s.Lease(ctx, id, now); err != nil || len(jobs) != 1 {
		t.Fatalf("lease: %+v %v", jobs, err)
	}
	status := func(at time.Time) (string, string) {
		t.Helper()
		runs, err := s.TaskRuns(ctx, taskID, at)
		if err != nil || len(runs) != 1 {
			t.Fatalf("runs: %+v %v", runs, err)
		}
		obs, err := s.Observations(ctx, id, at)
		if err != nil || len(obs) != 1 {
			t.Fatalf("observations: %+v %v", obs, err)
		}
		return runs[0].Status, obs[0].Result.Status
	}
	if run, obs := status(now.Add(3 * time.Minute)); run != "pending" || obs != "pending" {
		t.Fatalf("3 分钟时 = %s / %s，want pending", run, obs)
	}
	if run, obs := status(now.Add(16 * time.Minute)); run != "timeout" || obs != "timeout" {
		t.Fatalf("16 分钟时 = %s / %s，want timeout", run, obs)
	}
}
