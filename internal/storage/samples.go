package storage

import (
	"context"
	"strings"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

// RecentLatencySamples returns the last perGroup finished TCP probes for every
// (node, carrier, family, region) group, newest first. The public card renders
// one cell per probe, so it needs individual measurements rather than the
// aggregated observation it already has.
//
// Results are capped per group so a busy node cannot turn the panel payload
// into a full history dump, and the scan is bounded to ProbeWindow: 20 samples
// of a 30-60s probe is a quarter of an hour, so a day of jobs is already more
// than the card can draw while the seven-day table made every poll walk the
// whole history (3.5s for 17 nodes on the production host).
func (s *Store) RecentLatencySamples(ctx context.Context, nodeIDs []string, perGroup int) ([]model.LatencySample, error) {
	if len(nodeIDs) == 0 {
		return []model.LatencySample{}, nil
	}
	perGroup = min(max(perGroup, 1), 100)
	// Bound the IN list; the caller passes the visible node set.
	if len(nodeIDs) > 200 {
		nodeIDs = nodeIDs[:200]
	}
	args := make([]any, 0, len(nodeIDs)+2)
	// 占位符顺序：issued 的界在 IN 列表之前，参数必须按 SQL 里的先后绑定。
	args = append(args, time.Now().Add(-ProbeWindow).Unix())
	for _, id := range nodeIDs {
		args = append(args, id)
	}
	args = append(args, perGroup)
	rows, err := s.query(ctx, `
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
			  AND node_id IN (?`+strings.Repeat(",?", len(nodeIDs)-1)+`)
		) WHERE n <= ? ORDER BY at, node_id, carrier`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.LatencySample{}
	for rows.Next() {
		var s model.LatencySample
		if err := rows.Scan(&s.NodeID, &s.Carrier, &s.Region, &s.Family, &s.At, &s.Latency, &s.Sent, &s.Received); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
