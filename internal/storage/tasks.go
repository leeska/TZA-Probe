package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

// DeleteTask removes a task and, for an unlock task, the verdicts it produced:
// they are derived state, and leaving them behind would show a machine's unlock
// panel for a check nothing runs any more.
func (s *Store) DeleteTask(ctx context.Context, id string) error {
	// 先拿写锁再读旧配置：锁外读的话，读完到删除之间一次并发的 SaveTask / bind 可能已经改了
	// 绑定，按旧的 node_ids 清解锁结论会漏掉新绑定的节点。所有写入都持这把锁，锁内从读池
	// 读到的就是最新提交的配置。
	s.writes.Lock()
	defer s.writes.Unlock()
	t, err := s.taskConfig(ctx, id)
	if err != nil {
		return err
	}
	// 任务行与绑定、面板物化行立刻删；它的探测历史（可能上百万行）后台分批删，见 purge.go。
	err = s.withoutCascade(ctx, func(tx *sql.Tx) error {
		if t.Type == model.TaskUnlock {
			if err := clearUnlocks(ctx, tx, t.NodeIDs); err != nil {
				return err
			}
		}
		r, err := tx.ExecContext(ctx, "DELETE FROM tasks WHERE id=?", id)
		if err != nil {
			return err
		}
		if n, _ := r.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM bindings WHERE task_id=?", id); err != nil {
			return err
		}
		if err = dropTaskState(ctx, tx, id); err != nil {
			return err
		}
		return queuePurge(ctx, tx, purgeTaskJobs, id, noCutoff)
	})
	if err == nil {
		s.wakePurger()
	}
	return err
}

// dropTaskState removes what the panel and the scheduler read for a task: its
// rows in the materialized latest table (keyed taskID|…), its pending route
// retries, and its jobs still in flight. Older finished jobs are left to a purge.
func dropTaskState(ctx context.Context, tx *sql.Tx, id string) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM latest WHERE probe_key>=? AND probe_key<?", id+"|", id+"}"); err != nil {
		return err
	}
	// 补测行按 probe_key（taskID|…）记，没有指向 tasks 的外键。
	if _, err := tx.ExecContext(ctx, "DELETE FROM retries WHERE probe_key>=? AND probe_key<?", id+"|", id+"}"); err != nil {
		return err
	}
	// In flight means a result may still be accepted (see finishWindow); older
	// unfinished rows are dead and go with the purge. jobs_age keeps this a
	// short range scan instead of a walk over the task's whole history.
	_, err := tx.ExecContext(ctx, "DELETE FROM jobs WHERE rowid IN (SELECT rowid FROM jobs INDEXED BY jobs_age WHERE issued>=? AND task_id=? AND finished IS NULL)", time.Now().Add(-unlockFinishWindow).Unix(), id)
	return err
}

// taskConfig reads a stored task, used by the writers that must know what a
// task was before they change it.
func (s *Store) taskConfig(ctx context.Context, id string) (model.Task, error) {
	var config string
	err := s.queryRow(ctx, "SELECT config FROM tasks WHERE id=?", id).Scan(&config)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Task{}, ErrNotFound
	}
	if err != nil {
		return model.Task{}, err
	}
	var t model.Task
	if err = json.Unmarshal([]byte(config), &t); err != nil {
		return model.Task{}, err
	}
	return t, nil
}

// ProbeTargets maps node id → the distinct probe entries bound to it (kind / carrier / family /
// region, from every enabled task's leaves). See model.NodeView.ProbeTargets for why the panel
// needs this instead of inferring the cells from stored jobs.
func (s *Store) ProbeTargets(ctx context.Context) (map[string][]model.ProbeTarget, error) {
	tasks, err := s.Tasks(ctx)
	if err != nil {
		return nil, err
	}
	type key struct{ kind, carrier, family, region string }
	plan := map[string][]model.ProbeTarget{}
	seen := map[string]map[key]bool{}
	for _, t := range tasks {
		if !t.Enabled || t.Type == model.TaskBlock {
			continue
		}
		for _, leaf := range t.Leaves() {
			k := key{leaf.Kind, leaf.Carrier, leaf.Family, leaf.Region}
			for _, node := range t.NodeIDs {
				if seen[node] == nil {
					seen[node] = map[key]bool{}
				}
				if seen[node][k] {
					continue
				}
				seen[node][k] = true
				plan[node] = append(plan[node], model.ProbeTarget{
					Kind: k.kind, Carrier: k.carrier, Family: k.family, Region: k.region,
				})
			}
		}
	}
	return plan, nil
}

func (s *Store) Tasks(ctx context.Context) ([]model.Task, error) {
	rows, err := s.query(ctx, "SELECT config FROM tasks ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Task{}
	for rows.Next() {
		var data string
		var t model.Task
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(data), &t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SaveTask creates or updates a task and returns the stored id. An edit that
// changes what is probed (the task's probe keys) starts over: in-flight leases,
// results and history of the old definition go. An edit that keeps the probes —
// a new name, new intervals, other bindings, paused or resumed — keeps them, the
// same as the dedicated enable / bind operations.
func (s *Store) SaveTask(ctx context.Context, t model.Task, create bool) (string, error) {
	if err := t.Validate(); err != nil {
		return "", model.Invalid(err.Error())
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	for _, id := range t.NodeIDs {
		var exists int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM nodes WHERE id=?", id).Scan(&exists); err != nil {
			return "", err
		}
		if exists == 0 {
			return "", model.Invalid("绑定的节点不存在：" + id)
		}
	}
	if create {
		t.ID = model.Secret()[:16]
		if _, err = tx.ExecContext(ctx, "INSERT INTO tasks VALUES(?,?)", t.ID, encode(t)); err != nil {
			return "", err
		}
		for _, id := range t.NodeIDs {
			if _, err = tx.ExecContext(ctx, "INSERT INTO bindings(task_id,node_id) VALUES(?,?)", t.ID, id); err != nil {
				return "", err
			}
		}
		return t.ID, tx.Commit()
	}
	previous, err := loadTask(ctx, tx, t.ID)
	if err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE tasks SET config=? WHERE id=?", encode(t), t.ID); err != nil {
		return "", err
	}
	if sameKeys(previous.ProbeKeys(), t.ProbeKeys()) && previous.Type == t.Type {
		// 探测内容没变：只按绑定差异增删（保留未变绑定的调度时间与历史），与 BindTasks 同一条路。
		if err = bindTask(ctx, tx, t.ID, t.NodeIDs); err != nil {
			return "", err
		}
		return t.ID, tx.Commit()
	}
	// 解锁结论是从结果派生的：探测内容一变，旧结论必须跟着走，否则面板上会留着一份
	// 没有任务在跑的解锁面板。
	if previous.Type == model.TaskUnlock || t.Type == model.TaskUnlock {
		if err = clearUnlocks(ctx, tx, append(append([]string{}, previous.NodeIDs...), t.NodeIDs...)); err != nil {
			return "", err
		}
	}
	// Editing invalidates in-flight leases and the task's history. What the panel
	// and the scheduler read goes now; the history rows (up to a week of every
	// target on every node) are purged in the background up to the newest job
	// that exists at this moment, so jobs leased after the edit stay.
	if err = dropTaskState(ctx, tx, t.ID); err != nil {
		return "", err
	}
	cutoff, err := jobsCutoff(ctx, tx)
	if err != nil {
		return "", err
	}
	if err = queuePurge(ctx, tx, purgeTaskJobs, t.ID, cutoff); err != nil {
		return "", err
	}
	defer s.wakePurger()
	if _, err = tx.ExecContext(ctx, "DELETE FROM bindings WHERE task_id=?", t.ID); err != nil {
		return "", err
	}
	for _, id := range t.NodeIDs {
		if _, err = tx.ExecContext(ctx, "INSERT INTO bindings(task_id,node_id) VALUES(?,?)", t.ID, id); err != nil {
			return "", err
		}
	}
	return t.ID, tx.Commit()
}

func sameKeys(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}
