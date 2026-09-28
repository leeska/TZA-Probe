package storage

import (
	"context"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

// 达量降速：窗口平均带宽必须来自小时桶（Report 每次上报把网卡计数增量累加到整点桶），
// 并且要如实报出「本机上报覆盖了多久」——新接入的机器不能假装有整个窗口的数据。
//
// 时间取整点附近、窗口取 1 小时，期望值可以手算出来：窗口起点向下取整到 11:00，
// 于是分母是 (now-11:00) 秒，分子是两个整点桶里的流量合计。
func TestThrottleWindowAverage(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	now := time.Date(2026, 9, 18, 12, 30, 0, 0, time.UTC)
	id, _ := makeNode(t, s)
	save := func(n model.Node) {
		t.Helper()
		n.ID = id
		if _, _, err := s.SaveNode(ctx, n, false); err != nil {
			t.Fatal(err)
		}
	}
	save(model.Node{Name: "node", BillingAt: "2026-09-01T00:00:00Z", TrafficMode: "sum", LineMbps: 1000, ThrottleMbps: 1, ThrottleHours: 1})

	m := metrics() // NetIn 1000, NetOut 2000
	if err := s.Report(ctx, id, m, now.Add(-90*time.Minute)); err != nil {
		t.Fatal(err)
	}
	// +675000000 字节（双向合计）= 1 Mbit/s，正好落在 1 Mbit/s 阈值上。
	m.NetIn += 600_000_000
	m.NetOut += 75_000_000
	m.Uptime += 5400
	if err := s.Report(ctx, id, m, now); err != nil {
		t.Fatal(err)
	}
	views, err := s.Nodes(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	v := views[0]
	if v.WindowHours != 1 || v.CoveredHours != 1 {
		t.Fatalf("window/covered = %d/%d", v.WindowHours, v.CoveredHours)
	}
	if v.AvgBits != 1_000_000 {
		t.Fatalf("avg = %v bit/s, want 1e6", v.AvgBits)
	}
	if !v.Throttled {
		t.Fatal("1 Mbit/s 平均值没有达到 1 Mbit/s 阈值")
	}

	// 阈值抬到 2 Mbit/s：同一份数据不再算达到降速。
	save(model.Node{Name: "node", BillingAt: "2026-09-01T00:00:00Z", TrafficMode: "sum", LineMbps: 1000, ThrottleMbps: 2, ThrottleHours: 1})
	views, err = s.Nodes(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if views[0].Throttled || views[0].AvgBits != 1_000_000 {
		t.Fatalf("threshold raise: %+v", views[0])
	}

	// 统计方式取「较大方向」时按较大方向那一边算，与本周期用量同一口径：双向合计是
	// 1 Mbit/s，较大方向（6 亿字节）只有 0.89 Mbit/s，所以区间断言能分辨出取错了方向。
	save(model.Node{Name: "node", BillingAt: "2026-09-01T00:00:00Z", TrafficMode: "max", LineMbps: 1000, ThrottleMbps: 1, ThrottleHours: 1})
	views, err = s.Nodes(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if v := views[0].AvgBits; v < 880_000 || v > 890_000 {
		t.Fatalf("max mode avg = %v bit/s, want 8*6e8/5400 ≈ 888889", v)
	}
}

// 覆盖不满窗口：24 小时窗口里只有当前这个小时有桶，CoveredHours 必须小于配置值，
// 平均按真实覆盖秒数算（1800 秒），而不是按 24 小时摊薄。
func TestThrottleWindowReportsPartialCoverage(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	now := time.Date(2026, 9, 18, 12, 30, 0, 0, time.UTC)
	id, _ := makeNode(t, s)
	if _, _, err := s.SaveNode(ctx, model.Node{ID: id, Name: "fresh", BillingAt: "2026-09-01T00:00:00Z", TrafficMode: "sum", LineMbps: 1000, ThrottleMbps: 100}, false); err != nil {
		t.Fatal(err)
	}
	m := metrics()
	if err := s.Report(ctx, id, m, now.Add(-30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	m.NetIn += 2_250_000
	m.Uptime += 1800
	if err := s.Report(ctx, id, m, now); err != nil {
		t.Fatal(err)
	}
	views, err := s.Nodes(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	v := views[0]
	if v.WindowHours != 24 || v.CoveredHours != 1 {
		t.Fatalf("window/covered = %d/%d, want 24/1", v.WindowHours, v.CoveredHours)
	}
	if v.AvgBits != 10_000 {
		t.Fatalf("avg = %v bit/s, want 10000", v.AvgBits)
	}
	if v.Throttled {
		t.Fatal("10 kbit/s 不该算达到 100 Mbit/s 阈值")
	}
}
