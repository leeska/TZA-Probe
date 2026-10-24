package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

// Heartbeat only updates contact and diagnostics. Metrics, samples and the
// traffic baseline (nodes.last_seen) are deliberately left untouched.
func (s *Store) Heartbeat(ctx context.Context, id string, status model.AgentStatus, now time.Time) error {
	if err := status.Validate(); err != nil {
		return model.Invalid(err.Error())
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT 1 FROM nodes WHERE id=?", id).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if accept, err := acceptAgentState(ctx, tx, id, status.AgentState, now); err != nil || !accept {
		return err
	}
	var seen int64
	err = tx.QueryRowContext(ctx, "SELECT seen_at FROM node_health WHERE node_id=?", id).Scan(&seen)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if seen > 0 && now.Unix()-seen < 3 {
		return model.Conflict("心跳过于频繁")
	}
	status.Proof = ""
	if _, err := tx.ExecContext(ctx, `INSERT INTO node_health(node_id,seen_at,status) VALUES(?,?,?) ON CONFLICT(node_id) DO UPDATE SET seen_at=excluded.seen_at,status=excluded.status`, id, now.Unix(), encode(status)); err != nil {
		return err
	}
	return tx.Commit()
}
