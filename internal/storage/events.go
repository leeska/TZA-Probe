package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

// Transition persists debounce and edge state atomically with its durable
// notification event. hold is how long the condition must last before it fires;
// repeat re-sends a still-active alert every that many seconds (0: once).
//
// The engine calls it for every node and kind every tick, and almost every call
// changes nothing; those end as a read and write nothing, so a quiet fleet adds
// no commits (and no fsyncs) next to the Agents' reports. A missing row is the
// quiet state (since 0, inactive), so a kind that never fired needs none.
//
// describe builds the event text only when an event is written (active: the
// alert fires or repeats, repeat: it is a repeat reminder), so the quiet path
// formats nothing.
func (s *Store) Transition(ctx context.Context, n model.Node, kind string, describe func(active, repeat bool) string, condition bool, hold, repeat int, now time.Time) error {
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var since int64
	var active bool
	err = tx.QueryRowContext(ctx, "SELECT since,active FROM alert_state WHERE node_id=? AND kind=?", n.ID, kind).Scan(&since, &active)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	previous, previousSince := active, since
	if condition {
		if since == 0 {
			since = now.Unix()
		}
		if now.Unix()-since >= int64(hold) {
			active = true
		}
	} else {
		since = 0
		active = false
	}
	again := false
	if active && previous && repeat > 0 {
		var last int64
		if err = tx.QueryRowContext(ctx, "SELECT coalesce(max(at),0) FROM events WHERE node_id=? AND kind=?", n.ID, kind).Scan(&last); err != nil {
			return err
		}
		again = now.Unix()-last >= int64(repeat)
	}
	if since == previousSince && active == previous && !again {
		return nil
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO alert_state VALUES(?,?,?,?) ON CONFLICT(node_id,kind) DO UPDATE SET since=excluded.since,active=excluded.active", n.ID, kind, since, active); err != nil {
		return err
	}
	if active != previous || again {
		message := ""
		if describe != nil {
			message = describe(active, again)
		}
		if !active && message == "" {
			message = "条件已解除或提醒已关闭"
		}
		if !active && previousSince > 0 {
			message += "\n本次条件持续：" + model.HumanDuration(time.Duration(max(now.Unix()-previousSince, 0))*time.Second)
		}
		if elapsed := now.Unix() - since; active && elapsed > 0 {
			message += "\n本次条件持续：" + model.HumanDuration(time.Duration(elapsed)*time.Second)
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO events(node_id,node_name,kind,message,active,at) VALUES(?,?,?,?,?,?)", n.ID, n.Name, kind, message, active, now.Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Events pages the event log newest first for the panel and MCP: q.Before is
// the id cursor (0 = from the newest), NodeID / Kind narrow it.
func (s *Store) Events(ctx context.Context, q model.EventQuery) ([]model.Event, error) {
	q = q.Normalize()
	where, args := []string{}, []any{}
	if q.Before > 0 {
		where, args = append(where, "id<?"), append(args, q.Before)
	}
	if q.NodeID != "" {
		where, args = append(where, "node_id=?"), append(args, q.NodeID)
	}
	if q.Kind != "" {
		where, args = append(where, "kind=?"), append(args, q.Kind)
	}
	if q.Active {
		// 正在告警 = alert_state 里仍是 active 的那几条；每条取最近一次事件（触发或重复提醒）。
		// 从 alert_state 出发逐条走 events_rule_time 索引：事件保留 90 天、可达几十万条，
		// 先 JOIN 再 GROUP BY 会把整个索引扫一遍，节点页与告警页每次打开都要付这笔。
		where = append(where, "id IN (SELECT (SELECT max(e.id) FROM events e WHERE e.node_id=a.node_id AND e.kind=a.kind) FROM alert_state a WHERE a.active=1)")
	}
	clause := ""
	if len(where) > 0 {
		clause = "WHERE " + strings.Join(where, " AND ")
	}
	return s.events(ctx, clause+" ORDER BY id DESC LIMIT ?", append(args, q.Limit)...)
}

// PendingEvents is the delivery queue: undelivered events due for another
// attempt and still inside model.EventDeliveryWindow, oldest first.
func (s *Store) PendingEvents(ctx context.Context, now time.Time) ([]model.Event, error) {
	return s.events(ctx, "WHERE delivered=0 AND next_attempt<=? AND attempts<10 AND at>=? ORDER BY id ASC LIMIT 100", now.Unix(), now.Add(-model.EventDeliveryWindow).Unix())
}

func (s *Store) events(ctx context.Context, tail string, args ...any) ([]model.Event, error) {
	rows, err := s.query(ctx, "SELECT id,node_id,node_name,kind,message,active,at,delivered,attempts,last_error FROM events "+tail, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Event{}
	for rows.Next() {
		var e model.Event
		if err = rows.Scan(&e.ID, &e.NodeID, &e.NodeName, &e.Kind, &e.Message, &e.Active, &e.At, &e.Delivered, &e.Attempts, &e.LastError); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) Delivery(ctx context.Context, e model.Event, ok bool, now time.Time) error {
	_, err := s.db.ExecContext(ctx, "UPDATE events SET delivered=?,last_error=?,attempts=attempts+1,next_attempt=? WHERE id=?", ok, e.LastError, now.Add(time.Duration(min(3600, 30*(1<<min(e.Attempts, 7))))*time.Second).Unix(), e.ID)
	return err
}
