package storage

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

// RecentLatencySamples returns the last perGroup finished TCP probes for every
// (node, carrier, family, region) group, oldest first. The public card renders
// one cell per probe, so it needs individual measurements rather than the
// aggregated observation it already has.
//
// The cost must not grow with the window (model.SettingLatencyWindowMinutes goes
// up to the whole seven-day retention). Reading every job inside the window and
// ranking it was 93ms for one hour, 3.3s for a day and 40s for a week on ten
// nodes with eight targets each. So instead: the materialized latest table names
// every TCP probe that ran inside the window, and for each of them the jobs_node
// index is walked backwards from its newest issue, stopping after a handful of
// rows. The rows read are about probes × perGroup, whatever the window.
func (s *Store) RecentLatencySamples(ctx context.Context, nodeIDs []string, perGroup int, since time.Time) ([]model.LatencySample, error) {
	if len(nodeIDs) == 0 {
		return []model.LatencySample{}, nil
	}
	perGroup = min(max(perGroup, 1), 100)
	// Bound the IN list; the caller passes the visible node set.
	if len(nodeIDs) > 200 {
		nodeIDs = nodeIDs[:200]
	}
	args := make([]any, 0, len(nodeIDs)+1)
	for _, id := range nodeIDs {
		args = append(args, id)
	}
	args = append(args, since.Unix())
	rows, err := s.query(ctx, "SELECT node_id,probe_key,task,issued FROM latest WHERE node_id IN (?"+strings.Repeat(",?", len(nodeIDs)-1)+") AND issued>=?", args...)
	if err != nil {
		return nil, err
	}
	type probe struct {
		node, key string
		task      model.Probe
		newest    int64
	}
	var probes []probe
	for rows.Next() {
		var p probe
		var task string
		if err = rows.Scan(&p.node, &p.key, &task, &p.newest); err != nil {
			rows.Close()
			return nil, err
		}
		if json.Unmarshal([]byte(task), &p.task) != nil || p.task.Kind != "tcp" {
			continue
		}
		probes = append(probes, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}

	type group struct{ node, carrier, family, region string }
	type sample struct {
		model.LatencySample
		id string
	}
	groups := map[group][]sample{}
	for _, p := range probes {
		// 按 issued 倒序取，但分组按 finished 排：一次结果最晚在下发后 90 秒到，而探测间隔
		// 至少 30 秒，所以多取几条就能盖住「先发的后完成」造成的顺序差。
		// 结果 JSON 坏掉的行（半写入）按「没有结果」算：直接 json_extract 会让整条语句报
		// malformed JSON，一行坏数据就让整个面板的延迟格子消失。
		recent, err := s.query(ctx, `SELECT id,finished,json_extract(r,'$.latency'),coalesce(json_extract(r,'$.sent'),0),coalesce(json_extract(r,'$.received'),0)
			FROM (SELECT id,finished,issued,CASE WHEN json_valid(result) THEN result END AS r FROM jobs WHERE node_id=? AND issued>=? AND issued<=? AND finished IS NOT NULL AND `+jobKeySQL+`=?
			ORDER BY issued DESC LIMIT ?)`, p.node, since.Unix(), p.newest, p.key, perGroup+8)
		if err != nil {
			return nil, err
		}
		g := group{p.node, p.task.Carrier, p.task.Family, p.task.Region}
		for recent.Next() {
			x := sample{LatencySample: model.LatencySample{NodeID: p.node, Carrier: g.carrier, Region: g.region, Family: g.family}}
			if err = recent.Scan(&x.id, &x.At, &x.Latency, &x.Sent, &x.Received); err != nil {
				recent.Close()
				return nil, err
			}
			groups[g] = append(groups[g], x)
		}
		err = recent.Err()
		recent.Close()
		if err != nil {
			return nil, err
		}
	}
	out := []model.LatencySample{}
	for _, list := range groups {
		sort.Slice(list, func(i, j int) bool {
			if list[i].At != list[j].At {
				return list[i].At > list[j].At
			}
			return list[i].id > list[j].id
		})
		for _, x := range list[:min(len(list), perGroup)] {
			out = append(out, x.LatencySample)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.At != b.At {
			return a.At < b.At
		}
		if a.NodeID != b.NodeID {
			return a.NodeID < b.NodeID
		}
		return a.Carrier < b.Carrier
	})
	return out, nil
}

// jobKeySQL is a job row's grouping key exactly as the latest table computes it:
// the stored probe_key, or for a row written before that column existed the same
// key rebuilt from its task JSON. A legacy row whose task JSON is broken has no
// key (NULL) instead of failing the whole statement with "malformed JSON".
var jobKeySQL = "(CASE WHEN probe_key<>'' THEN probe_key WHEN json_valid(task) THEN " + probeKeyExpr("") + " END)"
