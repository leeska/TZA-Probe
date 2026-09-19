package storage

import (
	"context"
	"encoding/json"
	"github.com/leeska/TZA-Probe/internal/model"
	"time"
)

// NetworkHistory aggregates completed TCP probes per target and time bucket.
// Failed probes contribute to failure rate, never a fabricated zero latency.
func (s *Store) NetworkHistory(ctx context.Context, id string, hours int, now time.Time) ([]model.NetworkSeries, error) {
	hours = min(max(hours, 1), 168)
	step := max(60, hours*3600/240)
	rows, err := s.db.QueryContext(ctx, `SELECT task_id,json_extract(task,'$.family'),json_extract(task,'$.host'),json_extract(task,'$.port'),json_extract(task,'$.region'),json_extract(task,'$.carrier'), max(finished), avg(CASE WHEN json_extract(result,'$.status')='ok' THEN json_extract(result,'$.latency') END), sum(json_extract(result,'$.sent')),sum(json_extract(result,'$.received')),task FROM jobs WHERE node_id=? AND finished>=? AND json_extract(task,'$.kind')='tcp' GROUP BY task_id,json_extract(task,'$.family'),json_extract(task,'$.host'),json_extract(task,'$.port'),json_extract(task,'$.region'),json_extract(task,'$.carrier'),finished/? ORDER BY max(finished)`, id, now.Add(-time.Duration(hours)*time.Hour).Unix(), step)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	series := []model.NetworkSeries{}
	indexes := map[string]int{}
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
			return nil, err
		}
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
	return series, rows.Err()
}
