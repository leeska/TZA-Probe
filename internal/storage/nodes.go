package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

func (s *Store) Nodes(ctx context.Context, now time.Time) ([]model.NodeView, error) {
	plan, err := s.ProbeTargets(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.query(ctx, "SELECT config,last_seen,metrics,created_at,traffic_adjustment,adjustment_cycle FROM nodes")
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
		var c correction
		if err = rows.Scan(&config, &v.LastSeen, &metrics, &v.CreatedAt, &c.amount, &c.cycle); err != nil {
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
		v.Online = v.LastSeen > 0 && now.Unix()-v.LastSeen < int64(model.OfflineAfter.Seconds())
		v.ProbeTargets = plan[v.ID]
		corrections[v.ID] = c
		start, end := model.Cycle(now, model.BillingAnchor(v.Billing.ResetAt, now))
		v.CycleStart = start.Unix()
		v.CycleEnd = end.Unix()
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

// SetNodeAlerts gives every listed node the same alert rules in one
// transaction, leaving the rest of each node's config as stored. A node whose
// line cannot take the rules (say, a throttle alert without a throttle
// threshold) fails the whole batch and is named in the error.
func (s *Store) SetNodeAlerts(ctx context.Context, ids []string, alerts model.Alerts) error {
	if len(ids) == 0 {
		return model.Invalid("请至少选择一个节点")
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		var raw string
		if err = tx.QueryRowContext(ctx, "SELECT config FROM nodes WHERE id=?", id).Scan(&raw); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		var n model.Node
		if err = json.Unmarshal([]byte(raw), &n); err != nil {
			return err
		}
		n.Alerts = alerts
		if err = n.Validate(); err != nil {
			return model.Invalid(n.Name + "：" + err.Error())
		}
		if _, err = tx.ExecContext(ctx, "UPDATE nodes SET config=? WHERE id=?", encode(n), id); err != nil {
			return err
		}
	}
	return tx.Commit()
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
		for _, table := range []string{"bindings", "alert_state", "latest", "unlocks", "retries", "traffic", "traffic_hour"} {
			if _, err = tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE node_id=?", id); err != nil {
				return err
			}
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
