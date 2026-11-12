package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

// TestPerfProfile 在一份接近线上规模的数据上量各条热路径的耗时，只在 TZA_PERF=1 时跑：
// 10 台节点、每台 8 个 TCP 目标（30 秒）+ 8 个回程（1 小时）、8 天探测结果（第 8 天供
// Clean 删）、30 天分钟样本、45 天小时桶。造数据要几分钟：设 TZA_PERF_DB=/path/seed.db
// 时造好的库留在那里，下次直接复制一份来量（量的过程会删改数据，原件不动）。
func TestPerfProfile(t *testing.T) {
	if os.Getenv("TZA_PERF") == "" {
		t.Skip("set TZA_PERF=1")
	}
	ctx := context.Background()
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	base := os.Getenv("TZA_PERF_DB")
	if base == "" {
		base = filepath.Join(t.TempDir(), "seed.db")
	}
	if _, err := os.Stat(base); err != nil {
		seedPerf(t, base, now)
	}
	path := filepath.Join(t.TempDir(), "perf.db")
	copyFile(t, base, path)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var ids []string
	rows, err := s.db.Query("SELECT id FROM nodes ORDER BY json_extract(config,'$.name')")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	tasks, err := s.Tasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var task model.Task
	var taskID, unlockID string
	for _, x := range tasks {
		if x.Type == model.TaskUnlock {
			unlockID = x.ID
		} else {
			task, taskID = x, x.ID
		}
	}
	latency := 42.5

	measure := func(name string, runs int, f func() error) {
		t.Helper()
		var times []time.Duration
		for i := 0; i < runs; i++ {
			start := time.Now()
			if err := f(); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			times = append(times, time.Since(start))
		}
		sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
		t.Logf("%-34s median %9v  max %9v", name, times[len(times)/2].Round(10*time.Microsecond), times[len(times)-1].Round(10*time.Microsecond))
	}
	node := ids[0]
	measure("Nodes", 20, func() error { _, err := s.Nodes(ctx, now); return err })
	measure("Observations (1 node)", 20, func() error { _, err := s.Observations(ctx, node, now); return err })
	measure("Observations (all nodes)", 10, func() error {
		for _, id := range ids {
			if _, err := s.Observations(ctx, id, now); err != nil {
				return err
			}
		}
		return nil
	})
	for _, window := range []time.Duration{time.Hour, 24 * time.Hour, 7 * 24 * time.Hour} {
		w := window
		measure(fmt.Sprintf("RecentLatencySamples %v", w), 5, func() error {
			_, err := s.RecentLatencySamples(ctx, ids, 20, now.Add(-w))
			return err
		})
	}
	measure("NetworkHistory 24h", 5, func() error { _, err := s.NetworkHistory(ctx, node, 24, now); return err })
	measure("NetworkHistory 168h", 3, func() error { _, err := s.NetworkHistory(ctx, node, 168, now); return err })
	measure("History 24h", 10, func() error { _, err := s.History(ctx, node, 24, now); return err })
	measure("History 720h", 5, func() error { _, err := s.History(ctx, node, 720, now); return err })
	measure("TaskRuns", 5, func() error { _, err := s.TaskRuns(ctx, taskID, now); return err })
	measure("Tasks", 20, func() error { _, err := s.Tasks(ctx); return err })
	measure("Events", 20, func() error { _, err := s.PendingEvents(ctx, now); return err })

	tick := now
	m := capableMetrics()
	measure("Report", 50, func() error {
		tick = tick.Add(5 * time.Second)
		m.NetIn += 1e6
		m.Uptime += 5
		return s.Report(ctx, node, m, tick)
	})
	var leased []model.Job
	measure("Lease", 20, func() error {
		tick = tick.Add(31 * time.Second)
		jobs, err := s.Lease(ctx, node, tick)
		leased = append(leased, jobs...)
		return err
	})
	measure("Finish", min(20, len(leased)), func() error {
		j := leased[0]
		leased = leased[1:]
		r := model.Result{JobID: j.ID, Status: "ok", Latency: &latency, Sent: 3, Received: 3}
		if j.Task.Kind == model.UnlockKind {
			r = model.Result{JobID: j.ID, Status: "ok", Unlocks: []model.UnlockResult{{Service: "reddit", Status: model.UnlockYes}}}
		}
		err := s.Finish(ctx, node, r, tick)
		if errors.Is(err, ErrConflict) {
			return nil
		}
		return err
	})
	// 大批删除时 Agent 的写入要能插进来：一边删，一边每 5 秒的节奏上报，看最慢的一次。
	reportDuring := func(name string, work func() error) {
		t.Helper()
		done := make(chan error, 1)
		start := time.Now()
		go func() { done <- work() }()
		var worst time.Duration
		reports := 0
		other := ids[1]
		om := capableMetrics()
		for running := true; running; {
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				running = false
			default:
				tick = tick.Add(5 * time.Second)
				om.Uptime += 5
				began := time.Now()
				if err := s.Report(ctx, other, om, tick); err != nil {
					t.Fatalf("report during %s: %v", name, err)
				}
				worst = max(worst, time.Since(began))
				reports++
				time.Sleep(20 * time.Millisecond)
			}
		}
		t.Logf("%-34s total %9v  %d reports alongside, slowest %v", name, time.Since(start).Round(time.Millisecond), reports, worst.Round(10*time.Microsecond))
	}
	reportDuring("Clean (1 day of jobs expires)", func() error { return s.Clean(ctx, now) })
	s.stopPurger()
	measure("SaveTask (edit; history queued)", 1, func() error { _, err := s.SaveTask(ctx, task, false); return err })
	reportDuring("  └ background purge of the edit", func() error { return s.runPurges(ctx) })
	measure("DeleteNode (history queued)", 1, func() error { return s.DeleteNode(ctx, ids[2]) })
	reportDuring("  └ background purge of the node", func() error { return s.runPurges(ctx) })
	measure("DeleteTask (history queued)", 1, func() error { return s.DeleteTask(ctx, unlockID) })
}

// seedPerf writes the profile's dataset to path.
func seedPerf(t *testing.T, path string, now time.Time) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const nodes, targets = 10, 8
	carriers := []string{"电信", "联通", "移动"}

	var ids []string
	for i := 0; i < nodes; i++ {
		id, _ := makeNode(t, s)
		n := model.Node{ID: id, Name: fmt.Sprintf("node-%d", i), Public: true, Billing: model.Billing{ResetAt: "2026-09-23T12:36:00Z", TrafficMode: "sum"}, Line: model.Line{ThrottleMbps: 100, ThrottleHours: 24 * (i%3 + 1)}}
		if _, _, err := s.SaveNode(ctx, n, false); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	task := model.Task{Name: "三网", Enabled: true, NodeIDs: ids, Type: model.TaskProbe, Probe: &model.ProbeSpec{Checks: []string{"tcp", "route"}, Interval: 30, RouteInterval: 3600}}
	for i := 0; i < targets; i++ {
		task.Probe.Targets = append(task.Probe.Targets, model.Target{Host: fmt.Sprintf("10.0.%d.1", i), Port: 80, Family: "ipv4", Region: fmt.Sprintf("地区%d", i), Carrier: carriers[i%3]})
	}
	taskID, err := s.SaveTask(ctx, task, true)
	if err != nil {
		t.Fatal(err)
	}
	task.ID = taskID
	unlockID, err := s.SaveTask(ctx, model.Task{Name: "解锁", Enabled: true, NodeIDs: ids, Type: model.TaskUnlock, Unlock: &model.UnlockSpec{Interval: 3600}}, true)
	if err != nil {
		t.Fatal(err)
	}

	seedStart := time.Now()
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	insertJob, _ := tx.Prepare("INSERT INTO jobs(id,task_id,node_id,task,issued,finished,result,probe_key) VALUES(?,?,?,?,?,?,?,?)")
	insertSample, _ := tx.Prepare("INSERT INTO samples VALUES(?,?,?)")
	insertHour, _ := tx.Prepare("INSERT INTO traffic_hour VALUES(?,?,?,?)")
	insertDay, _ := tx.Prepare("INSERT INTO traffic VALUES(?,?,?,?)")
	hops := make([]model.Hop, 24)
	for i := range hops {
		hops[i] = model.Hop{TTL: i + 1, Address: fmt.Sprintf("202.97.%d.0/24", i), ASN: "4134", RTT: float64(i * 3)}
	}
	latency := 42.5
	tcpResult := encode(model.Result{Status: "ok", Latency: &latency, Sent: 3, Received: 3, Hops: []model.Hop{}})
	routeResult := encode(model.Result{Status: "ok", Latency: &latency, Sent: 1, Received: 1, Route: "CN2GIA", Hops: hops})
	payload := encode(capableMetrics())
	jobs := 0
	from := now.Add(-8 * 24 * time.Hour).Unix()
	for _, node := range ids {
		for at := now.Add(-30 * 24 * time.Hour).Unix(); at < now.Unix(); at += 60 {
			if _, err = insertSample.Exec(node, at, payload); err != nil {
				t.Fatal(err)
			}
		}
		for at := now.Add(-45*24*time.Hour).Unix() / 3600 * 3600; at < now.Unix(); at += 3600 {
			if _, err = insertHour.Exec(node, at, 4e9, 1e9); err != nil {
				t.Fatal(err)
			}
		}
		for at := now.Add(-400*24*time.Hour).Unix() / 86400 * 86400; at < now.Unix(); at += 86400 {
			if _, err = insertDay.Exec(node, at, 9e10, 2e10); err != nil {
				t.Fatal(err)
			}
		}
		if _, err = tx.Exec("UPDATE nodes SET metrics=?,last_seen=? WHERE id=?", payload, now.Unix()-10, node); err != nil {
			t.Fatal(err)
		}
		for _, svc := range model.UnlockServiceIDs() {
			if _, err = tx.Exec("INSERT INTO unlocks VALUES(?,?,?,?,?,?)", node, svc, model.UnlockYes, "US", "", now.Unix()); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// 探测结果按时间顺序写，各节点、各目标交错——与线上的写入顺序一致，页面布局才像线上
	// （按节点、按目标成块写会把每个目标的行摊满整棵索引树，删起来比线上慢一个数量级）。
	// 每 5 万行提交一次，免得一个事务把 WAL 撑到一两个 G。
	leaves := task.Leaves()
	encoded := make([]string, len(leaves))
	for i, leaf := range leaves {
		encoded[i] = encode(leaf)
	}
	for at := from; at < now.Unix(); at += 30 {
		if jobs%50000 < len(ids)*len(leaves) {
			if tx != nil && jobs > 0 {
				if err = tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			if tx, err = s.db.Begin(); err != nil {
				t.Fatal(err)
			}
			insertJob, _ = tx.Prepare("INSERT INTO jobs(id,task_id,node_id,task,issued,finished,result,probe_key) VALUES(?,?,?,?,?,?,?,?)")
		}
		for _, node := range ids {
			for i, leaf := range leaves {
				result := tcpResult
				if leaf.Kind == "route" {
					if (at-from)%3600 != 0 {
						continue
					}
					result = routeResult
				}
				if _, err = insertJob.Exec(jobID(time.Unix(at, 0)), taskID, node, encoded[i], at, at+2, result, leaf.ProbeKey()); err != nil {
					t.Fatal(err)
				}
				jobs++
			}
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	t.Logf("seeded %d jobs in %v", jobs, time.Since(seedStart).Round(time.Millisecond))
	_ = unlockID

}

// copyFile streams the seeded database into place: it is over a gigabyte, and
// reading it into memory first pushed an 8GB machine into swap, which made the
// timings worthless.
func copyFile(t *testing.T, from, to string) {
	t.Helper()
	src, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	if err = dst.Close(); err != nil {
		t.Fatal(err)
	}
}
