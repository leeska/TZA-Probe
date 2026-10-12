package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

// The task controls change management state without touching the probe
// definition, so unlike SaveTask they keep every historical result.

// loadTask reads one task's config inside tx.
func loadTask(ctx context.Context, tx *sql.Tx, id string) (model.Task, error) {
	var raw string
	var task model.Task
	if err := tx.QueryRowContext(ctx, "SELECT config FROM tasks WHERE id=?", id).Scan(&raw); errors.Is(err, sql.ErrNoRows) {
		return task, model.ErrNotFound
	} else if err != nil {
		return task, err
	}
	return task, json.Unmarshal([]byte(raw), &task)
}

// SetTaskEnabled pauses or resumes a task. Pausing does not cancel a probe an
// Agent has already leased; resuming keeps each binding's due time.
func (s *Store) SetTaskEnabled(ctx context.Context, id string, enabled bool) error {
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	task, err := loadTask(ctx, tx, id)
	if err != nil {
		return err
	}
	task.Enabled = enabled
	if _, err = tx.ExecContext(ctx, "UPDATE tasks SET config=? WHERE id=?", encode(task), id); err != nil {
		return err
	}
	return tx.Commit()
}

// RunTask asks every bound node to run the task once, as soon as it polls.
func (s *Store) RunTask(ctx context.Context, id string, now time.Time) error {
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	task, err := loadTask(ctx, tx, id)
	if err != nil {
		return err
	}
	if !task.Enabled || len(task.NodeIDs) == 0 {
		return model.Conflict("任务已暂停或未绑定节点，无法立即执行")
	}
	// Requests share one timestamp rather than a queue. Use the next second
	// so a lease issued earlier in this second cannot swallow the request.
	// Each leaf runs once after this timestamp, and
	// active leases keep the existing per-node concurrency bound.
	if _, err = tx.ExecContext(ctx, "UPDATE bindings SET due=max(due,?) WHERE task_id=?", now.Unix()+1, id); err != nil {
		return err
	}
	return tx.Commit()
}

// BindTasks gives every listed task the same node list, replacing its
// bindings, in one transaction: either every task is rebound or none is. An
// empty node list stops the tasks from running.
func (s *Store) BindTasks(ctx context.Context, taskIDs, nodeIDs []string) error {
	if len(taskIDs) == 0 {
		return model.Invalid("请至少选择一个任务")
	}
	if nodeIDs == nil {
		nodeIDs = []string{}
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, node := range nodeIDs {
		var exists int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM nodes WHERE id=?", node).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return model.Invalid("节点不存在：" + node)
		}
	}
	seen := map[string]bool{}
	for _, id := range taskIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		if err = bindTask(ctx, tx, id, nodeIDs); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func bindTask(ctx context.Context, tx *sql.Tx, id string, nodeIDs []string) error {
	task, err := loadTask(ctx, tx, id)
	if err != nil {
		return err
	}
	task.NodeIDs = nodeIDs
	if err = task.Validate(); err != nil {
		return model.Invalid(err.Error())
	}
	// Keep due timestamps for unchanged bindings and history for removed nodes.
	rows, err := tx.QueryContext(ctx, "SELECT node_id FROM bindings WHERE task_id=?", id)
	if err != nil {
		return err
	}
	var removed []string
	keep := map[string]bool{}
	for _, node := range nodeIDs {
		keep[node] = true
	}
	for rows.Next() {
		var node string
		if err = rows.Scan(&node); err != nil {
			rows.Close()
			return err
		}
		if !keep[node] {
			removed = append(removed, node)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, node := range removed {
		if _, err = tx.ExecContext(ctx, "DELETE FROM bindings WHERE task_id=? AND node_id=?", id, node); err != nil {
			return err
		}
		// 补测行按 taskID|… 记、没有外键，解绑的节点上不会再有人领走它。
		if _, err = tx.ExecContext(ctx, "DELETE FROM retries WHERE node_id=? AND probe_key LIKE ?", node, id+"|%"); err != nil {
			return err
		}
	}
	// 解锁结论是这个任务在那台机器上跑出来的派生状态：解绑后不再有人更新它，留着就是一份
	// 永远不变的分数（与 SaveTask / DeleteTask 的处理一致）。
	if task.Type == model.TaskUnlock {
		if err = clearUnlocks(ctx, tx, removed); err != nil {
			return err
		}
	}
	for _, node := range nodeIDs {
		if _, err = tx.ExecContext(ctx, "INSERT INTO bindings(task_id,node_id) VALUES(?,?) ON CONFLICT DO NOTHING", id, node); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, "UPDATE tasks SET config=? WHERE id=?", encode(task), id)
	return err
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
	// The materialized latest table already holds exactly that row per node and
	// probe key (taskID|kind|family|host|port|region|carrier), so this is a
	// primary-key range per binding. Ranking the task's jobs instead re-read the
	// whole task history for every binding: 69s with ten nodes and a week of it.
	rows, err := s.query(ctx, `SELECT n.id,json_extract(n.config,'$.name'),l.issued,l.finished,l.task,l.result FROM bindings b JOIN nodes n ON n.id=b.node_id LEFT JOIN latest l ON l.node_id=b.node_id AND l.probe_key>=? AND l.probe_key<? WHERE b.task_id=? ORDER BY n.id,l.issued DESC`, id+"|", id+"}", id)
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
			var t model.Probe
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
			if now.Unix()-r.Issued > int64(finishWindow(t.Kind).Seconds()) {
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
