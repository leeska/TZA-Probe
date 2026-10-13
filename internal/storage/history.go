package storage

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

func (s *Store) History(ctx context.Context, id string, hours int, now time.Time) ([]model.Sample, error) {
	// Aggregate into <= 240 buckets. Each bucket returns its last observed sample.
	hours = min(max(hours, 1), 720)
	step := max(60, hours*3600/240)
	rows, err := s.query(ctx, `SELECT at,payload FROM samples WHERE node_id=? AND at IN (SELECT max(at) FROM samples WHERE node_id=? AND at>=? GROUP BY at/?) ORDER BY at`, id, id, now.Add(-time.Duration(hours)*time.Hour).Unix(), step)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Sample{}
	skipped := 0
	for rows.Next() {
		var x model.Sample
		var data string
		if err = rows.Scan(&x.At, &data); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(data), &x.Metrics); err != nil {
			// 坏掉的样本行（旧版本格式或半写入）只跳过自己：一台机器的历史不该
			// 因为一行读不出来就整体打不开。
			skipped++
			continue
		}
		out = append(out, x)
	}
	if skipped > 0 {
		slog.Warn("skipped unreadable samples", "node", id, "samples", skipped)
	}
	return out, rows.Err()
}
