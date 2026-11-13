package storage

import (
	"context"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

const auditTable = `CREATE TABLE audit_log(id INTEGER PRIMARY KEY, at INTEGER NOT NULL, actor TEXT NOT NULL, source TEXT NOT NULL, action TEXT NOT NULL, node_id TEXT NOT NULL DEFAULT '', status INTEGER NOT NULL DEFAULT 0, detail TEXT NOT NULL DEFAULT '');
CREATE INDEX audit_log_at ON audit_log(at);`

// Audit appends one entry (At defaults to now) and drops entries past the
// retention period.
func (s *Store) Audit(ctx context.Context, e model.AuditEntry) error {
	if e.At == 0 {
		e.At = time.Now().Unix()
	}
	if len(e.Detail) > 16<<10 {
		e.Detail = e.Detail[:16<<10] + "…"
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	if _, err := s.db.ExecContext(ctx, "INSERT INTO audit_log(at,actor,source,action,node_id,status,detail) VALUES(?,?,?,?,?,?,?)", e.At, e.Actor, e.Source, e.Action, e.NodeID, e.Status, e.Detail); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, "DELETE FROM audit_log WHERE at<?", time.Now().AddDate(0, 0, -model.AuditRetentionDays).Unix())
	return err
}

// AuditLog lists entries newest first: at most limit of them, older than the
// entry before (0 starts at the newest), optionally only those of one node.
func (s *Store) AuditLog(ctx context.Context, before int64, limit int, node string) ([]model.AuditEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if before <= 0 {
		before = 1 << 62
	}
	query := "SELECT id,at,actor,source,action,node_id,status,detail FROM audit_log WHERE id<?"
	args := []any{before}
	if node != "" {
		query += " AND node_id=?"
		args = append(args, node)
	}
	rows, err := s.read.QueryContext(ctx, query+" ORDER BY id DESC LIMIT ?", append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.AuditEntry{}
	for rows.Next() {
		var e model.AuditEntry
		if err := rows.Scan(&e.ID, &e.At, &e.Actor, &e.Source, &e.Action, &e.NodeID, &e.Status, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
