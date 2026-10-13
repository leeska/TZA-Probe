package storage

import (
	"context"
	"testing"
	"time"
)

// 网络历史格子跟着历史一起删：改任务（含旧结果的格子）、删节点、保留期。
func TestTCPBucketsFollowHistoryDeletes(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	nodes, taskID := seedReadPaths(t, s, now)
	count := func(where string, args ...any) int {
		t.Helper()
		var n int
		if err := s.db.QueryRow("SELECT count(*) FROM tcp_buckets WHERE "+where, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count("node_id=?", nodes[0]) == 0 || count("node_id=?", nodes[1]) == 0 {
		t.Fatal("seed built no buckets")
	}

	if err := s.DeleteNode(ctx, nodes[0]); err != nil {
		t.Fatal(err)
	}
	if err := s.runPurges(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count("node_id=?", nodes[0]); n != 0 {
		t.Fatalf("%d buckets of a deleted node left", n)
	}

	tasks, err := s.Tasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if task.ID == taskID {
			task.Probe.Targets[0].Host = "10.0.0.9" // 改了探测内容，历史清掉
			task.NodeIDs = nodes[1:2]
			if _, err = s.SaveTask(ctx, task, false); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.runPurges(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count("task_id=?", taskID); n != 0 {
		t.Fatalf("%d buckets of the edited task's old results left", n)
	}
	if series, err := s.NetworkHistory(ctx, nodes[1], 24, now); err != nil || len(series) != 0 {
		t.Fatalf("history after the edit: %v %v", series, err)
	}

	// 保留期：重新放一格进去，Clean 到它过期之后。
	if _, err := s.db.Exec("INSERT INTO tcp_buckets(node_id,bucket,probe_key,task_id,job_min,finished,lat_sum,lat_n,sent,received,task) VALUES(?,?,?,?,0,?,0,0,0,0,'{}')", nodes[1], now.Unix()-now.Unix()%tcpBucketSeconds, "k", taskID, now.Unix()); err != nil {
		t.Fatal(err)
	}
	if err := s.Clean(ctx, now.Add(JobsRetention)); err != nil {
		t.Fatal(err)
	}
	if count("1") != 1 {
		t.Fatal("a bucket inside the retention was cleaned")
	}
	if err := s.Clean(ctx, now.Add(JobsRetention+time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := count("1"); n != 0 {
		t.Fatalf("%d expired buckets left", n)
	}
}
