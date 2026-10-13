package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

// NetworkHistory aggregates completed TCP probes per target and time bucket.
// Failed probes contribute to failure rate, never a fabricated zero latency.
//
// 这些行由不同版本的 Agent / Server 写下：早期版本的任务 JSON 里可能没有 family、
// port，结果 JSON 里可能没有 sent，json_extract 在这些行上返回 NULL，直接 Scan 进
// Go 的 string / int 会报 "converting NULL to string is unsupported"；json1 遇到坏掉的
// JSON 更会直接让整条语句报 "malformed JSON"。一条旧数据就能让整台节点的网络历史
// 变成 500，所以每个字段都 COALESCE、坏 JSON 用 json_valid 挡在聚合之外，任务 JSON
// 解析不了的行只跳过自己。
//
// task_id IN tasks：删掉的任务，它的探测历史由后台分批删（见 purge.go），删完之前这里
// 就不再画它。
const tcpFinishSlack = 120

func (s *Store) NetworkHistory(ctx context.Context, id string, hours int, now time.Time) ([]model.NetworkSeries, error) {
	hours = min(max(hours, 1), 168)
	step := max(60, hours*3600/240)
	since := now.Add(-time.Duration(hours) * time.Hour).Unix()
	// 索引是 (node_id, issued)，只按 finished 过滤会把这台节点 7 天的任务行全读一遍。
	// TCP 结果只在下发后 90 秒内收（见 Finish），所以 issued 的下界放宽两分钟就不会漏行。
	//
	// 分组键与种类都从 probe_key（taskID|kind|family|host|port|region|carrier）读，不再对
	// 每一行的任务 JSON 做六次 json_extract 再按六个表达式分组：一周的结果是十几万行，那是
	// 这条查询的大头（24 小时 0.5s、168 小时 3.2s）。目标字段每组只在 Go 里解析一次。
	var rows *sql.Rows
	var err error
	if from := s.tcpBucketsFrom.Load(); step%tcpBucketSeconds == 0 && (from == 0 || since-since%tcpBucketSeconds >= from) {
		// 步长是格子的整数倍：在 6 分钟格子上再聚合（见 tcp_buckets.go）。起点向下取整到
		// 格子边界，最左一格最多多算几分钟，对 24 小时以上的曲线看不出差别。
		rows, err = s.query(ctx, `SELECT probe_key,max(finished),CASE WHEN sum(lat_n)>0 THEN sum(lat_sum)/sum(lat_n) END,sum(sent),sum(received),task
			FROM tcp_buckets WHERE node_id=? AND bucket>=? AND task_id IN (SELECT id FROM tasks)
			GROUP BY probe_key,bucket/? ORDER BY max(finished)`, id, since-since%tcpBucketSeconds, step)
	} else {
		rows, err = s.query(ctx, `SELECT key,max(finished),avg(CASE WHEN json_extract(result,'$.status')='ok' THEN json_extract(result,'$.latency') END),COALESCE(sum(json_extract(result,'$.sent')),0),COALESCE(sum(json_extract(result,'$.received')),0),task
		FROM (SELECT task_id,task,finished,CASE WHEN json_valid(result) THEN result END AS result,`+jobKeySQL+` AS key
		      FROM jobs WHERE node_id=? AND issued>=? AND finished>=? AND task_id IN (SELECT id FROM tasks))
		WHERE substr(key,length(task_id)+2,4)='tcp|'
		GROUP BY key,finished/? ORDER BY max(finished)`, id, since-tcpFinishSlack, since, step)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	series := []model.NetworkSeries{}
	indexes := map[string]int{}
	tasks := map[string]*model.Probe{} // 每个分组键只解析一次任务 JSON
	skipped := 0
	for rows.Next() {
		var key, raw string
		var sent, received int
		var at int64
		var latency *float64
		if err := rows.Scan(&key, &at, &latency, &sent, &received, &raw); err != nil {
			return nil, err
		}
		task, ok := tasks[key]
		if !ok {
			var t model.Probe
			if err := json.Unmarshal([]byte(raw), &t); err != nil {
				tasks[key] = nil
			} else {
				if t.Family == "" {
					// 旧行没写 family。目标是 IP 字面量时地址族由地址本身决定，不是猜测：
					// 补上它，旧数据才会和新数据并进同一条曲线，而不是在面板上消失。
					if ip := net.ParseIP(t.Host); ip != nil {
						if ip.To4() != nil {
							t.Family = "ipv4"
						} else {
							t.Family = "ipv6"
						}
					}
				}
				tasks[key] = &t
			}
			task = tasks[key]
		}
		if task == nil {
			skipped++
			continue
		}
		merged := task.ProbeKey()
		index, ok := indexes[merged]
		if !ok {
			index = len(series)
			indexes[merged] = index
			series = append(series, model.NetworkSeries{Name: task.Name, Family: task.Family, Host: task.Host, Port: task.Port, Region: task.Region, Carrier: task.Carrier, Samples: []model.NetworkSample{}})
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
