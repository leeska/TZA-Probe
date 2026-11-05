package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/leeska/TZA-Probe/internal/ipsentinel"
	"github.com/leeska/TZA-Probe/internal/model"
)

const executorScheduleSchema = `
CREATE TABLE IF NOT EXISTS executor_schedules(
 node_id TEXT PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
 payload TEXT NOT NULL, revision INTEGER NOT NULL,
 last_tick INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS executor_slots(
 node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
 binding TEXT NOT NULL, local_date TEXT NOT NULL, local_time TEXT NOT NULL,
 timezone TEXT NOT NULL, schedule_id TEXT NOT NULL, schedule_revision INTEGER NOT NULL,
 service_id TEXT NOT NULL, purpose TEXT NOT NULL, phase TEXT NOT NULL,
 due_at INTEGER NOT NULL, job_id TEXT NOT NULL, action_id TEXT,
 status TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(node_id,binding,local_date,local_time));
CREATE INDEX IF NOT EXISTS executor_slots_recent ON executor_slots(node_id,due_at DESC);
CREATE INDEX IF NOT EXISTS executor_slots_action ON executor_slots(action_id);
`

func (s *Store) SaveExecutorSchedule(ctx context.Context, node string, input ipsentinel.ScheduleInput) error {
	if err := input.ScheduleSpec.Validate(); err != nil {
		return model.Invalid(err.Error())
	}
	if input.ExpectedRevision < 0 || input.ExpectedRevision >= ipsentinel.MaxSafeInt {
		return model.Invalid("日程修订无效")
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	binding, hello, _, err := executorBindingTx(ctx, tx, node)
	if err != nil {
		return err
	}
	now := nowFunc().UTC()
	if err := requireFreshExecutorSnapshot(ctx, tx, node, now); err != nil {
		return err
	}
	if err := validateManagedHello(binding, hello, now); err != nil {
		return err
	}
	if hello.Status.Config == nil || *hello.Status.Config != input.Config {
		return model.Conflict("配置已变化，请重新核对日程")
	}
	var configRaw string
	if err := tx.QueryRowContext(ctx, `SELECT config FROM executor_configs WHERE node_id=? AND binding=? AND revision=? AND digest=?`, node, encode(binding), input.Config.Revision, input.Config.SHA256).Scan(&configRaw); errors.Is(err, sql.ErrNoRows) {
		return model.Conflict("缺少已应用的配置快照")
	} else if err != nil {
		return err
	}
	var config ipsentinel.Config
	if err := json.Unmarshal([]byte(configRaw), &config); err != nil {
		return err
	}
	if input.TimeoutMS > config.Limits.TimeoutMS || int64(len(input.Slots)) > config.DailyBudget.Runs || int64(len(input.Slots))*config.Limits.NetworkBytes > config.DailyBudget.NetworkBytes {
		return model.Invalid("日程总预留超过节点每日次数、网络或单次超时限额")
	}
	for _, purpose := range []string{ipsentinel.PurposeObserve, ipsentinel.PurposeExperiment} {
		for _, service := range input.Services(purpose) {
			action := ipsentinel.ExecutorAction{ConfigRevision: input.Config.Revision, ConfigSHA256: input.Config.SHA256, ServiceID: service, Purpose: purpose, TimeoutMS: input.TimeoutMS}
			if err := validateExecutorJob(ctx, tx, node, binding, *hello, action); err != nil {
				return err
			}
		}
	}
	schedule := ipsentinel.Schedule{ScheduleSpec: input.ScheduleSpec, ID: ipsentinel.NewUUID(), Binding: binding, DailyBudget: config.DailyBudget, NetworkPerRun: config.Limits.NetworkBytes}
	var previousRaw string
	var revision int64
	err = tx.QueryRowContext(ctx, "SELECT payload,revision FROM executor_schedules WHERE node_id=?", node).Scan(&previousRaw, &revision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if revision != input.ExpectedRevision {
		return model.Conflict("日程已变化，请刷新后核对")
	}
	if previousRaw != "" {
		var previous ipsentinel.Schedule
		if err := json.Unmarshal([]byte(previousRaw), &previous); err != nil {
			return err
		}
		schedule.ID = previous.ID
	}
	schedule.Revision = revision + 1
	schedule.Slots = schedule.SortedSlots()
	if err := cancelUndispatchedScheduleTx(ctx, tx, node, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO executor_schedules(node_id,payload,revision,last_tick,updated_at) VALUES(?,?,?,?,?)
 ON CONFLICT(node_id) DO UPDATE SET payload=excluded.payload,revision=excluded.revision,last_tick=max(executor_schedules.last_tick,excluded.last_tick),updated_at=excluded.updated_at`, node, encode(schedule), schedule.Revision, now.UnixMilli(), now.UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

func cancelUndispatchedScheduleTx(ctx context.Context, tx *sql.Tx, node string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE executor_actions SET status='cancelled',updated_at=?
 WHERE node_id=? AND kind='run.submit' AND status='pending' AND attempt=0
 AND id IN (SELECT action_id FROM executor_slots WHERE node_id=?)`, now.Unix(), node, node)
	return err
}

func disableExecutorScheduleTx(ctx context.Context, tx *sql.Tx, node string, now time.Time, expected *int64) error {
	var raw string
	var revision int64
	err := tx.QueryRowContext(ctx, "SELECT payload,revision FROM executor_schedules WHERE node_id=?", node).Scan(&raw, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		if expected != nil {
			return model.NotFound("尚未创建日程")
		}
		return nil
	}
	if err != nil {
		return err
	}
	if expected != nil && *expected != revision {
		return model.Conflict("日程已变化，请刷新后核对")
	}
	var schedule ipsentinel.Schedule
	if err := json.Unmarshal([]byte(raw), &schedule); err != nil {
		return err
	}
	schedule.Enabled, schedule.Revision = false, revision+1
	if err := cancelUndispatchedScheduleTx(ctx, tx, node, now); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE executor_schedules SET payload=?,revision=?,last_tick=max(last_tick,?),updated_at=? WHERE node_id=?", encode(schedule), schedule.Revision, now.UnixMilli(), now.UnixMilli(), node)
	return err
}

func (s *Store) DisableExecutorSchedule(ctx context.Context, node string, expected int64) error {
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := disableExecutorScheduleTx(ctx, tx, node, nowFunc(), &expected); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ExecutorSchedule(ctx context.Context, node string) (ipsentinel.ScheduleView, error) {
	view := ipsentinel.ScheduleView{Coverage: []ipsentinel.ScheduleCoverage{}, Recent: []ipsentinel.ScheduledRun{}}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return view, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT 1 FROM nodes WHERE id=?", node).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return view, model.NotFound("节点不存在")
	} else if err != nil {
		return view, err
	}
	var raw string
	err = tx.QueryRowContext(ctx, "SELECT payload FROM executor_schedules WHERE node_id=?", node).Scan(&raw)
	if err == nil {
		if err := json.Unmarshal([]byte(raw), &view.Schedule); err != nil {
			return view, err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return view, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT s.local_date,s.local_time,s.timezone,s.service_id,s.purpose,s.phase,s.job_id,coalesce(s.action_id,''),s.due_at,s.status,s.reason,coalesce(a.status,''),coalesce(json_extract(a.run,'$.state'),'')
 FROM executor_slots s LEFT JOIN executor_actions a ON a.id=s.action_id WHERE s.node_id=? ORDER BY s.due_at DESC,s.rowid DESC LIMIT 50`, node)
	if err != nil {
		return view, err
	}
	for rows.Next() {
		var entry ipsentinel.ScheduledRun
		if err := rows.Scan(&entry.LocalDate, &entry.LocalTime, &entry.Timezone, &entry.ServiceID, &entry.Purpose, &entry.Phase, &entry.JobID, &entry.ActionID, &entry.DueAt, &entry.Status, &entry.Reason, &entry.ActionStatus, &entry.RunState); err != nil {
			rows.Close()
			return view, err
		}
		view.Recent = append(view.Recent, entry)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return view, err
	}
	if err := tx.Commit(); err != nil {
		return view, err
	}
	if view.Schedule != nil {
		view.Coverage = view.Schedule.Coverage(nowFunc())
	}
	return view, nil
}

func (s *Store) TickExecutorSchedules(ctx context.Context, now time.Time) error {
	rows, err := s.query(ctx, `SELECT node_id FROM executor_schedules WHERE json_extract(payload,'$.enabled')=1 ORDER BY node_id`)
	if err != nil {
		return err
	}
	nodes := []string{}
	for rows.Next() {
		var node string
		if err := rows.Scan(&node); err != nil {
			rows.Close()
			return err
		}
		nodes = append(nodes, node)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var failures []error
	for _, node := range nodes {
		if err := s.tickExecutorSchedule(ctx, node, now.UTC()); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (s *Store) tickExecutorSchedule(ctx context.Context, node string, now time.Time) error {
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw string
	var tick, updated int64
	err = tx.QueryRowContext(ctx, "SELECT payload,last_tick,updated_at FROM executor_schedules WHERE node_id=?", node).Scan(&raw, &tick, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var schedule ipsentinel.Schedule
	if err := json.Unmarshal([]byte(raw), &schedule); err != nil {
		return err
	}
	if !schedule.Enabled || now.UnixMilli() <= tick {
		return nil
	}
	if _, err := tx.ExecContext(ctx, "UPDATE executor_schedules SET last_tick=? WHERE node_id=?", now.UnixMilli(), node); err != nil {
		return err
	}
	location, err := time.LoadLocation(schedule.Timezone)
	if err != nil {
		return err
	}
	local := now.In(location)
	due := now.Truncate(time.Minute)
	if due.UnixMilli() <= tick || due.UnixMilli() <= updated {
		return tx.Commit()
	}
	for _, slot := range schedule.Slots {
		if slot.Time != local.Format("15:04") {
			continue
		}
		first, exists := ipsentinel.ScheduledSlotTime(local, slot)
		if !exists || !first.Equal(due) {
			continue
		}
		localDate := local.Format("2006-01-02")
		var recorded int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM executor_slots WHERE node_id=? AND binding=? AND local_date=? AND local_time=?", node, encode(schedule.Binding), localDate, slot.Time).Scan(&recorded); err != nil {
			return err
		}
		if recorded > 0 {
			continue
		}
		service := schedule.ServiceForSlot(local, slot)
		jobID := ipsentinel.ScheduledJobID(node, schedule.Binding, localDate, slot.Time)
		status, reason := "queued", ""
		var actionID any
		action := ipsentinel.ExecutorAction{
			ID: ipsentinel.NewUUID(), Kind: ipsentinel.MethodRunSubmit, Binding: schedule.Binding,
			JobID: jobID, ServiceID: service, Purpose: slot.Purpose(), Phase: slot.Phase,
			ConfigRevision: schedule.Config.Revision, ConfigSHA256: schedule.Config.SHA256,
			NotBefore: due.Format(time.RFC3339), ExpiresAt: due.Add(time.Minute).Format(time.RFC3339), TimeoutMS: schedule.TimeoutMS,
		}
		binding, hello, _, bindingErr := executorBindingTx(ctx, tx, node)
		switch {
		case now.Sub(time.UnixMilli(tick)) > 15*time.Second:
			status, reason = "skipped", "controller_gap"
		case bindingErr != nil:
			if !errors.Is(bindingErr, model.ErrNotFound) {
				return bindingErr
			}
			status, reason = "skipped", "binding_changed"
		case binding != schedule.Binding:
			status, reason = "skipped", "binding_changed"
		case hello == nil || !scheduleBudgetAvailable(schedule, *hello, now):
			status, reason = "skipped", "daily_budget_exhausted"
		default:
			if err := createExecutorActionTx(ctx, tx, node, action, now); err != nil {
				if !errors.Is(err, model.ErrConflict) && !errors.Is(err, model.ErrInvalid) && !errors.Is(err, model.ErrNotFound) {
					return err
				}
				status, reason = "skipped", "executor_ineligible"
			} else {
				var persistedID string
				if err := tx.QueryRowContext(ctx, `SELECT id FROM executor_actions WHERE node_id=? AND binding=? AND kind='run.submit' AND lower(json_extract(payload,'$.job_id'))=?`, node, encode(schedule.Binding), jobID).Scan(&persistedID); err != nil {
					return err
				}
				actionID = persistedID
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO executor_slots(node_id,binding,local_date,local_time,timezone,schedule_id,schedule_revision,service_id,purpose,phase,due_at,job_id,action_id,status,reason)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, node, encode(schedule.Binding), localDate, slot.Time, schedule.Timezone, schedule.ID, schedule.Revision, service, slot.Purpose(), slot.Phase, due.Unix(), jobID, actionID, status, reason); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func scheduleBudgetAvailable(schedule ipsentinel.Schedule, hello ipsentinel.Hello, now time.Time) bool {
	budget := hello.Status.Budget
	if budget == nil || budget.DayUTC != now.UTC().Format("2006-01-02") || budget.UsedRuns >= budget.Limits.Runs || budget.AccountedNetworkBytes > budget.Limits.NetworkBytes {
		return false
	}
	return schedule.NetworkPerRun <= budget.Limits.NetworkBytes-budget.AccountedNetworkBytes
}
