package storage

import (
	"context"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

// 回程探针出一条「未知」时，正常周期（本机 1 小时）才重测，面板上于是挂着整整一轮的未知。
// Finish 会给「上一次是好的、这一次没证据」的那种结果排一次两分钟后的补测：既让偶发抖动
// 自己好起来，又不会为了一条长期测不出的目标每两分钟烧一次 trace。
func TestRouteUnknownSchedulesOneShortRetry(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id, _ := makeNode(t, s)
	now := time.Now().Truncate(time.Second)
	task := model.Task{Name: "三网回程", Kind: "route", Host: "example.com", Family: "ipv4", Port: 80, Interval: 3600, Enabled: true, Region: "上海", Carrier: "电信"}
	if _, err := s.SaveTask(ctx, task, true); err != nil {
		t.Fatal(err)
	}
	tasks, _ := s.Tasks(ctx)
	task = tasks[0]
	task.NodeIDs = []string{id}
	if _, err := s.SaveTask(ctx, task, false); err != nil {
		t.Fatal(err)
	}
	lease := func(at time.Time) []model.Job {
		t.Helper()
		jobs, err := s.Lease(ctx, id, at)
		if err != nil {
			t.Fatalf("lease at %s: %v", at, err)
		}
		return jobs
	}
	finish := func(job model.Job, r model.Result, at time.Time) {
		t.Helper()
		r.JobID = job.ID
		if err := s.Finish(ctx, id, r, at); err != nil {
			t.Fatalf("finish at %s: %v", at, err)
		}
	}
	latency := 12.0

	// 第一轮：有骨干证据。
	jobs := lease(now)
	if len(jobs) != 1 {
		t.Fatalf("首轮没领到探针: %+v", jobs)
	}
	finish(jobs[0], model.Result{Status: "ok", Sent: 3, Received: 3, Latency: &latency, Route: "163 → 9929"}, now)
	if retries := lease(now.Add(2 * time.Minute)); len(retries) != 0 {
		t.Fatalf("好结果不该排补测: %+v", retries)
	}

	// 第二轮（周期到了）：这一条 trace 没走到目标，没有骨干证据。
	jobs = lease(now.Add(time.Hour))
	if len(jobs) != 1 {
		t.Fatalf("第二轮没领到探针: %+v", jobs)
	}
	finish(jobs[0], model.Result{Status: "timeout", Reason: "timeout", Sent: 3, Route: model.RouteUnknown}, now.Add(time.Hour))
	if early := lease(now.Add(time.Hour + time.Minute)); len(early) != 0 {
		t.Fatalf("补测提前发了: %+v", early)
	}
	jobs = lease(now.Add(time.Hour + 2*time.Minute + time.Second))
	if len(jobs) != 1 {
		t.Fatalf("补测到点没领到: %+v", jobs)
	}

	// 补测自己也没测出证据：不再续命（上一轮也是未知），下一次要等正常周期。
	finish(jobs[0], model.Result{Status: "timeout", Reason: "timeout", Sent: 3, Route: model.RouteUnknown}, now.Add(time.Hour+2*time.Minute+time.Second))
	if again := lease(now.Add(time.Hour + 3*time.Minute)); len(again) != 0 {
		t.Fatalf("补测没完没了: %+v", again)
	}
	if next := lease(now.Add(2*time.Hour + 3*time.Minute)); len(next) != 1 {
		t.Fatalf("正常周期该照常发: %+v", next)
	}
}
