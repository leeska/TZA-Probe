package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

// ControlTask changes management state without deleting historical probe results.
func (s *Store) ControlTask(ctx context.Context, id string, action model.TaskAction, now time.Time) error {
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw string
	if err = tx.QueryRowContext(ctx, "SELECT config FROM tasks WHERE id=?", id).Scan(&raw); errors.Is(err, sql.ErrNoRows) {
		return model.ErrNotFound
	} else if err != nil {
		return err
	}
	var task model.Task
	if err = json.Unmarshal([]byte(raw), &task); err != nil {
		return err
	}
	switch action.Action {
	case "pause", "resume":
		task.Enabled = action.Action == "resume"
	case "run":
		if !task.Enabled || len(task.NodeIDs) == 0 {
			return model.ErrConflict
		}
		// Requests share one timestamp rather than a queue. Use the next second
		// so a lease issued earlier in this second cannot swallow the request.
		// Each leaf runs once after this timestamp, and
		// active leases keep the existing per-node concurrency bound.
		_, err = tx.ExecContext(ctx, "UPDATE bindings SET due=max(due,?) WHERE task_id=?", now.Unix()+1, id)
		if err != nil {
			return err
		}
	case "bind":
		task.NodeIDs = action.NodeIDs
		if err = task.Validate(); err != nil {
			return model.ErrInvalid
		}
		for _, node := range task.NodeIDs {
			var exists int
			if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM nodes WHERE id=?", node).Scan(&exists); err != nil {
				return err
			}
			if exists == 0 {
				return model.ErrInvalid
			}
		}
		// Keep due timestamps for unchanged bindings and history for removed nodes.
		rows, e := tx.QueryContext(ctx, "SELECT node_id FROM bindings WHERE task_id=?", id)
		if e != nil {
			return e
		}
		var removed []string
		keep := map[string]bool{}
		for _, node := range task.NodeIDs {
			keep[node] = true
		}
		for rows.Next() {
			var node string
			if e = rows.Scan(&node); e != nil {
				rows.Close()
				return e
			}
			if !keep[node] {
				removed = append(removed, node)
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for _, node := range removed {
			if _, err = tx.ExecContext(ctx, "DELETE FROM bindings WHERE task_id=? AND node_id=?", id, node); err != nil {
				return err
			}
		}
		for _, node := range task.NodeIDs {
			if _, err = tx.ExecContext(ctx, "INSERT INTO bindings(task_id,node_id) VALUES(?,?) ON CONFLICT DO NOTHING", id, node); err != nil {
				return err
			}
		}
	default:
		return model.ErrInvalid
	}
	if _, err = tx.ExecContext(ctx, "UPDATE tasks SET config=? WHERE id=?", encode(task), id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) TaskRuns(ctx context.Context, id string, now time.Time) ([]model.TaskRun, error) {
	var exists int
	if err := s.queryRow(ctx, "SELECT count(*) FROM tasks WHERE id=?", id).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, model.ErrNotFound
	}
	// Latest attempt for each bound node and probe target. Never expose raw hops.
	rows, err := s.query(ctx, `SELECT n.id,json_extract(n.config,'$.name'),j.issued,j.finished,j.task,j.result FROM bindings b JOIN nodes n ON n.id=b.node_id LEFT JOIN jobs j ON j.id IN (SELECT id FROM (SELECT id,row_number() OVER(PARTITION BY json_extract(task,'$.kind'),json_extract(task,'$.family'),json_extract(task,'$.host'),json_extract(task,'$.port'),json_extract(task,'$.region'),json_extract(task,'$.carrier') ORDER BY issued DESC,id DESC) pos FROM jobs WHERE task_id=b.task_id AND node_id=b.node_id) WHERE pos=1) WHERE b.task_id=? ORDER BY n.id,j.issued DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.TaskRun{}
	for rows.Next() {
		var r model.TaskRun
		var issued, finished sql.NullInt64
		var task, result sql.NullString
		if err = rows.Scan(&r.NodeID, &r.NodeName, &issued, &finished, &task, &result); err != nil {
			return nil, err
		}
		r.Issued = issued.Int64
		r.Finished = finished.Int64
		r.Status = "never"
		if task.Valid {
			var t model.Task
			if err = json.Unmarshal([]byte(task.String), &t); err != nil {
				return nil, err
			}
			r.Kind = t.Kind
			r.Family = t.Family
			r.Host = t.Host
			r.Port = t.Port
			r.Region = t.Region
			r.Carrier = t.Carrier
			r.Status = "pending"
			if now.Unix()-r.Issued > 90 {
				r.Status = "timeout"
				r.Reason = "timeout"
			}
		}
		if result.Valid {
			var v model.Result
			if err = json.Unmarshal([]byte(result.String), &v); err != nil {
				return nil, err
			}
			r.Status = v.Status
			r.Reason = v.Reason
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
