package storage

import (
	"context"
	"github.com/leeska/TZA-Probe/internal/model"
	"path/filepath"
	"sync"
	"testing"
	"time"
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
	if err := s.CorrectTraffic(ctx, id, 500, start.Unix()-1, now); err != model.ErrConflict {
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
	n.Name = "renamed"
	n.TrafficAdjustment = 0
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

// 读连接池与写连接并存：读要能并发，写要在读连接上被拒。
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
	n := model.Node{ID: id, Name: "test", Alerts: model.Alerts{RepeatSeconds: 300}}
	for _, sec := range []int{0, 30, 299, 300, 301} {
		if err := s.Transition(ctx, n, "offline", "offline", true, 30, now.Add(time.Duration(sec)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	events, _ := s.Events(ctx, false, now)
	if len(events) != 1 {
		t.Fatal("repeat before cooldown", len(events))
	}
	if err := s.Transition(ctx, n, "offline", "offline", true, 30, now.Add(330*time.Second)); err != nil {
		t.Fatal(err)
	}
	events, _ = s.Events(ctx, false, now)
	if len(events) != 2 {
		t.Fatal(len(events))
	}
	e := events[0]
	e.LastError = "connection failed"
	if err := s.Delivery(ctx, e, false, now); err != nil {
		t.Fatal(err)
	}
	events, _ = s.Events(ctx, false, now)
	if events[0].LastError != e.LastError || events[0].Attempts != 1 {
		t.Fatal(events)
	}
	if err := s.Transition(ctx, n, "offline", "offline", false, 30, now.Add(340*time.Second)); err != nil {
		t.Fatal(err)
	}
	events, _ = s.Events(ctx, false, now)
	if len(events) != 3 || events[0].Active {
		t.Fatal("missing recovery")
	}
}

func TestUpgradeVersionOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upgrade.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("ALTER TABLE events DROP COLUMN last_error; DROP INDEX events_rule_time; PRAGMA user_version=1;"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.Events(context.Background(), false, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestNetworkHistorySeparatesTargetsAndNullLatency(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id, _ := makeNode(t, s)
	now := time.Now().Truncate(time.Minute)
	task := model.Task{ID: "task", Kind: "tcp", Family: "ipv4", Host: "example.com", Port: 443, Region: "test"}
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
		task := model.Task{ID: "task-" + name, Kind: kind, Family: "ipv4", Host: "example.com", Port: 443, Region: "上海", Carrier: "电信"}
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
		task := model.Task{ID: "task", Kind: "tcp", Family: family, Host: "example.com", Port: 443, Region: "上海", Carrier: carrier}
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
	route := model.Task{ID: "route", Kind: "route", Family: "ipv4", Host: "example.com", Carrier: "电信", Region: "上海"}
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
