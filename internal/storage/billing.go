package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

// CorrectTraffic changes the billed total without rewriting raw interface counters.
// Its offset is scoped to the current UTC billing cycle and expires automatically.
func (s *Store) CorrectTraffic(ctx context.Context, id string, used int64, cycle int64, now time.Time) error {
	if used < 0 || used > 1e16 {
		return model.ErrInvalid
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw string
	if err = tx.QueryRowContext(ctx, "SELECT config FROM nodes WHERE id=?", id).Scan(&raw); errors.Is(err, sql.ErrNoRows) {
		return model.ErrNotFound
	} else if err != nil {
		return err
	}
	var n model.Node
	if err = json.Unmarshal([]byte(raw), &n); err != nil {
		return err
	}
	start, end := model.Cycle(now, model.BillingAnchor(n.Billing.ResetAt, now))
	if cycle != start.Unix() {
		return model.Conflict("计费周期已变化，请刷新后重新校正")
	}
	var incoming, outgoing int64
	if err = tx.QueryRowContext(ctx, "SELECT coalesce(sum(incoming),0),coalesce(sum(outgoing),0) FROM traffic WHERE node_id=? AND day>=? AND day<?", id, start.Unix(), end.Unix()).Scan(&incoming, &outgoing); err != nil {
		return err
	}
	// 与 Nodes 同一口径：锚点不在 00:00 时，周期首日那一段在小时桶里（见 cycleStartSlice）。
	// 漏了它，校正后面板显示的是「校正值 + 首日那一段」，而不是管理员填的数。
	if from, to, ok := cycleStartSlice(start.Unix(), end.Unix()); ok {
		var edgeIn, edgeOut int64
		if err = tx.QueryRowContext(ctx, "SELECT coalesce(sum(incoming),0),coalesce(sum(outgoing),0) FROM traffic_hour WHERE node_id=? AND hour>=? AND hour<?", id, from, to).Scan(&edgeIn, &edgeOut); err != nil {
			return err
		}
		incoming, outgoing = incoming+edgeIn, outgoing+edgeOut
	}
	adjustment := used - model.Traffic(incoming, outgoing, n.Billing.TrafficMode)
	if _, err = tx.ExecContext(ctx, "UPDATE nodes SET traffic_adjustment=?,adjustment_cycle=? WHERE id=?", adjustment, start.Unix(), id); err != nil {
		return err
	}
	return tx.Commit()
}

// cycleStartSlice 返回周期首日里真正属于本周期的那一段流量。
//
// traffic 表按 UTC 整天存桶，Nodes 的 day>=cycle_start 只能整天算，于是计费锚点不在
// 00:00 的节点（腾讯轻量 09-23 12:36、BWH DC9 09-15 15:17 都是）在周期一开始就出问题：
// 首日那一天的桶整体早于 cycle_start，被整段丢掉——面板数字比商家自己的计费口径少了
// 一整段（实测腾讯 40.6 MB / 3.4%），而且数字其实在锚点日 00:00 就归零，与卡片上写的
// 锚点时刻对不上。这里用同一份网卡增量落的整点桶（traffic_hour）补回 [cycle_start, 当天 24:00)。
//
// 锚点不在整点时，含锚点的那一个小时横跨边界：宁可少算它（月周期约 0.03%），也不能
// 多算——按周期额度降速的线路会因此提前被判定为用满。小时桶只留 30 天，切不到就退回
// 原样（不补），任何情况下都不会多算。
func cycleStartSlice(start, end int64) (int64, int64, bool) {
	if start%86400 == 0 {
		return 0, 0, false // 锚点在整日边界：日桶已经精确覆盖，别动
	}
	from := start
	if from%3600 != 0 {
		from = from/3600*3600 + 3600
	}
	to := min(end, start/86400*86400+86400)
	return from, to, from < to
}

// span 是一台节点要汇总的桶区间 [from, to)。
type span struct {
	id       string
	from, to int64
}

type totals struct{ in, out int64 }

// rangeSums 用一条查询给每台节点汇总各自区间内的桶：区间作为 VALUES 表与桶表按
// (node_id, 桶) 主键连接，每台节点仍是一次索引范围查找，只是不再逐台往返。
// table / column 只取代码里的常量，不来自输入。
func (s *Store) rangeSums(ctx context.Context, table, column string, spans []span) (map[string]totals, error) {
	out := make(map[string]totals, len(spans))
	if len(spans) == 0 {
		return out, nil
	}
	values := make([]string, len(spans))
	args := make([]any, 0, len(spans)*3)
	for i, sp := range spans {
		values[i] = "(?,?,?)"
		args = append(args, sp.id, sp.from, sp.to)
	}
	rows, err := s.query(ctx, "WITH r(id,a,b) AS (VALUES "+strings.Join(values, ",")+") SELECT r.id,coalesce(sum(t.incoming),0),coalesce(sum(t.outgoing),0) FROM r JOIN "+table+" t ON t.node_id=r.id AND t."+column+">=r.a AND t."+column+"<r.b GROUP BY r.id", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var t totals
		if err = rows.Scan(&id, &t.in, &t.out); err != nil {
			return nil, err
		}
		out[id] = t
	}
	return out, rows.Err()
}

// attachWindow 给每台节点算「降速窗口内的平均带宽」。
//
// 数据来自 traffic_hour（Report 每次上报把网卡计数增量累加到整点桶），所以窗口任意、
// 且代价与窗口无关：一次按节点分组的查询就把所有节点都算完（首页每秒一轮 /nodes，
// 逐台查会把 1 核机的时间又叠回去）。窗口起点向下取整到整点：分子覆盖的桶区间与
// 分母 (now-起点) 完全对应；而 now 与窗口起点的分钟相位相同，开头多算的那点不足
// 一小时正好补上当前这个没走完的桶。
//
// 覆盖不足窗口（新接入、Agent 停过）时按真实覆盖秒数取平均，并把 CoveredHours 报给
// 面板，由它写「统计中」——不假装有一整个窗口的数据。
func (s *Store) attachWindow(ctx context.Context, views []model.NodeView, now time.Time) error {
	if len(views) == 0 {
		return nil
	}
	// 一次取出最长窗口内的小时桶（按 hour 索引范围扫，行数 ≈ 节点数 × 窗口小时数），
	// 再在内存里按每台节点自己的窗口汇总。以前每种窗口各跑一遍不带节点过滤的 GROUP BY，
	// 而 GROUP BY node_id 让规划器选了主键全表扫（45 天的小时桶），每秒一轮。
	startOf := func(hours int) int64 { return (now.Unix() - int64(hours)*3600) / 3600 * 3600 }
	longest := 0
	for i := range views {
		views[i].WindowHours = views[i].Line.WindowHours()
		longest = max(longest, views[i].WindowHours)
	}
	type bucket struct{ hour, in, out int64 }
	byNode := map[string][]bucket{}
	rows, err := s.query(ctx, "SELECT node_id,hour,incoming,outgoing FROM traffic_hour WHERE hour>=?", startOf(longest))
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		var b bucket
		if err = rows.Scan(&id, &b.hour, &b.in, &b.out); err != nil {
			rows.Close()
			return err
		}
		byNode[id] = append(byNode[id], b)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for i := range views {
		v := &views[i]
		hours := v.WindowHours
		start := startOf(hours)
		span := now.Unix() - start
		var in, out int64
		first, seen := int64(0), false
		for _, b := range byNode[v.ID] {
			if b.hour < start {
				continue
			}
			in += b.in
			out += b.out
			if !seen || b.hour < first {
				first, seen = b.hour, true
			}
		}
		if !seen || span <= 0 {
			continue
		}
		covered := min(span, now.Unix()-first)
		if covered <= 0 {
			continue
		}
		v.AvgBits = float64(model.Traffic(in, out, v.Billing.TrafficMode)) * 8 / float64(covered)
		v.CoveredHours = int((covered + 3599) / 3600)
		if covered >= span {
			v.CoveredHours = hours
		}
	}
	return nil
}
