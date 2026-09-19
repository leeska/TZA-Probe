package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/leeska/TZA-Probe/internal/model"
	"time"
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
	start, end := model.Cycle(now, n.BillingDay)
	if cycle != start.Unix() {
		return model.ErrConflict
	}
	var incoming, outgoing int64
	if err = tx.QueryRowContext(ctx, "SELECT coalesce(sum(incoming),0),coalesce(sum(outgoing),0) FROM traffic WHERE node_id=? AND day>=? AND day<?", id, start.Unix(), end.Unix()).Scan(&incoming, &outgoing); err != nil {
		return err
	}
	n.AdjustmentCycle = start.Unix()
	n.TrafficAdjustment = used - model.Traffic(incoming, outgoing, n.TrafficMode)
	if _, err = tx.ExecContext(ctx, "UPDATE nodes SET config=? WHERE id=?", encode(n), id); err != nil {
		return err
	}
	return tx.Commit()
}
