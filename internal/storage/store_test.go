package storage

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func makeNode(t *testing.T, s *Store) (string, string) {
	t.Helper()
	_, token, err := s.SaveNode(context.Background(), model.Node{Name: "node", BillingAt: "2026-09-01T00:00:00Z", TrafficMode: "sum"}, true)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	return id, token
}
func metrics() model.Metrics {
	return model.Metrics{CPU: 12, MemoryUsed: 10, MemoryTotal: 100, DiskUsed: 20, DiskTotal: 100, NetIn: 1000, NetOut: 2000, Uptime: 100}
}
func TestReportingTrafficRestartAndRetention(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id, _ := makeNode(t, s)
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	m := metrics()
	if err := s.Report(ctx, id, m, now); err != nil {
		t.Fatal(err)
	}
	m.NetIn += 500
	m.NetOut += 700
	m.Uptime += 5
	if err := s.Report(ctx, id, m, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	views, err := s.Nodes(ctx, now.Add(6*time.Second))
	if err != nil || views[0].TrafficUsed != 1200 || !views[0].Online {
		t.Fatalf("views=%+v, %v", views, err)
	}
	if err = s.Report(ctx, id, m, now.Add(6*time.Second)); !errors.Is(err, ErrConflict) {
		t.Fatalf("frequent report: %v", err)
	}
	m.NetIn = 5
	m.NetOut = 6
	m.Uptime = 1
	if err = s.Report(ctx, id, m, now.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	views, _ = s.Nodes(ctx, now.Add(60*time.Second))
	if views[0].TrafficUsed != 1200 || views[0].Online {
		t.Fatalf("reboot counter or offline wrong: %+v", views[0])
	}
	history, err := s.History(ctx, id, 24, now.Add(time.Minute))
	if err != nil || len(history) != 1 {
		t.Fatalf("history %d %v", len(history), err)
	}
	if err = s.Clean(ctx, now.Add(31*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.db.QueryRow("SELECT count(*) FROM samples").Scan(&count); err != nil || count != 0 {
		t.Fatal("samples not cleaned", err)
	}
	if err = s.db.QueryRow("SELECT count(*) FROM traffic").Scan(&count); err != nil || count != 1 {
		t.Fatal("billing ledger must outlive sample retention", err)
	}
}
func TestExplicitBindingLeaseOwnershipAndEdit(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	a, _ := makeNode(t, s)
	b, _ := makeNode(t, s)
	now := time.Now()
	task := model.Task{Name: "route", Kind: "route", Host: "example.com", Family: "ipv4", Port: 80, Interval: 900, Enabled: true, Region: "上海", Carrier: "电信"}
	if _, err := s.SaveTask(ctx, task, true); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.Lease(ctx, a, now)
	if err != nil || len(jobs) != 0 {
		t.Fatal("empty binding dispatched", jobs, err)
	}
	tasks, _ := s.Tasks(ctx)
	task = tasks[0]
	task.NodeIDs = []string{a}
	if _, err = s.SaveTask(ctx, task, false); err != nil {
		t.Fatal(err)
	}
	jobs, err = s.Lease(ctx, a, now)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs=%v err=%v", jobs, err)
	}
	duplicate, err := s.Lease(ctx, a, now.Add(time.Second))
	if err != nil || len(duplicate) != 0 {
		t.Fatal("duplicate lease")
	}
	other, err := s.Lease(ctx, b, now)
	if err != nil || len(other) != 0 {
		t.Fatal("wrong node leased")
	}
	latency := 1.0
	r := model.Result{JobID: jobs[0].ID, Status: "ok", Sent: 1, Received: 1, Latency: &latency}
	if err = s.Finish(ctx, b, r, now); !errors.Is(err, ErrConflict) {
		t.Fatal("cross-node result accepted", err)
	}
	if err = s.Finish(ctx, a, r, now); err != nil {
		t.Fatal(err)
	}
	if err = s.Finish(ctx, a, r, now); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate result accepted")
	}
	task.Enabled = false
	if _, err = s.SaveTask(ctx, task, false); err != nil {
		t.Fatal(err)
	}
	obs, err := s.Observations(ctx, a, now)
	if err != nil || len(obs) != 0 {
		t.Fatal("edited task retains stale observations")
	}
	task.NodeIDs = []string{"missing"}
	if _, err = s.SaveTask(ctx, task, false); err == nil {
		t.Fatal("accepted missing node")
	}
}
func TestTokenRotationDeletionAndPersistence(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id, old := makeNode(t, s)
	token, err := s.Rotate(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Authenticate(ctx, old); err == nil {
		t.Fatal("old token valid")
	}
	if got, err := s.Authenticate(ctx, token); err != nil || got != id {
		t.Fatal("new token invalid")
	}
	if err = s.DeleteNode(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Authenticate(ctx, token); err == nil {
		t.Fatal("deleted node authenticates")
	}
	path := filepath.Join(t.TempDir(), "persistent.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_, token = makeNode(t, first)
	first.Close()
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if _, err = second.Authenticate(ctx, token); err != nil {
		t.Fatal("state lost across restart")
	}
}
func TestLegacyDatabaseRejected(t *testing.T) {
	s := openTest(t)
	if _, err := s.db.Exec("PRAGMA user_version=0"); err != nil {
		t.Fatal(err)
	}
	var path string
	if err := s.db.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
		t.Fatal(err)
	}
	s.Close()
	other, err := Open(path)
	if err == nil {
		other.Close()
		t.Fatal("legacy database accepted")
	}
}

func TestCounterSourceChangeDoesNotBillAnotherInterface(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id, _ := makeNode(t, s)
	now := time.Now()
	m := metrics()
	m.CounterID = "boot-a/eth0"
	if err := s.Report(ctx, id, m, now); err != nil {
		t.Fatal(err)
	}
	// A new NIC can have much larger counters and the same/increasing uptime.
	m.CounterID = "boot-a/eth1"
	m.NetIn = 9_000_000
	m.NetOut = 8_000_000
	m.Uptime += 5
	if err := s.Report(ctx, id, m, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	m.NetIn += 100
	m.NetOut += 200
	m.Uptime += 5
	if err := s.Report(ctx, id, m, now.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	views, err := s.Nodes(ctx, now.Add(10*time.Second))
	if err != nil || views[0].TrafficUsed != 300 {
		t.Fatalf("source switch falsely billed: %+v %v", views, err)
	}
}

func TestCombinedTaskIndependentProbes(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	node, _ := makeNode(t, s)
	now := time.Now()
	task := model.Task{Name: "combined", Checks: []string{"tcp", "route"}, Targets: []model.Target{
		{Host: "127.0.0.1", Port: 80, Family: "ipv4", Region: "本地", Carrier: "其他"},
		{Host: "::1", Port: 80, Family: "ipv6", Region: "本地", Carrier: "其他"},
	}, Interval: 30, RouteInterval: 900, Enabled: true, NodeIDs: []string{node}}
	if _, err := s.SaveTask(ctx, task, true); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	finish := func(jobs []model.Job, at time.Time) {
		t.Helper()
		for _, j := range jobs {
			if len(j.Task.Checks) != 0 || len(j.Task.Targets) != 0 {
				t.Fatal("job must be scalar")
			}
			if err := s.Finish(ctx, node, model.Result{JobID: j.ID, Status: "failed"}, at); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i := 0; i < 2; i++ {
		at := now.Add(time.Duration(i) * 10 * time.Second)
		jobs, err := s.Lease(ctx, node, at)
		if err != nil || len(jobs) != 2 {
			t.Fatalf("lease: %+v %v", jobs, err)
		}
		for _, j := range jobs {
			if seen[j.Task.ProbeKey()] {
				t.Fatal("duplicate probe")
			}
			seen[j.Task.ProbeKey()] = true
		}
		// Repeated polling cannot bypass active lease limits.
		blocked, err := s.Lease(ctx, node, at)
		if err != nil || len(blocked) != 0 {
			t.Fatal("active lease bypass")
		}
		finish(jobs, at)
	}
	observations, err := s.Observations(ctx, node, now.Add(20*time.Second))
	if err != nil || len(observations) != 4 {
		t.Fatalf("observations: %+v %v", observations, err)
	}
	jobs, err := s.Lease(ctx, node, now.Add(31*time.Second))
	if err != nil || len(jobs) != 1 || jobs[0].Task.Kind != "tcp" || jobs[0].Task.Family != "ipv4" {
		t.Fatalf("independent cadence: %+v %v", jobs, err)
	}
	finish(jobs, now.Add(31*time.Second))
	tasks, _ := s.Tasks(ctx)
	task = tasks[0]
	task.NodeIDs = nil
	if _, err := s.SaveTask(ctx, task, false); err != nil {
		t.Fatal(err)
	}
	jobs, err = s.Lease(ctx, node, now.Add(time.Hour))
	if err != nil || len(jobs) != 0 {
		t.Fatal("empty bindings executed")
	}
}

func TestLeaseSkipsUnavailableAddressFamilies(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id, _ := makeNode(t, s)
	now := time.Now()
	m := metrics()
	m.IPFamilies = []string{"ipv6"}
	if err := s.Report(ctx, id, m, now); err != nil {
		t.Fatal(err)
	}
	views, err := s.Nodes(ctx, now.Add(time.Second))
	if err != nil || len(views) != 1 {
		t.Fatalf("views=%+v err=%v", views, err)
	}
	if !reflect.DeepEqual(views[0].IPFamilies, []string{"ipv6"}) {
		t.Fatalf("reported families not exposed: %+v", views[0].IPFamilies)
	}
	v4 := model.Task{Name: "v4", Kind: "tcp", Host: "1.1.1.1", Family: "ipv4", Port: 80, Interval: 60, Enabled: true, NodeIDs: []string{id}}
	if _, err = s.SaveTask(ctx, v4, true); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.Lease(ctx, id, now.Add(2*time.Second))
	if err != nil || len(jobs) != 0 {
		t.Fatalf("IPv4 task leased to IPv6-only host: jobs=%v err=%v", jobs, err)
	}
	v6 := model.Task{Name: "v6", Kind: "tcp", Host: "2606:4700:4700::1111", Family: "ipv6", Port: 80, Interval: 60, Enabled: true, NodeIDs: []string{id}}
	if _, err = s.SaveTask(ctx, v6, true); err != nil {
		t.Fatal(err)
	}
	jobs, err = s.Lease(ctx, id, now.Add(3*time.Second))
	if err != nil || len(jobs) != 1 || jobs[0].Task.Family != "ipv6" {
		t.Fatalf("IPv6 task not leased: jobs=%v err=%v", jobs, err)
	}
}

// 线路标签是跳表的解释，读取时按当前规则重算：判定规则修正后，存量的旧结论
// （例如移动回程被记成“CMI → CMIN2”）会跟着跳表一起更新，不必等下一次探测。
func TestObservationsRelabelRouteOnRead(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	node, _ := makeNode(t, s)
	now := time.Date(2026, 9, 22, 20, 8, 41, 0, time.UTC)
	_, err := s.SaveTask(ctx, model.Task{
		Name: "移动回程", Kind: "route", Family: "ipv6",
		Host: "2409:8c62:e10:32::99:80", Region: "四川", Carrier: "移动", Port: 443,
		Interval: 3600, RouteInterval: 3600, Enabled: true, NodeIDs: []string{node},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := s.Lease(ctx, node, now)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("lease: %+v %v", jobs, err)
	}
	hops := []model.Hop{
		{TTL: 1, Address: "2602:f656:****"},
		{TTL: 2, Address: "2602:f656:****", ASN: "AS1054"},
		{TTL: 3, Address: "2402:4f00:****", ASN: "AS58453"},
		{TTL: 4, Address: "2402:4f00:****", ASN: "AS58807"},
		{TTL: 6, Address: "2409:8080:****", ASN: "AS9808"},
		{TTL: 14, Address: "2409:8c62:****", ASN: "AS139080"},
	}
	// 存量数据里存的是修正前的标签。
	latency := 159.8
	stale := model.Result{JobID: jobs[0].ID, Status: "ok", Sent: 1, Received: 1, Latency: &latency, Route: "CMI → CMIN2", Hops: hops}
	if err = s.Finish(ctx, node, stale, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	obs, err := s.Observations(ctx, node, now.Add(2*time.Second))
	if err != nil || len(obs) != 1 {
		t.Fatalf("observations: %+v %v", obs, err)
	}
	if obs[0].Result.Route != "CMIN2" {
		t.Fatalf("route must be recomputed from the hops, got %q", obs[0].Result.Route)
	}
}
