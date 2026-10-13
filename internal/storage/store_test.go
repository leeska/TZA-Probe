package storage

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
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

// drainPurges runs the background purge queue to the end synchronously.
func drainPurges(t *testing.T, s *Store) {
	t.Helper()
	if err := s.runPurges(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func makeNode(t *testing.T, s *Store) (string, string) {
	t.Helper()
	_, token, err := s.SaveNode(context.Background(), model.Node{Name: "node", Billing: model.Billing{ResetAt: "2026-09-01T00:00:00Z", TrafficMode: "sum"}}, true)
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
	task := model.Task{Name: "route", Enabled: true, Type: model.TaskProbe, Probe: &model.ProbeSpec{Checks: []string{"route"}, Targets: []model.Target{{Host: "example.com", Port: 80, Family: "ipv4", Region: "上海", Carrier: "电信"}}, RouteInterval: 900}}
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
	// 只暂停（探测内容不变）：结果保留，与专用的启停接口一致。
	task.Enabled = false
	if _, err = s.SaveTask(ctx, task, false); err != nil {
		t.Fatal(err)
	}
	obs, err := s.Observations(ctx, a, now)
	if err != nil || len(obs) != 1 {
		t.Fatalf("pausing through a save dropped results: %d %v", len(obs), err)
	}
	// 改了探测内容：旧结果不再属于这个任务，面板立刻看不到。
	task.Probe = &model.ProbeSpec{Checks: []string{"tcp"}, Targets: []model.Target{{Host: "moved.example.com", Port: 443, Family: "ipv4"}}, Interval: 30}
	if _, err = s.SaveTask(ctx, task, false); err != nil {
		t.Fatal(err)
	}
	obs, err = s.Observations(ctx, a, now)
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
	task := model.Task{Name: "combined", Enabled: true, NodeIDs: []string{node}, Type: model.TaskProbe, Probe: &model.ProbeSpec{Checks: []string{"tcp", "route"}, Targets: []model.Target{
		{Host: "127.0.0.1", Port: 80, Family: "ipv4", Region: "本地", Carrier: "其他"},
		{Host: "::1", Port: 80, Family: "ipv6", Region: "本地", Carrier: "其他"},
	}, Interval: 30, RouteInterval: 900}}
	if _, err := s.SaveTask(ctx, task, true); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	finish := func(jobs []model.Job, at time.Time) {
		t.Helper()
		for _, j := range jobs {
			if j.Task.Host == "" || (j.Task.Kind != "tcp" && j.Task.Kind != "route") {
				t.Fatalf("job must be one concrete probe: %+v", j.Task)
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

func TestProbeTargetsListWhatANodeReallyProbes(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	bound, _ := makeNode(t, s)
	spare, _ := makeNode(t, s)
	// 一台机器挂三网（电信只有 IPv4）；另一台故意不挂任何任务。
	three := model.Task{Name: "三网", Enabled: true, NodeIDs: []string{bound, spare}, Type: model.TaskProbe, Probe: &model.ProbeSpec{Checks: []string{"tcp", "route"}, Targets: []model.Target{
		{Host: "127.0.0.1", Port: 80, Family: "ipv4", Region: "上海", Carrier: "电信"},
		{Host: "::1", Port: 80, Family: "ipv6", Region: "上海", Carrier: "电信"},
		{Host: "127.0.0.2", Port: 80, Family: "ipv4", Region: "上海", Carrier: "其他"},
	}, Interval: 30, RouteInterval: 900}}
	if _, err := s.SaveTask(ctx, three, true); err != nil {
		t.Fatal(err)
	}
	// 第二台任务：国际目标（没有运营商、没有地区的老写法），只有 TCP —— 计划要把两个任务并起来。
	international := model.Task{Name: "国际", Enabled: true, NodeIDs: []string{bound}, Type: model.TaskProbe, Probe: &model.ProbeSpec{Checks: []string{"tcp"}, Targets: []model.Target{
		{Host: "127.0.0.3", Port: 443, Family: "ipv4", Carrier: ""},
	}, Interval: 30}}
	if _, err := s.SaveTask(ctx, international, true); err != nil {
		t.Fatal(err)
	}
	// 「故意不挂任务」的机器要摘下来，否则计划里会留着它的项。
	tasks, _ := s.Tasks(ctx)
	for _, task := range tasks {
		if task.Name != "三网" {
			continue
		}
		task.NodeIDs = []string{bound}
		if _, err := s.SaveTask(ctx, task, false); err != nil {
			t.Fatal(err)
		}
	}
	views, err := s.Nodes(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	plan := map[string][]string{}
	for _, v := range views {
		for _, p := range v.ProbeTargets {
			plan[v.ID] = append(plan[v.ID], fmt.Sprintf("%s/%s/%s/%s", p.Kind, p.Carrier, p.Family, p.Region))
		}
		sort.Strings(plan[v.ID])
	}
	want := []string{
		"route/电信/ipv4/上海",
		"route/电信/ipv6/上海",
		"route/其他/ipv4/上海",
		"tcp//ipv4/",
		"tcp/电信/ipv4/上海",
		"tcp/电信/ipv6/上海",
		"tcp/其他/ipv4/上海",
	}
	sort.Strings(want)
	if !reflect.DeepEqual(plan[bound], want) {
		t.Fatalf("bound node plan:\n got %+v\nwant %+v", plan[bound], want)
	}
	if len(plan[spare]) != 0 {
		t.Fatalf("unbound node must carry no plan, got %+v", plan[spare])
	}
	// 停用的任务不算：面板上那台机器就该回到「仅主机指标」，而不是留一排空格子。
	tasks, _ = s.Tasks(ctx)
	for _, task := range tasks {
		task.Enabled = false
		if _, err := s.SaveTask(ctx, task, false); err != nil {
			t.Fatal(err)
		}
	}
	views, err = s.Nodes(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range views {
		if len(v.ProbeTargets) != 0 {
			t.Fatalf("a paused task must not be planned, got %+v", v.ProbeTargets)
		}
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
	v4 := model.Task{Name: "v4", Enabled: true, NodeIDs: []string{id}, Type: model.TaskProbe, Probe: &model.ProbeSpec{Checks: []string{"tcp"}, Targets: []model.Target{{Host: "1.1.1.1", Port: 80, Family: "ipv4"}}, Interval: 60}}
	if _, err = s.SaveTask(ctx, v4, true); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.Lease(ctx, id, now.Add(2*time.Second))
	if err != nil || len(jobs) != 0 {
		t.Fatalf("IPv4 task leased to IPv6-only host: jobs=%v err=%v", jobs, err)
	}
	v6 := model.Task{Name: "v6", Enabled: true, NodeIDs: []string{id}, Type: model.TaskProbe, Probe: &model.ProbeSpec{Checks: []string{"tcp"}, Targets: []model.Target{{Host: "2606:4700:4700::1111", Port: 80, Family: "ipv6"}}, Interval: 60}}
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
	_, err := s.SaveTask(ctx, model.Task{Name: "移动回程", Enabled: true, NodeIDs: []string{node}, Type: model.TaskProbe, Probe: &model.ProbeSpec{Checks: []string{"route"}, Targets: []model.Target{{Host: "2409:8c62:e10:32::99:80", Port: 443, Family: "ipv6", Region: "四川", Carrier: "移动"}}, RouteInterval: 3600}}, true)
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

// Every writer connection must enforce foreign keys, not only the first one:
// node deletion relies on ON DELETE CASCADE.
func TestWriterConnectionsEnforceForeignKeys(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		conn, err := s.db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var on int
		if err = conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&on); err != nil || on != 1 {
			t.Fatalf("foreign_keys=%d %v", on, err)
		}
		// Discard the connection so the pool has to open a fresh one.
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		conn.Close()
	}
}

// Events that waited longer than model.EventDeliveryWindow are no longer
// offered for delivery: configuring a channel later must not replay old alerts.
func TestPendingEventsExpire(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	for _, at := range []time.Time{now.Add(-model.EventDeliveryWindow - time.Minute), now.Add(-time.Hour)} {
		if _, err := s.db.ExecContext(ctx, "INSERT INTO events(node_id,node_name,kind,message,active,at) VALUES('n','node','offline','节点已离线',1,?)", at.Unix()); err != nil {
			t.Fatal(err)
		}
	}
	pending, err := s.PendingEvents(ctx, now)
	if err != nil || len(pending) != 1 || pending[0].At != now.Add(-time.Hour).Unix() {
		t.Fatalf("pending=%+v %v", pending, err)
	}
	all, err := s.Events(ctx, model.EventQuery{})
	if err != nil || len(all) != 2 {
		t.Fatalf("history must keep expired events: %+v %v", all, err)
	}
}

// The panel pages the 90-day event log by id cursor and can narrow it to one
// node or one kind; a page never exceeds model.MaxEventPage.
func TestEventsPageAndFilter(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	for i := 0; i < 150; i++ {
		node, kind := "a", "offline"
		if i%3 == 0 {
			node, kind = "b", "load"
		}
		if _, err := s.db.ExecContext(ctx, "INSERT INTO events(node_id,node_name,kind,message,active,at) VALUES(?,?,?,'m',1,?)", node, node, kind, 1000+i); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.Events(ctx, model.EventQuery{})
	if err != nil || len(first) != model.DefaultEventPage || first[0].At != 1149 {
		t.Fatalf("first page: %d %v", len(first), err)
	}
	rest, err := s.Events(ctx, model.EventQuery{Before: first[len(first)-1].ID})
	if err != nil || len(rest) != 50 || rest[0].ID >= first[len(first)-1].ID {
		t.Fatalf("second page: %d %v", len(rest), err)
	}
	only, err := s.Events(ctx, model.EventQuery{NodeID: "b", Kind: "load", Limit: 1000})
	if err != nil || len(only) != 50 {
		t.Fatalf("filtered: %d %v", len(only), err)
	}
	for _, e := range only {
		if e.NodeID != "b" || e.Kind != "load" {
			t.Fatalf("filter leaked %+v", e)
		}
	}
	if q := (model.EventQuery{Limit: 1 << 20}).Normalize(); q.Limit != model.MaxEventPage {
		t.Fatalf("limit not clamped: %d", q.Limit)
	}
}

// A fresh database runs the version 1 baseline and then every migration, so it
// must end at the newest version with every later table, index and column.
func TestFreshDatabaseRunsEveryMigration(t *testing.T) {
	s := openTest(t)
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 15 {
		t.Fatalf("user_version=%d %v", version, err)
	}
	for _, name := range []string{"latest", "traffic_hour", "unlocks", "retries", "alert_rules", "events_rule_time", "jobs_latest_insert", "tcp_buckets", "tcp_buckets_state", "jobs_tcp_bucket_insert", "jobs_tcp_bucket_finish"} {
		var n int
		if err := s.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name=?", name).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s missing (%d, %v)", name, n, err)
		}
	}
	for table, column := range map[string]string{"events": "last_error", "jobs": "probe_key"} {
		var n int
		if err := s.db.QueryRow("SELECT count(*) FROM pragma_table_info(?) WHERE name=?", table, column).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s.%s missing (%d, %v)", table, column, n, err)
		}
	}
}

// Version 10 moves 免费 out of the price period into the free flag: an old free
// machine comes back free with no price or period, paid machines are untouched,
// and the period no longer accepts 免费 at all.
func TestFreeFlagMigrationAndSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "free.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for id, period := range map[string]string{"free": "免费", "paid": "月"} {
		config := `{"name":"` + id + `","billing_at":"2026-09-01T00:00:00Z","traffic_mode":"sum","currency":"CNY","price_period":"` + period + `"}`
		if _, err = s.db.ExecContext(ctx, "INSERT INTO nodes(id,config,token_hash) VALUES(?,?,?)", id, config, model.Hash(id)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.db.Exec("PRAGMA user_version=9"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if s, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	nodes, err := s.Nodes(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		if n.ID == "free" && (!n.Billing.Free || n.Billing.PricePeriod != "" || n.Billing.Currency != "CNY") {
			t.Fatalf("free node after migration: %+v", n.Node)
		}
		if n.ID == "paid" && (n.Billing.Free || n.Billing.PricePeriod != "月") {
			t.Fatalf("paid node touched: %+v", n.Node)
		}
	}
	base := model.Node{Name: "x", Billing: model.Billing{ResetAt: "2026-09-01T00:00:00Z", TrafficMode: "sum"}}
	legacy := base
	legacy.Billing.PricePeriod = "免费"
	if _, _, err = s.SaveNode(ctx, legacy, true); err == nil {
		t.Fatal("price period 免费 accepted")
	}
	free := base
	free.Billing.Free, free.Billing.Price, free.Billing.PricePeriod = true, 9.9, "月"
	id, _, err := s.SaveNode(ctx, free, true)
	if err != nil {
		t.Fatal(err)
	}
	nodes, _ = s.Nodes(ctx, time.Now())
	for _, n := range nodes {
		if n.ID == id && (!n.Billing.Free || n.Billing.Price != 0 || n.Billing.PricePeriod != "") {
			t.Fatalf("free save kept a price: %+v", n.Node)
		}
	}
}

// Version 11 turns every legacy task config (combined, single probe, unlock)
// into the tagged union, keeping each probe's key exactly as it was so results,
// history and schedules stay attached.
func TestTaskShapeMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	legacy := map[string]string{
		"combined": `{"id":"combined","name":"三网","enabled":true,"node_ids":[],"checks":["tcp","route"],"targets":[{"host":"1.1.1.1","port":443,"family":"ipv4","region":"上海","carrier":"电信"}],"interval":60,"route_interval":1800}`,
		"tcp":      `{"id":"tcp","name":"单项延迟","enabled":true,"node_ids":[],"kind":"tcp","host":"example.com","port":443,"family":"ipv4","region":"","carrier":"","interval":30}`,
		"route":    `{"id":"route","name":"单项回程","enabled":false,"node_ids":[],"kind":"route","host":"2.2.2.2","port":80,"family":"ipv4","region":"北京","carrier":"联通","interval":900}`,
		"unlock":   `{"id":"unlock","name":"解锁","enabled":true,"node_ids":[],"kind":"unlock","family":"ipv6","interval":7200}`,
	}
	wantKeys := map[string][]string{
		"combined": {"combined|tcp|ipv4|1.1.1.1|443|上海|电信", "combined|route|ipv4|1.1.1.1|443|上海|电信"},
		"tcp":      {"tcp|tcp|ipv4|example.com|443||"},
		"route":    {"route|route|ipv4|2.2.2.2|80|北京|联通"},
		"unlock":   {"unlock|unlock|ipv6||0||"},
	}
	for id, config := range legacy {
		if _, err = s.db.Exec("INSERT INTO tasks VALUES(?,?)", id, config); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.db.Exec("PRAGMA user_version=10"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if s, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tasks, err := s.Tasks(ctx)
	if err != nil || len(tasks) != 4 {
		t.Fatalf("tasks: %d %v", len(tasks), err)
	}
	for _, task := range tasks {
		if err := task.Validate(); err != nil {
			t.Fatalf("%s did not migrate into a valid task: %v (%+v)", task.ID, err, task)
		}
		keys := task.ProbeKeys()
		if len(keys) != len(wantKeys[task.ID]) {
			t.Fatalf("%s keys %v", task.ID, keys)
		}
		for _, k := range wantKeys[task.ID] {
			if !keys[k] {
				t.Fatalf("%s lost probe key %q (has %v)", task.ID, k, keys)
			}
		}
	}
	byID := map[string]model.Task{}
	for _, task := range tasks {
		byID[task.ID] = task
	}
	if c := byID["combined"]; c.Type != model.TaskProbe || c.Probe.Interval != 60 || c.Probe.RouteInterval != 1800 {
		t.Fatalf("combined: %+v %+v", c, c.Probe)
	}
	if r := byID["route"]; r.Enabled || r.Probe.RouteInterval != 900 || r.Probe.Interval != 0 {
		t.Fatalf("single route: %+v %+v", r, r.Probe)
	}
	if u := byID["unlock"]; u.Type != model.TaskUnlock || u.Unlock.Interval != 7200 || u.Unlock.Family != "ipv6" || u.Probe != nil {
		t.Fatalf("unlock: %+v", u)
	}
}

// Version 12 groups the node config (display / billing / line) and moves the
// server-kept creation time and cycle correction into their own columns; every
// value survives and the correction still applies to its cycle.
func TestNodeShapeMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	cycle, _ := model.Cycle(now, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	flat := fmt.Sprintf(`{"id":"n","name":"东京","group":"JP","region":"日本","order":3,"public":true,
		"representative":"上海","latency_region":"北京","latency_carriers":["电信"],
		"traffic_limit":2000,"traffic_mode":"max","billing_at":"2026-09-01T00:00:00Z","free":false,"price":9.9,"currency":"USD","price_period":"月","expires_at":"2027-01-01T00:00:00Z",
		"line_mbps":1000,"throttle_mbps":100,"throttle_hours":12,"throttle_on":"bandwidth",
		"alerts":{"offline":true,"offline_seconds":60},
		"created_at":1700000000,"traffic_adjustment":700,"adjustment_cycle":%d}`, cycle.Unix())
	if _, err = s.db.Exec("ALTER TABLE nodes DROP COLUMN created_at; ALTER TABLE nodes DROP COLUMN traffic_adjustment; ALTER TABLE nodes DROP COLUMN adjustment_cycle"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("INSERT INTO nodes(id,config,token_hash) VALUES('n',?,'h')", flat); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("PRAGMA user_version=11"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if s, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	views, err := s.Nodes(context.Background(), now)
	if err != nil || len(views) != 1 {
		t.Fatalf("nodes: %d %v", len(views), err)
	}
	v := views[0]
	want := model.Node{ID: "n", Name: "东京", Group: "JP", Region: "日本", Order: 3, Public: true,
		Display: model.NodeDisplay{RouteRegion: "上海", LatencyRegion: "北京", LatencyCarriers: []string{"电信"}},
		Billing: model.Billing{ResetAt: "2026-09-01T00:00:00Z", TrafficLimit: 2000, TrafficMode: "max", Price: 9.9, Currency: "USD", PricePeriod: "月", ExpiresAt: "2027-01-01T00:00:00Z"},
		Line:    model.Line{Mbps: 1000, ThrottleMbps: 100, ThrottleHours: 12, ThrottleOn: "bandwidth"}}
	if encode(v.Node) != encode(want) {
		t.Fatalf("node after migration:\n got %s\nwant %s", encode(v.Node), encode(want))
	}
	if got, want := ruleParams(v.Alerts), ruleParams(model.Alerts{{Kind: model.AlertOffline, HoldSeconds: 60}}); got != want {
		t.Fatalf("alerts after migration: got %s want %s", got, want)
	}
	if v.CreatedAt != 1700000000 || v.TrafficUsed != 700 {
		t.Fatalf("server-kept state lost: created=%d used=%d", v.CreatedAt, v.TrafficUsed)
	}
	var raw string
	if err = s.db.QueryRow("SELECT config FROM nodes WHERE id='n'").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "traffic_adjustment") || strings.Contains(raw, "created_at") || strings.Contains(raw, "representative") {
		t.Fatalf("config still carries the old fields: %s", raw)
	}
}

// Version 13 turns the flat alert object into the rule list: rules that were on
// keep their thresholds, the shared repeat goes onto each, the load rule splits
// into CPU and memory, and the retired load debounce state is dropped.
func TestAlertRulesMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alerts.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	config := `{"id":"n","name":"甲","group":"","region":"","order":0,"public":false,"display":{"route_region":"","latency_region":""},
		"billing":{"reset_at":"2026-09-01T00:00:00Z","traffic_limit":0,"traffic_mode":"sum","price":0,"currency":"","price_period":"","expires_at":""},
		"line":{"mbps":0,"throttle_mbps":0,"throttle_hours":0,"throttle_on":""},
		"alerts":{"offline":true,"offline_seconds":90,"repeat_seconds":600,"renewal":false,"renewal_days":7,"traffic":true,"traffic_percent":80,
		          "throttle":false,"load":true,"cpu_percent":95,"memory_percent":85,"hold_seconds":120}}`
	if _, err = s.db.Exec("INSERT INTO nodes(id,config,token_hash) VALUES('n',?,'h')", config); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("INSERT INTO alert_state VALUES('n','load',1,1),('n','offline',1,0)"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("PRAGMA user_version=12"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if s, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	views, err := s.Nodes(context.Background(), time.Now())
	if err != nil || len(views) != 1 {
		t.Fatalf("nodes: %v", err)
	}
	want := model.Alerts{
		{Kind: model.AlertOffline, HoldSeconds: 90, RepeatSeconds: 600},
		{Kind: model.AlertTraffic, Threshold: 80, RepeatSeconds: 600},
		{Kind: model.AlertCPU, Threshold: 95, HoldSeconds: 120, RepeatSeconds: 600},
		{Kind: model.AlertMemory, Threshold: 85, HoldSeconds: 120, RepeatSeconds: 600},
	}
	if ruleParams(views[0].Alerts) != ruleParams(want) {
		t.Fatalf("rules after migration:\n got %s\nwant %s", ruleParams(views[0].Alerts), ruleParams(want))
	}
	if err = views[0].Node.Validate(); err != nil {
		t.Fatalf("migrated node invalid: %v", err)
	}
	var kinds string
	if err = s.db.QueryRow("SELECT group_concat(kind) FROM alert_state WHERE node_id='n'").Scan(&kinds); err != nil || kinds != "offline" {
		t.Fatalf("alert_state kinds = %q %v", kinds, err)
	}
}

// ruleParams is the rules as kind and parameters only: what a node is alerted
// on, whatever ids and scopes the rules got.
func ruleParams(rules model.Alerts) string {
	out := model.Alerts{}
	for _, r := range rules {
		out = append(out, model.AlertRule{Kind: r.Kind, Threshold: r.Threshold, HoldSeconds: r.HoldSeconds, RepeatSeconds: r.RepeatSeconds})
	}
	return encode(out)
}

// Version 14 moves each node's own rules into the rule table. Nodes that had
// the very same rule share one node-scoped rule; a different threshold is a
// rule of its own; nothing widens to every node, and the node configs no
// longer carry alerts.
func TestAlertScopesMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scopes.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	node := func(id, alerts string) string {
		return `{"id":"` + id + `","name":"` + id + `","group":"","region":"","order":0,"public":false,"display":{"route_region":"","latency_region":""},
		"billing":{"reset_at":"2026-09-01T00:00:00Z","traffic_limit":0,"traffic_mode":"sum","price":0,"currency":"","price_period":"","expires_at":""},
		"line":{"mbps":0,"throttle_mbps":0,"throttle_hours":0,"throttle_on":""},"alerts":` + alerts + `}`
	}
	configs := map[string]string{
		"a": node("a", `[{"kind":"offline","hold_seconds":60},{"kind":"cpu","threshold":90,"hold_seconds":300}]`),
		"b": node("b", `[{"kind":"offline","hold_seconds":60},{"kind":"cpu","threshold":80,"hold_seconds":300}]`),
		"c": node("c", `[]`),
	}
	for _, id := range []string{"a", "b", "c"} {
		if _, err = s.db.Exec("INSERT INTO nodes(id,config,token_hash) VALUES(?,?,?)", id, configs[id], "h"+id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.db.Exec("DROP TABLE alert_rules; PRAGMA user_version=13"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if s, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	rules, err := s.AlertRules(ctx)
	if err != nil || len(rules) != 3 {
		t.Fatalf("rules: %+v %v", rules, err)
	}
	for _, r := range rules {
		if r.Scope != model.ScopeNodes || r.ID == "" || r.Validate() != nil {
			t.Fatalf("migrated rule: %+v", r)
		}
	}
	if fmt.Sprint(rules[0].NodeIDs) != "[a b]" || rules[0].Kind != model.AlertOffline {
		t.Fatalf("shared offline rule: %+v", rules[0])
	}
	views, _ := s.Nodes(ctx, time.Now())
	byID := map[string]model.NodeView{}
	for _, v := range views {
		byID[v.ID] = v
	}
	if cpu, _ := byID["b"].Alerts.Rule(model.AlertCPU); cpu.Threshold != 80 || len(byID["a"].Alerts) != 2 || len(byID["c"].Alerts) != 0 {
		t.Fatalf("effective: a=%+v b=%+v c=%+v", byID["a"].Alerts, byID["b"].Alerts, byID["c"].Alerts)
	}
	var left int
	if err = s.db.QueryRow("SELECT count(*) FROM nodes WHERE json_extract(config,'$.alerts') IS NOT NULL").Scan(&left); err != nil || left != 0 {
		t.Fatalf("configs still carry alerts: %d %v", left, err)
	}
}

// 迁移出来的规则都要能在面板里原样存回去：共用一条规则的节点超过 MaxRuleList 台时拆成
// 几条，版本 13 允许的 0–1% 阈值抬到 1%。
func TestAlertScopesMigrationKeepsRulesSavable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "savable.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	total := model.MaxRuleList + 1
	for i := range total {
		id := fmt.Sprintf("n%04d", i)
		alerts := `[{"kind":"offline","hold_seconds":60}]`
		if i == 0 {
			alerts = `[{"kind":"offline","hold_seconds":60},{"kind":"cpu","threshold":0.5,"hold_seconds":300}]`
		}
		config := `{"id":"` + id + `","name":"` + id + `","group":"","billing":{"reset_at":"2026-09-01T00:00:00Z"},"alerts":` + alerts + `}`
		if _, err = s.db.Exec("INSERT INTO nodes(id,config,token_hash) VALUES(?,?,?)", id, config, "h"+id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.db.Exec("DROP TABLE alert_rules; PRAGMA user_version=13"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if s, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	rules, err := s.AlertRules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	offline := 0
	for _, r := range rules {
		if err = r.Validate(); err != nil {
			t.Fatalf("migrated %s rule (%d nodes, threshold %g) cannot be saved again: %v", r.Kind, len(r.NodeIDs), r.Threshold, err)
		}
		if _, err = s.SaveAlertRule(ctx, r, false); err != nil {
			t.Fatalf("resave %s: %v", r.Kind, err)
		}
		switch r.Kind {
		case model.AlertOffline:
			offline += len(r.NodeIDs)
		case model.AlertCPU:
			if r.Threshold != 1 {
				t.Fatalf("cpu threshold: %+v", r)
			}
		}
	}
	if len(rules) != 3 || offline != total {
		t.Fatalf("rules=%d offline nodes=%d", len(rules), offline)
	}
}
