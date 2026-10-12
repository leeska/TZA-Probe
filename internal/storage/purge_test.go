package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

// purgeFixture 是一台有历史的节点：一个已完成过几轮的 TCP 任务、分钟样本、流量桶、告警状态。
func purgeFixture(t *testing.T, s *Store, now time.Time) (node, token string, task model.Task) {
	t.Helper()
	ctx := context.Background()
	node, token = makeNode(t, s)
	task = model.Task{Name: "t", Kind: "tcp", Family: "ipv4", Host: "example.com", Port: 443, Region: "上海", Carrier: "电信", Interval: 30, Enabled: true, NodeIDs: []string{node}}
	id, err := s.SaveTask(ctx, task, true)
	if err != nil {
		t.Fatal(err)
	}
	task.ID = id
	m := metrics()
	for i := 0; i < 5; i++ {
		at := now.Add(time.Duration(i-5) * time.Minute)
		m.NetIn += 1000
		m.Uptime += 60
		if err = s.Report(ctx, node, m, at); err != nil {
			t.Fatal(err)
		}
		jobs, err := s.Lease(ctx, node, at)
		if err != nil || len(jobs) != 1 {
			t.Fatalf("lease: %+v %v", jobs, err)
		}
		if err = s.Finish(ctx, node, model.Result{JobID: jobs[0].ID, Status: "ok", Sent: 3, Received: 3, Latency: millis(10)}, at.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Transition(ctx, model.Node{ID: node, Name: "node"}, "load", "x", true, 0, now); err != nil {
		t.Fatal(err)
	}
	return node, token, task
}

func rowCount(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// 外键完整：分批删完之后不留孤儿，写连接上的外键约束也已恢复。
func assertForeignKeysIntact(t *testing.T, s *Store) {
	t.Helper()
	if n := rowCount(t, s, "SELECT count(*) FROM pragma_foreign_key_check"); n != 0 {
		t.Fatalf("%d 行违反外键", n)
	}
	if _, err := s.db.Exec("INSERT INTO bindings(task_id,node_id) VALUES('no-such-task','no-such-node')"); err == nil {
		t.Fatal("写连接上的外键约束没有恢复")
	}
}

func TestDeleteNodeIsImmediateAndPurgesHistoryInTheBackground(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	now := time.Now().Truncate(time.Second)
	node, token, _ := purgeFixture(t, s, now)
	if rowCount(t, s, "SELECT count(*) FROM jobs WHERE node_id=?", node) == 0 || rowCount(t, s, "SELECT count(*) FROM samples WHERE node_id=?", node) == 0 {
		t.Fatal("fixture has no history")
	}
	if err := s.DeleteNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	// 立刻生效：节点不见了，密钥失效，小表跟着清掉。
	if _, err := s.Authenticate(ctx, token); err == nil {
		t.Fatal("deleted node still authenticates")
	}
	if _, err := s.IsPublic(ctx, node); err != ErrNotFound {
		t.Fatalf("IsPublic = %v", err)
	}
	for _, table := range []string{"bindings", "alert_state", "latest", "traffic", "traffic_hour"} {
		if n := rowCount(t, s, "SELECT count(*) FROM "+table+" WHERE node_id=?", node); n != 0 {
			t.Fatalf("%s 还有 %d 行", table, n)
		}
	}
	if err := s.DeleteNode(ctx, node); err != ErrNotFound {
		t.Fatalf("second delete = %v", err)
	}
	drainPurges(t, s)
	for _, table := range []string{"jobs", "samples"} {
		if n := rowCount(t, s, "SELECT count(*) FROM "+table+" WHERE node_id=?", node); n != 0 {
			t.Fatalf("%s 还有 %d 行", table, n)
		}
	}
	if n := rowCount(t, s, "SELECT count(*) FROM purges"); n != 0 {
		t.Fatalf("purge queue left %d", n)
	}
	assertForeignKeysIntact(t, s)
}

func TestDeleteTaskIsImmediateAndPurgesHistoryInTheBackground(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	now := time.Now().Truncate(time.Second)
	node, _, task := purgeFixture(t, s, now)
	if err := s.DeleteTask(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if tasks, err := s.Tasks(ctx); err != nil || len(tasks) != 0 {
		t.Fatalf("tasks = %+v %v", tasks, err)
	}
	if obs, err := s.Observations(ctx, node, now); err != nil || len(obs) != 0 {
		t.Fatalf("observations = %+v %v", obs, err)
	}
	// 删完之前网络历史也不再画已删任务。
	if series, err := s.NetworkHistory(ctx, node, 24, now); err != nil || len(series) != 0 {
		t.Fatalf("network history = %+v %v", series, err)
	}
	if n := rowCount(t, s, "SELECT count(*) FROM bindings WHERE task_id=?", task.ID); n != 0 {
		t.Fatalf("bindings %d", n)
	}
	drainPurges(t, s)
	if n := rowCount(t, s, "SELECT count(*) FROM jobs WHERE task_id=?", task.ID); n != 0 {
		t.Fatalf("jobs %d", n)
	}
	assertForeignKeysIntact(t, s)
}

// 改任务：面板立刻看不到旧结果，旧历史后台删掉；改完之后新领走的任务不在删除范围里。
func TestEditPurgesOnlyTheHistoryBeforeTheEdit(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	now := time.Now().Truncate(time.Second)
	node, _, task := purgeFixture(t, s, now)
	before := rowCount(t, s, "SELECT count(*) FROM jobs WHERE task_id=?", task.ID)
	s.stopPurger() // 让历史留到下面手动删，才能证明删除有界
	edited := task
	edited.Host = "moved.example.com"
	if _, err := s.SaveTask(ctx, edited, false); err != nil {
		t.Fatal(err)
	}
	if obs, err := s.Observations(ctx, node, now); err != nil || len(obs) != 0 {
		t.Fatalf("改任务后还能看到旧观测: %+v %v", obs, err)
	}
	if n := rowCount(t, s, "SELECT count(*) FROM jobs WHERE task_id=?", task.ID); n != before {
		t.Fatalf("历史该留给后台删：%d → %d", before, n)
	}
	jobs, err := s.Lease(ctx, node, now.Add(time.Second))
	if err != nil || len(jobs) != 1 || jobs[0].Task.Host != "moved.example.com" {
		t.Fatalf("改完立刻能按新配置领任务: %+v %v", jobs, err)
	}
	drainPurges(t, s)
	if n := rowCount(t, s, "SELECT count(*) FROM jobs WHERE task_id=?", task.ID); n != 1 {
		t.Fatalf("改完之后领走的那条必须留下，剩 %d 条", n)
	}
}

// 删到一半重启：purges 是持久的，重新打开后接着删完。
func TestPurgeResumesAfterRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "resume.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	node, _, _ := purgeFixture(t, s, now)
	s.stopPurger()
	if err = s.DeleteNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	if n := rowCount(t, s, "SELECT count(*) FROM purges"); n != 1 {
		t.Fatalf("queue %d", n)
	}
	s.purgeStop = nil // 已经停过
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	deadline := time.Now().Add(5 * time.Second)
	for rowCount(t, s, "SELECT count(*) FROM purges") != 0 {
		if time.Now().After(deadline) {
			t.Fatal("purge did not resume")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := rowCount(t, s, "SELECT count(*) FROM jobs WHERE node_id=?", node); n != 0 {
		t.Fatalf("jobs %d", n)
	}
}
