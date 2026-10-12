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

// 按周期额度降速：判据是「本周期用量顶到额度」，与窗口平均带宽无关——同一份数据在两条
// 规则下结论必须不同（额度模式的线路降速、带宽模式的线路不降速），断言才咬得住。
func TestThrottleOnQuotaFollowsCycleVolume(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	now := time.Date(2026, 9, 18, 12, 30, 0, 0, time.UTC)
	base := func(mode string) string {
		t.Helper()
		id, _ := makeNode(t, s)
		n := model.Node{ID: id, Name: "dmit", BillingAt: "2026-09-01T00:00:00Z", TrafficMode: "sum",
			TrafficLimit: 1_000_000, LineMbps: 4000, ThrottleMbps: 50, ThrottleOn: mode}
		if _, _, err := s.SaveNode(ctx, n, false); err != nil {
			t.Fatal(err)
		}
		return id
	}
	// 额度 1 MB：先建基线，再让双向合计超一点（600 KB + 500 KB = 1.1 MB）。
	quotaID, bwID := base("quota"), base("bandwidth")
	for _, id := range []string{quotaID, bwID} {
		m := metrics()
		if err := s.Report(ctx, id, m, now.Add(-2*time.Minute)); err != nil {
			t.Fatal(err)
		}
		m.NetIn += 600_000
		m.NetOut += 500_000
		if err := s.Report(ctx, id, m, now); err != nil {
			t.Fatal(err)
		}
	}
	views, err := s.Nodes(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]model.NodeView{}
	for _, v := range views {
		byName[v.ID] = v
	}
	q, bw := byName[quotaID], byName[bwID]
	if q.TrafficUsed < q.TrafficLimit {
		t.Fatalf("用量 %d 没到额度 %d，用例前提不成立", q.TrafficUsed, q.TrafficLimit)
	}
	if !q.Throttled {
		t.Fatalf("用满额度不算降速：%+v", q)
	}
	if bw.Throttled {
		t.Fatalf("带宽模式的线路不该因为用满额度而降速（均值只有 %v bit/s）：%+v", bw.AvgBits, bw)
	}

	// 额度抬到 100 MB：同一份用量不再算降速。
	n := q.Node
	n.TrafficLimit = 100_000_000
	if _, _, err = s.SaveNode(ctx, n, false); err != nil {
		t.Fatal(err)
	}
	views, err = s.Nodes(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range views {
		if v.ID == quotaID && v.Throttled {
			t.Fatalf("额度 100 MB 时 %d 字节不该算用满", v.TrafficUsed)
		}
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

// Agent 停了两天、主机没重启：重连那一次的计数增量属于这两天，按时长摊进经过的每个小时桶与
// 天桶，而不是全部记进重连的那一小时（那样 1 小时窗口的均值会暴涨成两天流量的 48 倍）。
func TestReportSpreadsAGapOverItsBuckets(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	id, _ := makeNode(t, s)
	start := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	m := metrics()
	if err := s.Report(ctx, id, m, start); err != nil {
		t.Fatal(err)
	}
	m.NetIn += 48 * 3_600_000 // 两天，每小时 3.6 MB
	m.NetOut += 48 * 7_200
	m.Uptime += 48 * 3600
	now := start.Add(48 * time.Hour)
	if err := s.Report(ctx, id, m, now); err != nil {
		t.Fatal(err)
	}
	var hours, sumIn, sumOut, maxIn int64
	if err := s.db.QueryRow("SELECT count(*),sum(incoming),sum(outgoing),max(incoming) FROM traffic_hour WHERE node_id=? AND incoming>0", id).Scan(&hours, &sumIn, &sumOut, &maxIn); err != nil {
		t.Fatal(err)
	}
	if hours != 48 || sumIn != 48*3_600_000 || sumOut != 48*7_200 || maxIn != 3_600_000 {
		t.Fatalf("小时桶 %d 个，合计 %d/%d，最大 %d；want 48 个、每个 3600000", hours, sumIn, sumOut, maxIn)
	}
	var days, dayIn int64
	if err := s.db.QueryRow("SELECT count(*),sum(incoming) FROM traffic WHERE node_id=? AND incoming>0", id).Scan(&days, &dayIn); err != nil {
		t.Fatal(err)
	}
	// 9-16 12:00 → 9-18 12:00：三个自然日（12 + 24 + 12 小时）。
	if days != 3 || dayIn != 48*3_600_000 {
		t.Fatalf("天桶 %d 个，合计 %d", days, dayIn)
	}
}

func TestSpreadKeepsTheTotalExact(t *testing.T) {
	for _, c := range []struct{ from, to, size, in, out int64 }{
		{100, 100, 3600, 7, 9},                 // 零长度：整段进 to 所在的桶
		{3590, 3605, 3600, 1001, 3},            // 跨一个整点
		{0, 10*86400 + 17, 3600, 1<<62 + 5, 1}, // 大数值：取整误差都归最后一个桶
		{0, 400 * 86400, 3600, 1e12, 1e12},     // 超过 maxSpread 的缺口只摊最后 45 天
	} {
		pieces := spread(c.from, c.to, c.size, c.in, c.out)
		var in, out int64
		for i, p := range pieces {
			if p.in < 0 || p.out < 0 || p.at%c.size != 0 || (i > 0 && p.at <= pieces[i-1].at) {
				t.Fatalf("%+v: bad piece %+v", c, p)
			}
			in, out = in+p.in, out+p.out
		}
		if in != c.in || out != c.out {
			t.Fatalf("%+v: total %d/%d", c, in, out)
		}
		if n := int64(len(pieces)); n > int64(maxSpread.Seconds())/c.size+2 {
			t.Fatalf("%+v: %d pieces", c, n)
		}
	}
}
