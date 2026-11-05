package storage

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/ipsentinel"
	"github.com/leeska/TZA-Probe/internal/model"
)

func scheduleFixture(t *testing.T, now time.Time) (*Store, string, ipsentinel.SyncReport, ipsentinel.ScheduleInput, *time.Time) {
	t.Helper()
	store, node, _, report, config := managedFixture(t)
	applyManagedConfig(t, store, node, &report, config)
	report.Hello.Status.Budget = &ipsentinel.BudgetStatus{DayUTC: now.UTC().Format("2006-01-02"), Limits: config.DailyBudget, NetworkAccounting: "sampled"}
	clock := now
	previous := nowFunc
	nowFunc = func() time.Time { return clock }
	t.Cleanup(func() { nowFunc = previous })
	if _, err := store.db.Exec("UPDATE executor_bindings SET hello=NULL,received_at=0 WHERE node_id=?", node); err != nil {
		t.Fatal(err)
	}
	syncScheduleAt(t, store, node, &report, clock)
	input := ipsentinel.ScheduleInput{ScheduleSpec: ipsentinel.ScheduleSpec{
		Enabled: true, Timezone: "UTC", Config: *report.Hello.Status.Config,
		ObserveServices: []string{"google"}, ExperimentServices: []string{"google"},
		Slots: []ipsentinel.ScheduleSlot{{Time: "08:00", Phase: "followup"}, {Time: "20:00", Phase: "experiment"}}, TimeoutMS: 30000,
	}}
	return store, node, report, input, &clock
}

func syncScheduleAt(t *testing.T, store *Store, node string, report *ipsentinel.SyncReport, now time.Time) {
	t.Helper()
	report.Hello.Status.ObservedAt = now.UTC().Format(time.RFC3339Nano)
	report.Hello.Status.Assessments = []ipsentinel.Assessment{}
	if report.Hello.Status.Budget != nil {
		report.Hello.Status.Budget.DayUTC = now.UTC().Format("2006-01-02")
	}
	if _, err := store.SaveExecutorRecords(context.Background(), node, *report, now); err != nil {
		t.Fatal(err)
	}
}

func scheduleAt(t *testing.T, value string) time.Time {
	t.Helper()
	now, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return now
}

func TestExecutorScheduleAtomicTickDedupAndDisable(t *testing.T) {
	start := scheduleAt(t, "2026-11-05T07:59:55Z")
	store, node, report, input, clock := scheduleFixture(t, start)
	ctx := context.Background()
	if err := store.SaveExecutorSchedule(ctx, node, input); err != nil {
		t.Fatal(err)
	}
	*clock = start.Add(7 * time.Second)
	syncScheduleAt(t, store, node, &report, *clock)
	var workers sync.WaitGroup
	for range 4 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if err := store.TickExecutorSchedules(ctx, *clock); err != nil {
				t.Error(err)
			}
		}()
	}
	workers.Wait()
	view, err := store.ExecutorSchedule(ctx, node)
	if err != nil || len(view.Recent) != 1 || view.Recent[0].Status != "queued" || view.Recent[0].Purpose != "observe" {
		t.Fatal("slot not durably queued exactly once", view, err)
	}
	entry := view.Recent[0]
	actions, err := store.ExecutorActions(ctx, node)
	if err != nil || actions[0].ID != entry.ActionID || actions[0].JobID != entry.JobID || actions[0].ExpiresAt != "2026-11-05T08:01:00Z" {
		t.Fatal("slot and action not committed together", actions, err)
	}
	if err := store.DisableExecutorSchedule(ctx, node, 0); !errors.Is(err, model.ErrConflict) {
		t.Fatal("stale administrator disabled edited schedule", err)
	}
	if err := store.DisableExecutorSchedule(ctx, node, view.Schedule.Revision); err != nil {
		t.Fatal(err)
	}
	actions, err = store.ExecutorActions(ctx, node)
	if err != nil || actions[0].Status != "cancelled" {
		t.Fatal("disabled schedule retained an undispatched action", actions, err)
	}
	view, err = store.ExecutorSchedule(ctx, node)
	if err != nil || view.Schedule.Enabled || len(view.Recent) != 1 {
		t.Fatal("disable lost history", view, err)
	}
}

func TestExecutorScheduleDoesNotBackfillOrRecoverSkippedSlots(t *testing.T) {
	for _, scenario := range []string{"controller_gap", "stale", "pause", "config_changed", "daily_budget"} {
		t.Run(scenario, func(t *testing.T) {
			start := scheduleAt(t, "2026-11-05T07:59:55Z")
			store, node, report, input, clock := scheduleFixture(t, start)
			ctx := context.Background()
			if scenario == "stale" {
				if _, err := store.db.Exec("UPDATE executor_bindings SET received_at=? WHERE node_id=?", start.Add(-time.Minute).Unix(), node); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "pause" {
				if _, err := store.db.Exec("UPDATE executor_bindings SET dispatch_paused=1 WHERE node_id=?", node); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "daily_budget" {
				report.Hello.Status.Budget.UsedRuns = report.Hello.Status.Budget.Limits.Runs
			}
			if err := store.SaveExecutorSchedule(ctx, node, input); err != nil {
				if scenario == "stale" {
					syncScheduleAt(t, store, node, &report, start)
					if err := store.SaveExecutorSchedule(ctx, node, input); err != nil {
						t.Fatal(err)
					}
					if _, err := store.db.Exec("UPDATE executor_bindings SET received_at=? WHERE node_id=?", start.Add(-time.Minute).Unix(), node); err != nil {
						t.Fatal(err)
					}
				} else {
					t.Fatal(err)
				}
			}
			*clock = start.Add(7 * time.Second)
			if scenario == "controller_gap" {
				*clock = start.Add(40 * time.Second)
			}
			if scenario == "config_changed" {
				report.Hello.Status.Config.Revision++
				syncScheduleAt(t, store, node, &report, *clock)
			}
			if scenario == "daily_budget" {
				syncScheduleAt(t, store, node, &report, *clock)
			}
			if err := store.TickExecutorSchedules(ctx, *clock); err != nil {
				t.Fatal(err)
			}
			view, err := store.ExecutorSchedule(ctx, node)
			if err != nil || len(view.Recent) != 1 || view.Recent[0].Status != "skipped" || view.Recent[0].ActionID != "" {
				t.Fatal("ineligible slot queued", view, err)
			}
			if _, err := store.db.Exec("UPDATE executor_bindings SET dispatch_paused=0 WHERE node_id=?", node); err != nil {
				t.Fatal(err)
			}
			*clock = clock.Add(5 * time.Second)
			syncScheduleAt(t, store, node, &report, *clock)
			if err := store.TickExecutorSchedules(ctx, *clock); err != nil {
				t.Fatal(err)
			}
			view, err = store.ExecutorSchedule(ctx, node)
			if err != nil || len(view.Recent) != 1 || view.Recent[0].Status != "skipped" {
				t.Fatal("reconnect backfilled skipped slot", view, err)
			}
		})
	}
}

func TestExecutorScheduleDSTFoldAndRebind(t *testing.T) {
	start := scheduleAt(t, "2026-11-01T05:29:55Z")
	store, node, report, input, clock := scheduleFixture(t, start)
	input.Timezone = "America/New_York"
	input.Slots = []ipsentinel.ScheduleSlot{{Time: "01:30", Phase: "followup"}}
	input.ExperimentServices = nil
	ctx := context.Background()
	if err := store.SaveExecutorSchedule(ctx, node, input); err != nil {
		t.Fatal(err)
	}
	*clock = start.Add(7 * time.Second)
	syncScheduleAt(t, store, node, &report, *clock)
	if err := store.TickExecutorSchedules(ctx, *clock); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("UPDATE executor_schedules SET last_tick=? WHERE node_id=?", scheduleAt(t, "2026-11-01T06:29:55Z").UnixMilli(), node); err != nil {
		t.Fatal(err)
	}
	*clock = scheduleAt(t, "2026-11-01T06:30:02Z")
	syncScheduleAt(t, store, node, &report, *clock)
	if err := store.TickExecutorSchedules(ctx, *clock); err != nil {
		t.Fatal(err)
	}
	view, err := store.ExecutorSchedule(ctx, node)
	if err != nil || len(view.Recent) != 1 {
		t.Fatal("DST repeat created second slot", view, err)
	}
	binding := view.Schedule.Binding
	binding.Epoch++
	if err := store.BindExecutor(ctx, node, binding); err != nil {
		t.Fatal(err)
	}
	view, err = store.ExecutorSchedule(ctx, node)
	if err != nil || view.Schedule.Enabled || len(view.Recent) != 1 {
		t.Fatal("new epoch silently adopted previous schedule", view, err)
	}
}

func TestExecutorScheduleBudgetCASAndUnknownService(t *testing.T) {
	start := scheduleAt(t, "2026-11-05T07:59:55Z")
	store, node, _, input, _ := scheduleFixture(t, start)
	ctx := context.Background()
	if err := store.SaveExecutorSchedule(ctx, node, input); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveExecutorSchedule(ctx, node, input); !errors.Is(err, model.ErrConflict) {
		t.Fatal("schedule lost-update accepted", err)
	}
	input.ExpectedRevision = 1
	for _, slot := range []string{"01:00", "02:00", "03:00", "04:00", "05:00"} {
		input.Slots = append(input.Slots, ipsentinel.ScheduleSlot{Time: slot, Phase: "followup"})
	}
	if err := store.SaveExecutorSchedule(ctx, node, input); !errors.Is(err, model.ErrInvalid) {
		t.Fatal("services multiplied fixed daily network budget", err)
	}
	input.Slots = input.Slots[:2]
	input.ObserveServices = []string{"unapproved"}
	if err := store.SaveExecutorSchedule(ctx, node, input); !errors.Is(err, model.ErrInvalid) {
		t.Fatal("unknown service scheduled", err)
	}
}

func TestExecutorScheduleRestartGapAndClockRollbackNeverBackfill(t *testing.T) {
	start := scheduleAt(t, "2026-11-05T07:59:55Z")
	store, node, report, input, clock := scheduleFixture(t, start)
	ctx := context.Background()
	if err := store.SaveExecutorSchedule(ctx, node, input); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"2026-11-05T09:00:02Z", "2026-11-05T08:00:02Z"} {
		*clock = scheduleAt(t, value)
		if err := store.TickExecutorSchedules(ctx, *clock); err != nil {
			t.Fatal(err)
		}
	}
	view, err := store.ExecutorSchedule(ctx, node)
	if err != nil || len(view.Recent) != 0 {
		t.Fatal("server restart or clock rollback backfilled morning slot", view, err)
	}
	*clock = scheduleAt(t, "2026-11-05T19:59:55Z")
	if err := store.TickExecutorSchedules(ctx, *clock); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(7 * time.Second)
	syncScheduleAt(t, store, node, &report, *clock)
	if err := store.TickExecutorSchedules(ctx, *clock); err != nil {
		t.Fatal(err)
	}
	view, err = store.ExecutorSchedule(ctx, node)
	if err != nil || len(view.Recent) != 1 || view.Recent[0].Purpose != "experiment" || view.Recent[0].Status != "queued" {
		t.Fatal("future experiment slot was mixed with missed observation", view, err)
	}
}

func TestExecutorScheduleDSTSecondOccurrenceDoesNotReplaceMissedFirst(t *testing.T) {
	start := scheduleAt(t, "2026-11-01T06:29:55Z")
	store, node, report, input, clock := scheduleFixture(t, start)
	input.Timezone, input.ExperimentServices = "America/New_York", nil
	input.Slots = []ipsentinel.ScheduleSlot{{Time: "01:30", Phase: "followup"}}
	ctx := context.Background()
	if err := store.SaveExecutorSchedule(ctx, node, input); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(7 * time.Second)
	syncScheduleAt(t, store, node, &report, *clock)
	if err := store.TickExecutorSchedules(ctx, *clock); err != nil {
		t.Fatal(err)
	}
	view, err := store.ExecutorSchedule(ctx, node)
	if err != nil || len(view.Recent) != 0 {
		t.Fatal("DST second occurrence backfilled missed first occurrence", view, err)
	}
}
