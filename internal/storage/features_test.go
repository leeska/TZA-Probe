package storage

import (
	"context"
	"github.com/leeska/TZA-Probe/internal/model"
	"path/filepath"
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
	out, err := s.RecentLatencySamples(ctx, []string{id}, 2)
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
	empty, err := s.RecentLatencySamples(ctx, nil, 20)
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
	baseline, err := s.RecentLatencySamples(ctx, []string{id}, 20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("INSERT INTO jobs(id,task_id,node_id,task,issued,finished,result) VALUES(?,?,?,?,?,?,?)",
		"routejob2", route.ID, id, encode(route), now.Add(5*time.Minute).Unix(), now.Add(5*time.Minute).Unix(), encode(model.Result{Status: "ok", Sent: 1, Received: 1, Route: "未知"})); err != nil {
		t.Fatal(err)
	}
	again, err := s.RecentLatencySamples(ctx, []string{id}, 20)
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
