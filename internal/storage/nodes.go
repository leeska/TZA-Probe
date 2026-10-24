package storage

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

func (s *Store) Nodes(ctx context.Context, now time.Time) ([]model.NodeView, error) {
	plan, err := s.ProbeTargets(ctx)
	if err != nil {
		return nil, err
	}
	rules, err := s.AlertRules(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.query(ctx, "SELECT n.config,n.last_seen,n.metrics,n.created_at,n.traffic_adjustment,n.adjustment_cycle,coalesce(h.seen_at,0),h.status FROM nodes n LEFT JOIN node_health h ON h.node_id=n.id")
	if err != nil {
		return nil, err
	}
	views := []model.NodeView{}
	// 用量校正（服务端自记的状态，不在配置里）：只在它记下的那个周期生效。
	type correction struct{ amount, cycle int64 }
	corrections := map[string]correction{}
	for rows.Next() {
		var v model.NodeView
		var config string
		var metrics sql.NullString
		var health sql.NullString
		var heartbeatAt int64
		var c correction
		if err = rows.Scan(&config, &v.LastSeen, &metrics, &v.CreatedAt, &c.amount, &c.cycle, &heartbeatAt, &health); err != nil {
			rows.Close()
			return nil, err
		}
		if err = json.Unmarshal([]byte(config), &v.Node); err != nil {
			rows.Close()
			return nil, err
		}
		if metrics.Valid {
			if err = json.Unmarshal([]byte(metrics.String), &v.Metrics); err != nil {
				rows.Close()
				return nil, err
			}
			if v.Metrics != nil {
				v.IPFamilies = v.Metrics.IPFamilies
			}
		}
		v.MetricsAt = v.LastSeen
		if health.Valid && heartbeatAt >= v.MetricsAt {
			if err = json.Unmarshal([]byte(health.String), &v.AgentStatus); err != nil {
				rows.Close()
				return nil, err
			}
		}
		v.LastSeen = max(v.LastSeen, heartbeatAt)
		v.Online = v.LastSeen > 0 && now.Unix()-v.LastSeen < int64(model.OfflineAfter.Seconds())
		v.MetricsStale = v.Online && (v.MetricsAt == 0 || now.Unix()-v.MetricsAt >= int64(model.OfflineAfter.Seconds()))
		if v.Online && v.AgentStatus != nil {
			switch v.AgentStatus.State {
			case "collect_failed", "collect_timeout", "report_failed":
				v.MetricsStale = true
			}
		}
		if v.MetricsStale {
			v.Metrics = nil
		}
		v.ProbeTargets = plan[v.ID]
		v.Alerts = model.EffectiveAlerts(rules, v.Node)
		corrections[v.ID] = c
		start, end := model.Cycle(now, model.BillingAnchor(v.Billing.ResetAt, now))
		v.CycleStart = start.Unix()
		v.CycleEnd = end.Unix()
		v.RemainingValue = model.RemainingValue(v.Billing, now)
		views = append(views, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	// 每台节点的周期用量：日桶一条、首日切片一条，两条查询把整页都算完（以前逐台各查两次，
	// 首页每秒一轮，节点越多查询越多）。
	days := make([]span, 0, len(views))
	edges := []span{}
	for _, v := range views {
		days = append(days, span{v.ID, v.CycleStart, v.CycleEnd})
		// 日桶只能整天算：锚点不在 00:00 时周期首日那一天的桶整体早于 cycle_start，被日桶
		// 查询整段丢掉，用小时桶把锚点之后的那一段补回来（见 cycleStartSlice）。
		if from, to, ok := cycleStartSlice(v.CycleStart, v.CycleEnd); ok {
			edges = append(edges, span{v.ID, from, to})
		}
	}
	dayTotals, err := s.rangeSums(ctx, "traffic", "day", days)
	if err != nil {
		return nil, err
	}
	edgeTotals, err := s.rangeSums(ctx, "traffic_hour", "hour", edges)
	if err != nil {
		return nil, err
	}
	for i := range views {
		v := &views[i]
		d, e := dayTotals[v.ID], edgeTotals[v.ID]
		v.TrafficUsed = model.Traffic(d.in+e.in, d.out+e.out, v.Billing.TrafficMode)
		if c := corrections[v.ID]; c.cycle == v.CycleStart {
			v.TrafficUsed = max(0, v.TrafficUsed+c.amount)
		}
	}
	if err := s.attachWindow(ctx, views, now); err != nil {
		return nil, err
	}
	// 解锁结论与延迟/降速一样，是「这台机器现在是什么样」的一部分：一次按节点分组的
	// 查询把整页都算完（首页每秒一轮，逐台查会把 1 核机的时间叠回去）。
	unlocks, err := s.unlockSummaries(ctx)
	if err != nil {
		return nil, err
	}
	for i := range views {
		views[i].Unlock = unlocks[views[i].ID]
	}
	// 降速状态在这里统一判定（不放在 attachWindow 里）：按周期额度降速的线路即使一个
	// 小时桶都还没有，只要本周期用量已经顶到额度就是已降速。
	for i := range views {
		views[i].Throttled = views[i].ThrottleActive()
	}
	return views, nil
}

// SaveNode creates or updates a node and returns the stored id plus, on
// creation, the one-time Agent token.
func (s *Store) SaveNode(ctx context.Context, n model.Node, create bool) (string, string, error) {
	if err := n.Validate(); err != nil {
		return "", "", model.Invalid(err.Error())
	}
	n = n.Normalized()
	s.writes.Lock()
	defer s.writes.Unlock()
	if create {
		n.ID = model.Secret()[:16]
		token := model.Secret()
		_, err := s.db.ExecContext(ctx, "INSERT INTO nodes(id,config,token_hash,created_at) VALUES(?,?,?,?)", n.ID, encode(n), model.Hash(token), time.Now().Unix())
		return n.ID, token, err
	}
	var old string
	if err := s.queryRow(ctx, "SELECT config FROM nodes WHERE id=?", n.ID).Scan(&old); errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	} else if err != nil {
		return "", "", err
	}
	var prior model.Node
	if err := json.Unmarshal([]byte(old), &prior); err != nil {
		return "", "", err
	}
	// 用量校正按旧的周期与计费方向算出来的：两者一变，它就不再成立。
	query := "UPDATE nodes SET config=? WHERE id=?"
	if n.Billing.ResetAt != prior.Billing.ResetAt || n.Billing.TrafficMode != prior.Billing.TrafficMode {
		query = "UPDATE nodes SET config=?,traffic_adjustment=0,adjustment_cycle=0 WHERE id=?"
	}
	_, err := s.db.ExecContext(ctx, query, encode(n), n.ID)
	return n.ID, "", err
}

// SaveNodeOrder rewrites only the order field of the listed nodes, in the given
// sequence (the first id becomes 0). The panel's drag sends the whole visible
// list, so the caller never computes the numbers; because the stored config is
// read back and only Order is replaced, a drag cannot blank a name, a billing
// anchor or a traffic correction the way a partially filled SaveNode would.
func (s *Store) SaveNodeOrder(ctx context.Context, ids []string) error {
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, id := range ids {
		var config string
		if err := tx.QueryRowContext(ctx, "SELECT config FROM nodes WHERE id=?", id).Scan(&config); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		var n model.Node
		if err := json.Unmarshal([]byte(config), &n); err != nil {
			return err
		}
		n.Order = i
		if _, err := tx.ExecContext(ctx, "UPDATE nodes SET config=? WHERE id=?", encode(n), id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteNode removes a node at once — it disappears from the panel and its
// key stops working — while its probe results and minute samples (a week and a
// month of rows) are purged in the background, see purge.go.
func (s *Store) DeleteNode(ctx context.Context, id string) error {
	s.writes.Lock()
	defer s.writes.Unlock()
	err := s.withoutCascade(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, "DELETE FROM nodes WHERE id=?", id)
		if err != nil {
			return err
		}
		if n, _ := r.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		// What the cascade used to do for the small tables; jobs and samples are queued.
		for _, table := range []string{"bindings", "alert_state", "latest", "unlocks", "retries", "traffic", "traffic_hour", "node_health", "node_agent_sessions"} {
			if _, err = tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE node_id=?", id); err != nil {
				return err
			}
		}
		if err = forgetNodeInRules(ctx, tx, id); err != nil {
			return err
		}
		return queuePurge(ctx, tx, purgeNodeRows, id, noCutoff)
	})
	if err == nil {
		s.wakePurger()
	}
	return err
}

func (s *Store) Rotate(ctx context.Context, id string) (string, error) {
	s.writes.Lock()
	defer s.writes.Unlock()
	token := model.Secret()
	r, err := s.db.ExecContext(ctx, "UPDATE nodes SET token_hash=? WHERE id=?", model.Hash(token), id)
	if err != nil {
		return "", err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return "", ErrNotFound
	}
	return token, nil
}

// IsPublic is a keyed lookup; detail requests must not reload all nodes and billing ledgers.
func (s *Store) IsPublic(ctx context.Context, id string) (bool, error) {
	var public bool
	err := s.queryRow(ctx, "SELECT json_extract(config,'$.public') FROM nodes WHERE id=?", id).Scan(&public)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	return public, err
}

// SaveNodes updates several existing nodes in one transaction: the panel's
// batch edit (分组、地区、公开…) either lands on every chosen node or on none.
// Like SaveNode, a changed cycle anchor or traffic mode drops the correction.
func (s *Store) SaveNodes(ctx context.Context, nodes []model.Node) error {
	for i := range nodes {
		if err := nodes[i].Validate(); err != nil {
			return model.Invalid(nodes[i].Name + "：" + err.Error())
		}
		nodes[i] = nodes[i].Normalized()
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, n := range nodes {
		var old string
		if err := tx.QueryRowContext(ctx, "SELECT config FROM nodes WHERE id=?", n.ID).Scan(&old); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		var prior model.Node
		if err := json.Unmarshal([]byte(old), &prior); err != nil {
			return err
		}
		query := "UPDATE nodes SET config=? WHERE id=?"
		if n.Billing.ResetAt != prior.Billing.ResetAt || n.Billing.TrafficMode != prior.Billing.TrafficMode {
			query = "UPDATE nodes SET config=?,traffic_adjustment=0,adjustment_cycle=0 WHERE id=?"
		}
		if _, err := tx.ExecContext(ctx, query, encode(n), n.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RenameGroup moves every node of group from into group to and rewrites the
// alert rules scoped to from, so a renamed group keeps its rules. to may name
// an existing group (the two merge) or be "" (未分组). A merge that would leave
// two rules of the same kind competing on one group is refused and nothing
// changes. It returns how many nodes moved.
func (s *Store) RenameGroup(ctx context.Context, from, to string) (int, error) {
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT config FROM nodes")
	if err != nil {
		return 0, err
	}
	var moved []model.Node
	names := map[string]string{}
	for rows.Next() {
		var config string
		if err = rows.Scan(&config); err != nil {
			rows.Close()
			return 0, err
		}
		var n model.Node
		if err = json.Unmarshal([]byte(config), &n); err != nil {
			rows.Close()
			return 0, err
		}
		names[n.ID] = n.Name
		if n.Group == from {
			n.Group = to
			moved = append(moved, n)
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return 0, err
	}
	for _, n := range moved {
		if _, err = tx.ExecContext(ctx, "UPDATE nodes SET config=? WHERE id=?", encode(n), n.ID); err != nil {
			return 0, err
		}
	}
	rules, err := alertRules(ctx, tx)
	if err != nil {
		return 0, err
	}
	var changed []int
	for i, r := range rules {
		if r.Scope != model.ScopeGroup || !slices.Contains(r.Groups, from) {
			continue
		}
		groups := []string{}
		for _, g := range r.Groups {
			if g == from {
				g = to
			}
			if !slices.Contains(groups, g) {
				groups = append(groups, g)
			}
		}
		rules[i].Groups = groups
		changed = append(changed, i)
	}
	for _, i := range changed {
		r := rules[i]
		if err = r.Validate(); err != nil {
			return 0, model.Invalid(err.Error())
		}
		if err = r.Overlaps(rules, func(id string) string { return cmp.Or(names[id], id) }); err != nil {
			return 0, model.Invalid("合并后提醒规则冲突：" + err.Error())
		}
		if _, err = tx.ExecContext(ctx, "UPDATE alert_rules SET config=? WHERE id=?", encode(r), r.ID); err != nil {
			return 0, err
		}
	}
	return len(moved), tx.Commit()
}
