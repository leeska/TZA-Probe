package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"net"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

// oldRecentLatencySQL 是改写前的延迟样本读路径：窗口内全部 TCP 结果按
// (节点, 运营商, 协议, 地区) 分组取 finished 最新的 N 条。留作基准。
const oldRecentLatencySQL = `
	SELECT node_id, carrier, region, family, at, latency, sent, received FROM (
		SELECT node_id,
		       json_extract(task,'$.carrier') AS carrier,
		       json_extract(task,'$.region') AS region,
		       json_extract(task,'$.family') AS family,
		       finished AS at,
		       json_extract(result,'$.latency') AS latency,
		       json_extract(result,'$.sent') AS sent,
		       json_extract(result,'$.received') AS received,
		       row_number() OVER (
		         PARTITION BY node_id, json_extract(task,'$.carrier'), json_extract(task,'$.family'), json_extract(task,'$.region')
		         ORDER BY finished DESC, id DESC
		       ) AS n
		FROM jobs
		WHERE finished IS NOT NULL AND json_extract(task,'$.kind')='tcp' AND issued>=?
		  AND node_id IN (%s)
	) WHERE n <= ? ORDER BY at, node_id, carrier`

// oldTaskRunsSQL 是改写前的执行记录读路径（每个绑定节点 × 每个探测目标的最新一次）。
const oldTaskRunsSQL = `SELECT n.id,json_extract(n.config,'$.name'),j.issued,j.finished,j.task,j.result FROM bindings b JOIN nodes n ON n.id=b.node_id LEFT JOIN jobs j ON j.id IN (SELECT id FROM (SELECT id,row_number() OVER(PARTITION BY json_extract(task,'$.kind'),json_extract(task,'$.family'),json_extract(task,'$.host'),json_extract(task,'$.port'),json_extract(task,'$.region'),json_extract(task,'$.carrier') ORDER BY issued DESC,id DESC) pos FROM jobs WHERE task_id=b.task_id AND node_id=b.node_id) WHERE pos=1) WHERE b.task_id=? ORDER BY n.id,j.issued DESC`

// seedReadPaths 写一份打乱的探测历史：多个目标落在同一分组、结果晚到乱序、失败、
// 从未完成、回程混在其中、一部分旧行没有 probe_key（迁移前的写法）。
func seedReadPaths(t *testing.T, s *Store, now time.Time) (nodes []string, taskID string) {
	t.Helper()
	ctx := context.Background()
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 3; i++ {
		id, _ := makeNode(t, s)
		nodes = append(nodes, id)
	}
	task := model.Task{Name: "混合", Enabled: true, NodeIDs: nodes[:2], Type: model.TaskProbe, Probe: &model.ProbeSpec{Checks: []string{"tcp", "route"}, Interval: 30, RouteInterval: 900}}
	// 两个电信目标落在同一分组（同地区、同协议族），联通单独一组。
	for i, c := range []string{"电信", "电信", "联通"} {
		task.Probe.Targets = append(task.Probe.Targets, model.Target{Host: fmt.Sprintf("10.0.0.%d", i+1), Port: 80, Family: "ipv4", Region: "上海", Carrier: c})
	}
	var err error
	if taskID, err = s.SaveTask(ctx, task, true); err != nil {
		t.Fatal(err)
	}
	task.ID = taskID
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	seq := 0
	for _, node := range nodes {
		for _, leaf := range task.Leaves() {
			step := int64(30)
			if leaf.Kind == "route" {
				step = 600
			}
			for at := now.Add(-90 * time.Minute).Unix(); at < now.Unix(); at += step + int64(rng.Intn(5)) {
				seq++
				key := leaf.ProbeKey()
				if rng.Intn(10) == 0 {
					key = "" // 迁移前的行
				}
				var finished sql.NullInt64
				var result sql.NullString
				switch r := rng.Intn(20); {
				case r == 0: // 从未完成
				case r < 3:
					finished = sql.NullInt64{Int64: at + int64(rng.Intn(90)), Valid: true}
					result = sql.NullString{String: encode(model.Result{Status: "timeout", Reason: "connect_failed", Sent: 3}), Valid: true}
				default:
					ms := 10 + rng.Float64()*200
					finished = sql.NullInt64{Int64: at + int64(rng.Intn(90)), Valid: true}
					result = sql.NullString{String: encode(model.Result{Status: "ok", Latency: &ms, Sent: 3, Received: 1 + rng.Intn(3)}), Valid: true}
				}
				if _, err = tx.Exec("INSERT INTO jobs(id,task_id,node_id,task,issued,finished,result,probe_key) VALUES(?,?,?,?,?,?,?,?)",
					fmt.Sprintf("job%06d", seq), taskID, node, encode(leaf), at, finished, result, key); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return nodes, taskID
}

func TestRecentLatencySamplesMatchesTheWindowScan(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	nodes, _ := seedReadPaths(t, s, now)
	for _, window := range []time.Duration{10 * time.Minute, 40 * time.Minute, 2 * time.Hour} {
		for _, per := range []int{1, 20} {
			since := now.Add(-window)
			got, err := s.RecentLatencySamples(ctx, nodes, per, since)
			if err != nil {
				t.Fatal(err)
			}
			args := []any{since.Unix()}
			for _, id := range nodes {
				args = append(args, id)
			}
			args = append(args, per)
			rows, err := s.db.Query(fmt.Sprintf(oldRecentLatencySQL, "?"+strings.Repeat(",?", len(nodes)-1)), args...)
			if err != nil {
				t.Fatal(err)
			}
			want := []model.LatencySample{}
			for rows.Next() {
				var x model.LatencySample
				if err = rows.Scan(&x.NodeID, &x.Carrier, &x.Region, &x.Family, &x.At, &x.Latency, &x.Sent, &x.Received); err != nil {
					t.Fatal(err)
				}
				want = append(want, x)
			}
			rows.Close()
			if len(want) == 0 {
				t.Fatalf("window %v: reference is empty, the seed does not exercise anything", window)
			}
			// 旧 SQL 只按 (at, node_id, carrier) 排，三者都相同的行顺序本来就未定义：两边按全字段
			// 排一次再比。
			normalize(got)
			normalize(want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("window %v per %d: %d samples, want %d\ngot  %+v\nwant %+v", window, per, len(got), len(want), got[:min(len(got), 5)], want[:min(len(want), 5)])
			}
		}
	}
}

func normalize(list []model.LatencySample) {
	key := func(x model.LatencySample) string {
		latency := -1.0
		if x.Latency != nil {
			latency = *x.Latency
		}
		return fmt.Sprintf("%012d|%s|%s|%s|%s|%f|%d|%d", x.At, x.NodeID, x.Carrier, x.Family, x.Region, latency, x.Sent, x.Received)
	}
	sort.Slice(list, func(i, j int) bool { return key(list[i]) < key(list[j]) })
}

func TestTaskRunsMatchesTheJobRanking(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	_, taskID := seedReadPaths(t, s, now)
	got, err := s.TaskRuns(ctx, taskID, now)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.db.Query(oldTaskRunsSQL, taskID)
	if err != nil {
		t.Fatal(err)
	}
	type run struct {
		node, name     string
		issued, finish sql.NullInt64
		task, result   sql.NullString
	}
	var want []run
	for rows.Next() {
		var r run
		if err = rows.Scan(&r.node, &r.name, &r.issued, &r.finish, &r.task, &r.result); err != nil {
			t.Fatal(err)
		}
		want = append(want, r)
	}
	rows.Close()
	// 两台绑定节点 × 6 个探测（3 个目标 × 延迟/回程）；第三台没绑定，不出现。
	if len(got) != len(want) || len(got) != 12 {
		t.Fatalf("runs = %d, reference %d", len(got), len(want))
	}
	seen := map[string]bool{}
	for _, r := range want {
		seen[fmt.Sprintf("%s@%d/%d", r.node, r.issued.Int64, r.finish.Int64)] = true
	}
	for _, r := range got {
		if !seen[fmt.Sprintf("%s@%d/%d", r.NodeID, r.Issued, r.Finished)] {
			t.Fatalf("run not in reference: %+v", r)
		}
	}
}

// oldNetworkHistory 是改写前的实现（逐行 json_extract、按六个表达式分组），留作基准。
func oldNetworkHistory(ctx context.Context, s *Store, id string, hours int, now time.Time) ([]model.NetworkSeries, error) {
	hours = min(max(hours, 1), 168)
	step := max(60, hours*3600/240)
	since := now.Add(-time.Duration(hours) * time.Hour).Unix()
	// 索引是 (node_id, issued)，只按 finished 过滤会把这台节点 7 天的任务行全读一遍。
	// TCP 结果只在下发后 90 秒内收（见 Finish），所以 issued 的下界放宽两分钟就不会漏行。
	rows, err := s.query(ctx, `SELECT task_id,COALESCE(json_extract(task,'$.family'),''),COALESCE(json_extract(task,'$.host'),''),COALESCE(json_extract(task,'$.port'),0),COALESCE(json_extract(task,'$.region'),''),COALESCE(json_extract(task,'$.carrier'),''), max(finished), avg(CASE WHEN json_extract(result,'$.status')='ok' THEN json_extract(result,'$.latency') END), COALESCE(sum(json_extract(result,'$.sent')),0),COALESCE(sum(json_extract(result,'$.received')),0),task FROM (SELECT task_id,node_id,task,finished,CASE WHEN json_valid(result) THEN result END AS result FROM jobs WHERE node_id=? AND issued>=? AND finished>=?) WHERE CASE WHEN json_valid(task) THEN json_extract(task,'$.kind') END='tcp' GROUP BY task_id,json_extract(task,'$.family'),json_extract(task,'$.host'),json_extract(task,'$.port'),json_extract(task,'$.region'),json_extract(task,'$.carrier'),finished/? ORDER BY max(finished)`, id, since-tcpFinishSlack, since, step)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	series := []model.NetworkSeries{}
	indexes := map[string]int{}
	skipped := 0
	for rows.Next() {
		var taskID, family, host, region, carrier, raw string
		var port, sent, received int
		var at int64
		var latency *float64
		if err := rows.Scan(&taskID, &family, &host, &port, &region, &carrier, &at, &latency, &sent, &received, &raw); err != nil {
			return nil, err
		}
		var task model.Probe
		if err := json.Unmarshal([]byte(raw), &task); err != nil {
			skipped++
			continue
		}
		if family == "" {
			// 旧行没写 family。目标是 IP 字面量时地址族由地址本身决定，不是猜测：
			// 补上它，旧数据才会和新数据并进同一条曲线，而不是在面板上消失。
			if ip := net.ParseIP(host); ip != nil {
				if ip.To4() != nil {
					family = "ipv4"
				} else {
					family = "ipv6"
				}
			}
		}
		task.Family, task.Host, task.Port, task.Region, task.Carrier = family, host, port, region, carrier
		key := task.ProbeKey()
		index, ok := indexes[key]
		if !ok {
			index = len(series)
			indexes[key] = index
			series = append(series, model.NetworkSeries{Name: task.Name, Family: family, Host: host, Port: port, Region: region, Carrier: carrier, Samples: []model.NetworkSample{}})
		}
		var failure *float64
		if sent > 0 {
			v := 100 * float64(sent-received) / float64(sent)
			failure = &v
		}
		series[index].Samples = append(series[index].Samples, model.NetworkSample{At: at, Latency: latency, Failure: failure})
	}
	if skipped > 0 {
		slog.Warn("skipped jobs with unreadable task payload", "node", id, "jobs", skipped)
	}
	return series, rows.Err()
}

func TestNetworkHistoryMatchesThePerRowGrouping(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	nodes, _ := seedReadPaths(t, s, now)
	for _, node := range nodes {
		for _, hours := range []int{1, 2, 24} {
			got, err := s.NetworkHistory(ctx, node, hours, now)
			if err != nil {
				t.Fatal(err)
			}
			want, err := oldNetworkHistory(ctx, s, node, hours, now)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("node %s %dh: %d series, want %d\ngot  %+v\nwant %+v", node, hours, len(got), len(want), got, want)
			}
		}
	}
}
