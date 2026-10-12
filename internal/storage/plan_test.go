package storage

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

// queryPlan 返回 EXPLAIN QUERY PLAN 的 detail 列，拼成一行。
func queryPlan(t *testing.T, s *Store, query string, args ...any) string {
	t.Helper()
	rows, err := s.db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var parts []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		parts = append(parts, detail)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(parts, " | ")
}

// 删除路径都在写锁里跑：Clean 每小时删几万行 job，每删一行触发器就要删 latest 里对应那行；
// SaveTask / DeleteTask 按 task_id 删 job。任何一条退回全表扫描，写锁就会被占住几秒，
// 所有 Agent 的上报与领任务都跟着排队。
func TestDeletePathsUseIndexes(t *testing.T) {
	s := openTest(t)
	for _, c := range []struct{ query, index string }{
		{"DELETE FROM latest WHERE job_id=?", "latest_job"},
		{"DELETE FROM jobs WHERE task_id=?", "jobs_task"},
	} {
		plan := queryPlan(t, s, c.query, "x")
		if !strings.Contains(plan, c.index) || strings.Contains(plan, "SCAN ") {
			t.Errorf("%s: plan %q, want a search on %s", c.query, plan, c.index)
		}
	}
}

// 首页每秒一轮的两条流量读路径：区间汇总要按 (node_id, 桶) 主键逐台范围查找，窗口均值
// 要按 hour 索引只扫窗口内的小时桶——不能退回 45 天小时桶的全表扫。
func TestPanelTrafficQueriesUseIndexes(t *testing.T) {
	s := openTest(t)
	for _, c := range []struct{ query, index string }{
		{"WITH r(id,a,b) AS (VALUES ('x',1,2),('y',1,2)) SELECT r.id,sum(t.incoming) FROM r JOIN traffic t ON t.node_id=r.id AND t.day>=r.a AND t.day<r.b GROUP BY r.id", "sqlite_autoindex_traffic_1"},
		{"WITH r(id,a,b) AS (VALUES ('x',1,2),('y',1,2)) SELECT r.id,sum(t.incoming) FROM r JOIN traffic_hour t ON t.node_id=r.id AND t.hour>=r.a AND t.hour<r.b GROUP BY r.id", "sqlite_autoindex_traffic_hour_1"},
		{"SELECT node_id,hour,incoming,outgoing FROM traffic_hour WHERE hour>=?", "traffic_hour_age"},
	} {
		plan := queryPlan(t, s, c.query, 1)
		if !strings.Contains(plan, "SEARCH t USING INDEX "+c.index) && !strings.Contains(plan, "SEARCH traffic_hour USING INDEX "+c.index) {
			t.Errorf("%s: plan %q, want a search on %s", c.query, plan, c.index)
		}
	}
}

// 合并成两条查询之后，每台节点的周期用量与窗口均值必须与逐台查询的旧算法逐字节一致：
// 几台节点各自的计费锚点（整日 / 非整点）、统计方式与降速窗口都不同，桶散落在窗口内外。
func TestBatchedTrafficMatchesPerNodeQueries(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	now := time.Date(2026, 9, 18, 12, 30, 0, 0, time.UTC)
	configs := []model.Node{
		{Name: "a", BillingAt: "2026-09-01T00:00:00Z", TrafficMode: "sum", ThrottleHours: 1},
		{Name: "b", BillingAt: "2026-08-17T15:17:42Z", TrafficMode: "max", ThrottleHours: 6},
		{Name: "c", BillingAt: "2026-09-18T09:00:00Z", TrafficMode: "in", ThrottleHours: 48},
		{Name: "d", BillingAt: "2026-09-05T00:00:00Z", TrafficMode: "out"}, // 默认 24 小时，没有任何桶
	}
	var ids []string
	for i, n := range configs {
		id, _ := makeNode(t, s)
		n.ID = id
		if _, _, err := s.SaveNode(ctx, n, false); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		if i == 3 {
			continue
		}
		// 小时桶：最近 72 小时里每隔几小时一个，数值互不相同，窗口边界两侧都有。
		for h := int64(0); h < 72; h += int64(i + 2) {
			hour := now.Unix()/3600*3600 - h*3600
			if _, err := s.db.Exec("INSERT INTO traffic_hour VALUES(?,?,?,?)", id, hour, 1000*(h+1)+int64(i), 777*(h+3)); err != nil {
				t.Fatal(err)
			}
		}
		for d := int64(0); d < 40; d++ {
			day := now.Unix()/86400*86400 - d*86400
			if _, err := s.db.Exec("INSERT INTO traffic VALUES(?,?,?,?)", id, day, 5_000_000*(d+1)+int64(i), 3_000_000*(d+2)); err != nil {
				t.Fatal(err)
			}
		}
	}
	views, err := s.Nodes(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != len(ids) {
		t.Fatalf("views = %d", len(views))
	}
	for _, v := range views {
		// 旧算法：逐台查日桶 + 首日小时桶切片。
		var in, out int64
		if err = s.db.QueryRow("SELECT coalesce(sum(incoming),0),coalesce(sum(outgoing),0) FROM traffic WHERE node_id=? AND day>=? AND day<?", v.ID, v.CycleStart, v.CycleEnd).Scan(&in, &out); err != nil {
			t.Fatal(err)
		}
		if from, to, ok := cycleStartSlice(v.CycleStart, v.CycleEnd); ok {
			var ein, eout int64
			if err = s.db.QueryRow("SELECT coalesce(sum(incoming),0),coalesce(sum(outgoing),0) FROM traffic_hour WHERE node_id=? AND hour>=? AND hour<?", v.ID, from, to).Scan(&ein, &eout); err != nil {
				t.Fatal(err)
			}
			in, out = in+ein, out+eout
		}
		if want := model.Traffic(in, out, v.TrafficMode); v.TrafficUsed != want {
			t.Errorf("%s traffic_used = %d, want %d", v.Name, v.TrafficUsed, want)
		}
		// 旧算法：按本节点窗口单独 GROUP BY。
		hours := v.Node.WindowHours()
		start := (now.Unix() - int64(hours)*3600) / 3600 * 3600
		span := now.Unix() - start
		var win, wout int64
		var first sql.NullInt64
		if err = s.db.QueryRow("SELECT coalesce(sum(incoming),0),coalesce(sum(outgoing),0),min(hour) FROM traffic_hour WHERE node_id=? AND hour>=?", v.ID, start).Scan(&win, &wout, &first); err != nil {
			t.Fatal(err)
		}
		var avg float64
		covered := 0
		if first.Valid {
			c := min(span, now.Unix()-first.Int64)
			avg = float64(model.Traffic(win, wout, v.TrafficMode)) * 8 / float64(c)
			covered = int((c + 3599) / 3600)
			if c >= span {
				covered = hours
			}
		}
		if v.AvgBits != avg || v.CoveredHours != covered || v.WindowHours != hours {
			t.Errorf("%s window = %v bit/s %dh/%dh, want %v bit/s %dh/%dh", v.Name, v.AvgBits, v.CoveredHours, v.WindowHours, avg, covered, hours)
		}
	}
}
