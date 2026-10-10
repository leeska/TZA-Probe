package storage

import (
	"context"
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
const tcpFinishSlack = 120

func (s *Store) NetworkHistory(ctx context.Context, id string, hours int, now time.Time) ([]model.NetworkSeries, error) {
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
		var task model.Task
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
