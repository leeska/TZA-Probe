package storage

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
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
	start, _ := model.Cycle(now, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	if err := s.CorrectTraffic(ctx, id, 500, start.Unix()-1, now); !errors.Is(err, model.ErrConflict) {
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
	// 校正值不在配置里，保存节点（改名）无从清掉它。
	n.Name = "renamed"
	if _, _, err := s.SaveNode(ctx, n, false); err != nil {
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

// 计费锚点不在 00:00 UTC 时，周期首日只有一部分属于本周期：日桶整天存，首日那一天的桶
// 整体早于 cycle_start，会被 day>=cycle_start 整段丢掉，必须用小时桶补回锚点之后的部分。
// 补的时候有三条边界不能越：横跨锚点的那一个小时（少算可以、多算会让按额度降速的线路
// 提前告警）、锚点日的第二天（整天已在日桶里，再补就是双算）、以及锚点在整点日的节点
// （日桶本来就精确，小时桶一行都不该用）。
func TestCycleAddsTheFirstPartialDayFromHourBuckets(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	anchor := time.Date(2026, 9, 23, 12, 36, 0, 0, time.UTC)
	now := time.Date(2026, 9, 25, 6, 0, 0, 0, time.UTC)
	add := func(name, billing string, mode string) string {
		_, token, err := s.SaveNode(ctx, model.Node{Name: name, Billing: model.Billing{ResetAt: billing, TrafficMode: mode}}, true)
		if err != nil {
			t.Fatal(err)
		}
		id, err := s.Authenticate(ctx, token)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	day := func(id string, at time.Time, in, out int64) {
		if _, err := s.db.Exec("INSERT INTO traffic VALUES(?,?,?,?)", id, at.Unix(), in, out); err != nil {
			t.Fatal(err)
		}
	}
	hour := func(id string, at time.Time, in, out int64) {
		if _, err := s.db.Exec("INSERT INTO traffic_hour VALUES(?,?,?,?)", id, at.Unix(), in, out); err != nil {
			t.Fatal(err)
		}
	}

	// 锚点 12:36 的节点：首日整天（含 00:00-12:36）在日桶里、必须整段丢掉，13:00 之后的小时桶补回来。
	mid := add("mid-anchor", anchor.Format(time.RFC3339), "out")
	day(mid, time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC), 1000, 2000)  // 周期首日整天：整段不属于本周期
	day(mid, time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC), 100, 200)    // 周期内
	day(mid, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), 9999, 9999)   // 上一个周期
	hour(mid, time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC), 10, 20)    // 横跨锚点：不算
	hour(mid, time.Date(2026, 9, 23, 13, 0, 0, 0, time.UTC), 30, 40)    // 算
	hour(mid, time.Date(2026, 9, 23, 23, 0, 0, 0, time.UTC), 50, 60)    // 算
	hour(mid, time.Date(2026, 9, 24, 5, 0, 0, 0, time.UTC), 7000, 8000) // 第二天：已在日桶里，再补就是双算

	// 对照：锚点在整日边界，日桶精确，小时桶一行都不该用。
	midnight := add("midnight-anchor", "2026-09-23T00:00:00Z", "out")
	day(midnight, time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC), 1000, 2000)
	day(midnight, time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC), 100, 200)
	hour(midnight, time.Date(2026, 9, 23, 13, 0, 0, 0, time.UTC), 30, 40)

	views, err := s.Nodes(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, v := range views {
		got[v.Name] = v.TrafficUsed
	}
	// 200（09-24 日桶）+ 40 + 60（锚点之后的小时桶）
	if got["mid-anchor"] != 300 {
		t.Fatalf("锚点 12:36 的周期用量 = %d，want 300（首日只补锚点之后的小时桶）", got["mid-anchor"])
	}
	// 2000（09-23 整天）+ 200（09-24）
	if got["midnight-anchor"] != 2200 {
		t.Fatalf("锚点在整点日的周期用量 = %d，want 2200（日桶精确，不许再叠小时桶）", got["midnight-anchor"])
	}

	// 用量校正与显示同一口径：校正成多少，面板就显示多少——首日小时桶那一段不能再叠上去。
	var start int64
	for _, v := range views {
		if v.Name == "mid-anchor" {
			start = v.CycleStart
		}
	}
	if err = s.CorrectTraffic(ctx, mid, 5000, start, now); err != nil {
		t.Fatal(err)
	}
	if views, err = s.Nodes(ctx, now); err != nil {
		t.Fatal(err)
	}
	for _, v := range views {
		if v.Name == "mid-anchor" && v.TrafficUsed != 5000 {
			t.Fatalf("校正为 5000 后显示 %d（多出来的是首日小时桶那一段）", v.TrafficUsed)
		}
	}
}

func TestReadPoolServesConcurrentReadsWhileWriting(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id, _ := makeNode(t, s)
	now := time.Now()

	if _, err := s.read.ExecContext(ctx, "INSERT INTO settings VALUES('x','y')"); err == nil {
		t.Fatal("读连接必须拒绝写入（query_only）")
	}

	errs := make(chan error, 64)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Observations(ctx, id, now); err != nil {
				errs <- err
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Nodes(ctx, now); err != nil {
				errs <- err
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := s.Report(ctx, id, metrics(), now); err != nil {
			errs <- err
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	// 写连接提交的结果对读连接立即可见。
	nodes, err := s.Nodes(ctx, now)
	if err != nil || len(nodes) != 1 || nodes[0].Metrics == nil {
		t.Fatalf("读连接没看到写入: %v %+v", err, nodes)
	}
}

func TestRepeatReminderAndFailurePersistence(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id, _ := makeNode(t, s)
	now := time.Now()
	n := model.Node{ID: id, Name: "test"}
	for _, sec := range []int{0, 30, 299, 300, 301} {
		if err := s.Transition(ctx, n, "offline", text("offline"), true, 30, 300, now.Add(time.Duration(sec)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	events, _ := s.Events(ctx, model.EventQuery{})
	if len(events) != 1 {
		t.Fatal("repeat before cooldown", len(events))
	}
	if err := s.Transition(ctx, n, "offline", text("offline"), true, 30, 300, now.Add(330*time.Second)); err != nil {
		t.Fatal(err)
	}
	events, _ = s.Events(ctx, model.EventQuery{})
	if len(events) != 2 {
		t.Fatal(len(events))
	}
	if !strings.Contains(events[0].Message, "本次条件持续：5分钟30秒") {
		t.Fatal("repeat missing its duration", events[0].Message)
	}
	e := events[0]
	e.LastError = "connection failed"
	if err := s.Delivery(ctx, e, false, now); err != nil {
		t.Fatal(err)
	}
	events, _ = s.Events(ctx, model.EventQuery{})
	if events[0].LastError != e.LastError || events[0].Attempts != 1 {
		t.Fatal(events)
	}
	if err := s.Transition(ctx, n, "offline", text("offline"), false, 30, 300, now.Add(340*time.Second)); err != nil {
		t.Fatal(err)
	}
	events, _ = s.Events(ctx, model.EventQuery{})
	if len(events) != 3 || events[0].Active {
		t.Fatal("missing recovery")
	}
}

// The engine calls Transition for every node and kind every 15 seconds; a call
// that changes nothing must not write (each write is a commit under the lock
// the Agents' reports wait on).
func TestQuietTransitionWritesNothing(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id, _ := makeNode(t, s)
	n := model.Node{ID: id, Name: "test"}
	now := time.Now()
	changes := func() (c int64) {
		if err := s.db.QueryRowContext(ctx, "SELECT total_changes()").Scan(&c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	at := func(sec int) time.Time { return now.Add(time.Duration(sec) * time.Second) }
	before := changes()
	for _, sec := range []int{0, 15, 30} {
		if err := s.Transition(ctx, n, "cpu", nil, false, 30, 0, at(sec)); err != nil {
			t.Fatal(err)
		}
	}
	var rows int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM alert_state").Scan(&rows); err != nil || rows != 0 || changes() != before {
		t.Fatalf("a kind that never fired wrote state: rows=%d changes=%d→%d %v", rows, before, changes(), err)
	}
	// The condition starting is a change (since is recorded); holding below the
	// hold time is not.
	if err := s.Transition(ctx, n, "cpu", nil, true, 300, 0, at(45)); err != nil {
		t.Fatal(err)
	}
	started := changes()
	if started == before {
		t.Fatal("the start of a condition was not recorded")
	}
	if err := s.Transition(ctx, n, "cpu", nil, true, 300, 0, at(60)); err != nil {
		t.Fatal(err)
	}
	if changes() != started {
		t.Fatal("a condition still inside its hold time wrote again")
	}
}

func TestUpgradeVersionOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upgrade.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("ALTER TABLE events DROP COLUMN last_error; DROP INDEX events_rule_time; DROP TABLE latest; DROP TRIGGER jobs_latest_insert; DROP TRIGGER jobs_latest_update; DROP TRIGGER jobs_latest_delete; DROP TRIGGER jobs_tcp_bucket_insert; DROP TRIGGER jobs_tcp_bucket_finish; DROP TABLE tcp_buckets; DROP TABLE tcp_buckets_state; ALTER TABLE jobs DROP COLUMN probe_key; PRAGMA user_version=1;"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.Events(context.Background(), model.EventQuery{}); err != nil {
		t.Fatal(err)
	}
}

func TestNetworkHistorySeparatesTargetsAndNullLatency(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id, _ := makeNode(t, s)
	now := time.Now().Truncate(time.Minute)
	task := model.Probe{ID: "task", Kind: "tcp", Family: "ipv4", Host: "example.com", Port: 443, Region: "test"}
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

// 面板只显示每个目标最新一次探测与每个分组最近 N 次样本，而 jobs 保留七天：
// 读的时候必须把窗口切在 ProbeWindow 上，否则每次轮询都要把整段历史重新解析
// 一遍（线上就是这条查询让首页的延迟与回程等了好几秒）。
func TestProbeWindowSkipsHistoryThePanelCannotShow(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id, _ := makeNode(t, s)
	now := time.Now().Truncate(time.Second)

	insert := func(name, kind string, at time.Time) {
		task := model.Probe{ID: "task-" + name, Kind: kind, Family: "ipv4", Host: "example.com", Port: 443, Region: "上海", Carrier: "电信"}
		if _, err := s.db.Exec("INSERT INTO tasks VALUES(?,?) ON CONFLICT(id) DO NOTHING", task.ID, encode(task)); err != nil {
			t.Fatal(err)
		}
		latency := 30.0
		r := model.Result{Status: "ok", Sent: 3, Received: 3, Latency: &latency, Hops: []model.Hop{{TTL: 1, Address: "1.2.3.4", ASN: "AS4134"}}}
		if _, err := s.db.Exec("INSERT INTO jobs(id,task_id,node_id,task,issued,finished,result) VALUES(?,?,?,?,?,?,?)",
			"job-"+name, task.ID, id, encode(task), at.Unix(), at.Unix(), encode(r)); err != nil {
			t.Fatal(err)
		}
	}
	insert("fresh-tcp", "tcp", now.Add(-time.Minute))
	insert("fresh-route", "route", now.Add(-time.Minute))
	insert("old-tcp", "tcp", now.Add(-ProbeWindow-time.Hour))
	insert("old-route", "route", now.Add(-ProbeWindow-time.Hour))

	obs, err := s.Observations(ctx, id, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 2 {
		t.Fatalf("窗口外的观测不该出现: %+v", obs)
	}
	for _, o := range obs {
		if o.At < now.Add(-ProbeWindow).Unix() {
			t.Fatalf("窗口外的观测漏了出来: %+v", o)
		}
	}

	// 色带的窗口由调用方给（后台设置 latency_window_minutes）：面板设 1 小时时，
	// 三小时前的样本就不该再画成格子；设 24 小时时又要都在。
	insert("mid-tcp", "tcp", now.Add(-3*time.Hour))
	hour, err := s.RecentLatencySamples(ctx, []string{id}, 20, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(hour) != 1 || hour[0].At < now.Add(-time.Hour).Unix() {
		t.Fatalf("1 小时窗口只该留下最近的样本: %+v", hour)
	}
	day, err := s.RecentLatencySamples(ctx, []string{id}, 20, now.Add(-ProbeWindow))
	if err != nil {
		t.Fatal(err)
	}
	if len(day) != 2 {
		t.Fatalf("24 小时窗口该留下两条: %+v", day)
	}
}

// 公开卡片每个格子代表一次探测，所以要拿的是逐次的原始结果，按
// 节点/运营商/协议/地区分组保留最近 N 次，且失败的探测必须保留为一条记录。
func TestRecentLatencySamplesKeepsPerProbeResults(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id, _ := makeNode(t, s)
	now := time.Now().Truncate(time.Minute)
	insert := func(seq int, carrier, family string, latency *float64, sent, received int, at time.Time) {
		task := model.Probe{ID: "task", Kind: "tcp", Family: family, Host: "example.com", Port: 443, Region: "上海", Carrier: carrier}
		if _, err := s.db.Exec("INSERT INTO tasks VALUES(?,?) ON CONFLICT(id) DO NOTHING", task.ID, encode(task)); err != nil {
			t.Fatal(err)
		}
		r := model.Result{Status: "ok", Sent: sent, Received: received, Latency: latency}
		if latency == nil {
			r.Status, r.Reason = "timeout", "connect_failed"
		}
		if _, err := s.db.Exec("INSERT INTO jobs(id,task_id,node_id,task,issued,finished,result) VALUES(?,?,?,?,?,?,?)",
			"job"+string(rune('a'+seq)), task.ID, id, encode(task), at.Unix(), at.Unix(), encode(r)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		ms := 20.0 + float64(i)
		insert(i, "电信", "ipv4", &ms, 3, 3, now.Add(time.Duration(i)*time.Minute))
	}
	insert(3, "电信", "ipv4", nil, 3, 0, now.Add(3*time.Minute))
	insert(4, "电信", "ipv6", nil, 3, 3, now.Add(4*time.Minute))
	insert(5, "联通", "ipv4", nil, 3, 3, now.Add(4*time.Minute))

	// 每个分组只要最近 2 次
	out, err := s.RecentLatencySamples(ctx, []string{id}, 2, now.Add(-ProbeWindow))
	if err != nil {
		t.Fatal(err)
	}
	byGroup := map[string][]model.LatencySample{}
	for _, sample := range out {
		byGroup[sample.Carrier+"/"+sample.Family] = append(byGroup[sample.Carrier+"/"+sample.Family], sample)
	}
	if len(byGroup) != 3 {
		t.Fatalf("分组: %+v", byGroup)
	}
	dsl := byGroup["电信/ipv4"]
	// 最新两次是 22ms 那次和紧随其后的失败那次
	if len(dsl) != 2 || dsl[0].Latency == nil || *dsl[0].Latency != 22 || dsl[1].Latency != nil {
		t.Fatalf("电信/ipv4 应保留最近两次且按时间升序: %+v", dsl)
	}
	if got := dsl[1]; got.Sent != 3 || got.Received != 0 || got.Region != "上海" {
		t.Fatalf("失败的探测必须保留为一条记录: %+v", got)
	}

	// 空节点列表不应触碰数据库
	empty, err := s.RecentLatencySamples(ctx, nil, 20, now.Add(-ProbeWindow))
	if err != nil || len(empty) != 0 {
		t.Fatalf("空列表: %v %+v", err, empty)
	}
	// 回程任务不参与延迟格子
	route := model.Probe{ID: "route", Kind: "route", Family: "ipv4", Host: "example.com", Carrier: "电信", Region: "上海"}
	if _, err := s.db.Exec("INSERT INTO tasks VALUES(?,?) ON CONFLICT(id) DO NOTHING", route.ID, encode(route)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("INSERT INTO jobs(id,task_id,node_id,task,issued,finished,result) VALUES(?,?,?,?,?,?,?)",
		"routejob", route.ID, id, encode(route), now.Unix(), now.Unix(), encode(model.Result{Status: "ok", Sent: 1, Received: 1, Route: "未知"})); err != nil {
		t.Fatal(err)
	}
	// 上限相同才能比较数量
	baseline, err := s.RecentLatencySamples(ctx, []string{id}, 20, now.Add(-ProbeWindow))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("INSERT INTO jobs(id,task_id,node_id,task,issued,finished,result) VALUES(?,?,?,?,?,?,?)",
		"routejob2", route.ID, id, encode(route), now.Add(5*time.Minute).Unix(), now.Add(5*time.Minute).Unix(), encode(model.Result{Status: "ok", Sent: 1, Received: 1, Route: "未知"})); err != nil {
		t.Fatal(err)
	}
	again, err := s.RecentLatencySamples(ctx, []string{id}, 20, now.Add(-ProbeWindow))
	if err != nil {
		t.Fatal(err)
	}
	for _, sample := range again {
		if sample.NodeID != id {
			t.Fatalf("越权节点: %+v", sample)
		}
	}
	if len(again) != len(baseline) {
		t.Fatalf("回程任务不应进入延迟采样: %d vs %d", len(again), len(baseline))
	}
}

// 面板的延迟色带时间窗上限就是探测结果的保留期：改清理策略时必须同步这个上限，
// 否则设置里能选到一个永远取不到更早数据的窗口。
func TestLatencyWindowCeilingMatchesJobsRetention(t *testing.T) {
	if got := int(JobsRetention.Minutes()); got != model.MaxLatencyWindowMinutes {
		t.Fatalf("jobs 保留 %d 分钟，面板上限 %d 分钟，两者必须一致", got, model.MaxLatencyWindowMinutes)
	}
}

// text is a fixed event text for Transition, whatever edge it describes.
func text(message string) func(bool, bool) string {
	return func(bool, bool) string { return message }
}
