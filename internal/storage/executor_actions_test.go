package storage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/ipsentinel"
	"github.com/leeska/TZA-Probe/internal/model"
)

func managedFixture(t *testing.T) (*Store, string, ipsentinel.Binding, ipsentinel.SyncReport, ipsentinel.Config) {
	t.Helper()
	store := openTest(t)
	node, _ := makeNode(t, store)
	binding, report, _ := executorFixture(t)
	binding.Mode = ipsentinel.ModeManaged
	report.Hello.Status.Control.Mode = ipsentinel.ModeManaged
	report.Hello.Status.Control.ActiveRunID = nil
	report.Hello.Status.Control.Paused = false
	report.Hello.Status.Control.PauseReason = nil
	report.Hello.Status.Config = nil
	report.Hello.Status.ObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
	report.Page = ipsentinel.RecordPage{ExecutorInstanceID: binding.ExecutorInstanceID, Records: []ipsentinel.Record{}}
	data, err := os.ReadFile("../ipsentinel/testdata/contracts/ipsentinel-executor-v1/examples/config.json")
	if err != nil {
		t.Fatal(err)
	}
	var config ipsentinel.Config
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if err := store.BindExecutor(context.Background(), node, binding); err != nil {
		t.Fatal(err)
	}
	syncManaged(t, store, node, report)
	return store, node, binding, report, config
}

func syncManaged(t *testing.T, store *Store, node string, report ipsentinel.SyncReport) {
	t.Helper()
	if _, err := store.SaveExecutorRecords(context.Background(), node, report, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func createManaged(t *testing.T, store *Store, node string, action ipsentinel.ExecutorAction) ipsentinel.ExecutorAction {
	t.Helper()
	action.ID = ipsentinel.NewUUID()
	if err := store.CreateExecutorAction(context.Background(), node, action); err != nil {
		t.Fatal(err)
	}
	return action
}

func leaseManaged(t *testing.T, store *Store, node string, hello ipsentinel.Hello) ipsentinel.ExecutorAction {
	t.Helper()
	action, err := store.LeaseExecutorAction(context.Background(), node, hello)
	if err != nil {
		t.Fatal(err)
	}
	if action.ID == "" {
		t.Fatal("expected a leased action")
	}
	return action
}

func finishManaged(t *testing.T, store *Store, node string, action ipsentinel.ExecutorAction, result ipsentinel.ActionResult) {
	t.Helper()
	result.ID, result.LeaseID = action.ID, action.LeaseID
	if err := store.FinishExecutorAction(context.Background(), node, result); err != nil {
		t.Fatal(err)
	}
}

func applyManagedConfig(t *testing.T, store *Store, node string, report *ipsentinel.SyncReport, config ipsentinel.Config) {
	t.Helper()
	digest, err := ipsentinel.ConfigSHA256(config)
	if err != nil {
		t.Fatal(err)
	}
	createManaged(t, store, node, ipsentinel.ExecutorAction{Kind: ipsentinel.MethodConfigValidate, Config: &config})
	action := leaseManaged(t, store, node, report.Hello)
	finishManaged(t, store, node, action, ipsentinel.ActionResult{Accepted: true, Validation: &ipsentinel.Validation{Valid: true, ConfigSHA256: digest, Issues: []ipsentinel.ValidationIssue{}}})
	createManaged(t, store, node, ipsentinel.ExecutorAction{Kind: ipsentinel.MethodConfigApply, Config: &config})
	action = leaseManaged(t, store, node, report.Hello)
	finishManaged(t, store, node, action, ipsentinel.ActionResult{Accepted: true, Config: &ipsentinel.ConfigReceipt{Config: ipsentinel.ConfigRef{Revision: 1, SHA256: digest}, Changed: true}})
	report.Hello.Status.Config = &ipsentinel.ConfigRef{Revision: 1, SHA256: digest}
	syncManaged(t, store, node, *report)
}

func managedJob(report ipsentinel.SyncReport) ipsentinel.ExecutorAction {
	now := time.Now().UTC()
	return ipsentinel.ExecutorAction{Kind: ipsentinel.MethodRunSubmit, JobID: ipsentinel.NewUUID(), ConfigRevision: report.Hello.Status.Config.Revision, ConfigSHA256: report.Hello.Status.Config.SHA256, ServiceID: "google", Purpose: "observe", Phase: "baseline", NotBefore: now.Add(-time.Second).Format(time.RFC3339), ExpiresAt: now.Add(5 * time.Minute).Format(time.RFC3339), TimeoutMS: 30000}
}

func TestExecutorExplicitAuthorizationAndConfigConfirmation(t *testing.T) {
	store, node, binding, report, config := managedFixture(t)
	ctx := context.Background()
	binding.Mode = ""
	if err := store.BindExecutor(ctx, node, binding); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveExecutorRecords(ctx, node, report, time.Now()); err == nil {
		t.Fatal("managed runtime accepted under read-only grant")
	}
	if err := store.CreateExecutorAction(ctx, node, ipsentinel.ExecutorAction{ID: ipsentinel.NewUUID(), Kind: ipsentinel.MethodControlPause}); err == nil {
		t.Fatal("read-only binding executed command")
	}
	binding.Mode = ipsentinel.ModeManaged
	if err := store.BindExecutor(ctx, node, binding); err != nil {
		t.Fatal(err)
	}
	syncManaged(t, store, node, report)
	if err := store.CreateExecutorAction(ctx, node, ipsentinel.ExecutorAction{ID: ipsentinel.NewUUID(), Kind: ipsentinel.MethodConfigApply, Config: &config}); !errors.Is(err, model.ErrConflict) {
		t.Fatal("apply without local validation accepted", err)
	}
	digest, err := ipsentinel.ConfigSHA256(config)
	if err != nil {
		t.Fatal(err)
	}
	createManaged(t, store, node, ipsentinel.ExecutorAction{Kind: ipsentinel.MethodConfigValidate, Config: &config})
	invalidValidation := leaseManaged(t, store, node, report.Hello)
	finishManaged(t, store, node, invalidValidation, ipsentinel.ActionResult{Validation: &ipsentinel.Validation{Valid: false, ConfigSHA256: digest, Issues: []ipsentinel.ValidationIssue{{Pointer: "/services/0", Code: "policy_denied", Message: "fixture"}}}})
	if err := store.CreateExecutorAction(ctx, node, ipsentinel.ExecutorAction{ID: ipsentinel.NewUUID(), Kind: ipsentinel.MethodConfigApply, Config: &config}); !errors.Is(err, model.ErrConflict) {
		t.Fatal("negative local validation authorized config apply", err)
	}
	applyManagedConfig(t, store, node, &report, config)
	views, err := store.Executors(ctx)
	if err != nil || len(views) != 1 || views[0].Config == nil {
		t.Fatal("confirmed config missing", views, err)
	}
	createManaged(t, store, node, managedJob(report))
}

func TestExecutorLeaseRecoveryRejectsOldReceiptAndPreservesJob(t *testing.T) {
	store, node, _, report, config := managedFixture(t)
	applyManagedConfig(t, store, node, &report, config)
	action := createManaged(t, store, node, managedJob(report))
	first := leaseManaged(t, store, node, report.Hello)
	if other, err := store.LeaseExecutorAction(context.Background(), node, report.Hello); err != nil || other.ID != "" {
		t.Fatal("live lease issued twice", other, err)
	}
	if _, err := store.db.Exec("UPDATE executor_actions SET lease_until=0 WHERE id=?", action.ID); err != nil {
		t.Fatal(err)
	}
	second := leaseManaged(t, store, node, report.Hello)
	if second.LeaseID == first.LeaseID || second.RequestID != first.RequestID || second.Job() != first.Job() || second.Attempt != 2 {
		t.Fatal("lease changed logical job or replay request", first, second)
	}
	result := ipsentinel.ActionResult{ID: first.ID, LeaseID: first.LeaseID, Accepted: false, ErrorCode: ipsentinel.ErrCodeExpired, Message: "expired"}
	if err := store.FinishExecutorAction(context.Background(), node, result); !errors.Is(err, model.ErrConflict) {
		t.Fatal("late lease changed state", err)
	}
	result.LeaseID = second.LeaseID
	if err := store.FinishExecutorAction(context.Background(), node, result); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishExecutorAction(context.Background(), node, result); err != nil {
		t.Fatal("lost HTTP receipt not replayable", err)
	}
}

func TestExecutorBusyRetriesChangeRequestNotLogicalJob(t *testing.T) {
	store, node, _, report, config := managedFixture(t)
	applyManagedConfig(t, store, node, &report, config)
	createManaged(t, store, node, managedJob(report))
	first := leaseManaged(t, store, node, report.Hello)
	finishManaged(t, store, node, first, ipsentinel.ActionResult{Retryable: true, ErrorCode: ipsentinel.ErrCodeBusy, Message: "busy"})
	if other, err := store.LeaseExecutorAction(context.Background(), node, report.Hello); err != nil || other.ID != "" {
		t.Fatal("busy retries spin", other, err)
	}
	if _, err := store.db.Exec("UPDATE executor_actions SET lease_until=0 WHERE id=?", first.ID); err != nil {
		t.Fatal(err)
	}
	second := leaseManaged(t, store, node, report.Hello)
	if first.RequestID == second.RequestID || first.Job() != second.Job() {
		t.Fatal("busy retry replayed rejection or changed window", first, second)
	}
	result := ipsentinel.ActionResult{ID: second.ID, LeaseID: second.LeaseID, Retryable: true, ErrorCode: ipsentinel.ErrCodePolicyDenied, Message: "policy_denied"}
	if err := store.FinishExecutorAction(context.Background(), node, result); err == nil {
		t.Fatal("policy rejection retried indefinitely")
	}
}

func TestExecutorLeaseAfterWindowExpiryQueriesPreviouslyDispatchedJob(t *testing.T) {
	store, node, _, report, config := managedFixture(t)
	applyManagedConfig(t, store, node, &report, config)
	original := managedJob(report)
	original.ExpiresAt = time.Now().Add(2 * time.Second).UTC().Format(time.RFC3339Nano)
	createManaged(t, store, node, original)
	first := leaseManaged(t, store, node, report.Hello)
	end, _ := ipsentinel.ParseContractTime(original.ExpiresAt)
	now := end.Add(time.Second)
	priorClock := nowFunc
	nowFunc = func() time.Time { return now }
	defer func() { nowFunc = priorClock }()
	if _, err := store.db.Exec("UPDATE executor_actions SET lease_until=0 WHERE id=?", first.ID); err != nil {
		t.Fatal(err)
	}
	hello := report.Hello
	hello.Status.Control.Paused = true
	reason := "operator"
	hello.Status.Control.PauseReason = &reason
	hello.Status.Control.PauseGeneration++
	second := leaseManaged(t, store, node, hello)
	if second.ID != first.ID || second.Job() != first.Job() || second.RequestID != first.RequestID || second.Attempt != 2 {
		t.Fatal("expired dispatched job lost reconciliation lease", first, second)
	}
	finishManaged(t, store, node, second, ipsentinel.ActionResult{ErrorCode: ipsentinel.ErrCodeExpired, Message: "expired"})
	views, err := store.ExecutorActions(context.Background(), node)
	if err != nil || views[0].ID != first.ID || views[0].Status != "rejected" {
		t.Fatal("expiry became a successful admission", views, err)
	}
}

func TestExecutorConfigValidationFalseNeverAuthorizesApply(t *testing.T) {
	store, node, _, report, config := managedFixture(t)
	digest, err := ipsentinel.ConfigSHA256(config)
	if err != nil {
		t.Fatal(err)
	}
	createManaged(t, store, node, ipsentinel.ExecutorAction{Kind: ipsentinel.MethodConfigValidate, Config: &config})
	validation := leaseManaged(t, store, node, report.Hello)
	finishManaged(t, store, node, validation, ipsentinel.ActionResult{Validation: &ipsentinel.Validation{Valid: false, ConfigSHA256: digest, Issues: []ipsentinel.ValidationIssue{{Pointer: "/services/0", Code: "policy_denied", Message: "fixture"}}}})
	if err := store.CreateExecutorAction(context.Background(), node, ipsentinel.ExecutorAction{ID: ipsentinel.NewUUID(), Kind: ipsentinel.MethodConfigApply, Config: &config}); !errors.Is(err, model.ErrConflict) {
		t.Fatal("config.validate=false authorized config.apply", err)
	}
}

func TestExecutorPauseCancelAndRevocationRaces(t *testing.T) {
	store, node, binding, report, config := managedFixture(t)
	applyManagedConfig(t, store, node, &report, config)
	job := createManaged(t, store, node, managedJob(report))
	cancel := createManaged(t, store, node, ipsentinel.ExecutorAction{Kind: ipsentinel.MethodRunCancel, JobID: job.JobID})
	views, err := store.ExecutorActions(context.Background(), node)
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range views {
		if view.ID == job.ID && view.Status != "cancelled" {
			t.Fatal("never leased job reached RPC")
		}
		if view.ID == cancel.ID && view.Status != "completed" {
			t.Fatal("local cancellation not confirmed")
		}
	}
	createManaged(t, store, node, managedJob(report))
	pause := createManaged(t, store, node, ipsentinel.ExecutorAction{Kind: ipsentinel.MethodControlPause})
	leased := leaseManaged(t, store, node, report.Hello)
	if leased.ID != pause.ID {
		t.Fatal("pause did not preempt new work", leased)
	}
	if err := store.CreateExecutorAction(context.Background(), node, ipsentinel.ExecutorAction{ID: ipsentinel.NewUUID(), Kind: ipsentinel.MethodRunSubmit, JobID: ipsentinel.NewUUID()}); err == nil {
		t.Fatal("invalid job accepted")
	}
	binding.Epoch++
	if err := store.BindExecutor(context.Background(), node, binding); err != nil {
		t.Fatal(err)
	}
	result := ipsentinel.ActionResult{ID: leased.ID, LeaseID: leased.LeaseID, Accepted: true, Control: &report.Hello.Status.Control}
	if err := store.FinishExecutorAction(context.Background(), node, result); !errors.Is(err, model.ErrConflict) {
		t.Fatal("revoked lease wrote receipt", err)
	}
}

func TestExecutorConcurrentLeasesAndLogicalJobDedup(t *testing.T) {
	store, node, _, report, config := managedFixture(t)
	applyManagedConfig(t, store, node, &report, config)
	job := createManaged(t, store, node, managedJob(report))
	duplicate := job
	duplicate.ID = ipsentinel.NewUUID()
	duplicate.JobID = strings.ToUpper(job.JobID)
	if err := store.CreateExecutorAction(context.Background(), node, duplicate); err != nil {
		t.Fatal("same logical job not idempotent", err)
	}
	duplicate.TimeoutMS++
	if err := store.CreateExecutorAction(context.Background(), node, duplicate); !errors.Is(err, model.ErrConflict) {
		t.Fatal("same job changed payload", err)
	}
	var workers sync.WaitGroup
	leases := make(chan ipsentinel.ExecutorAction, 8)
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			action, err := store.LeaseExecutorAction(context.Background(), node, report.Hello)
			if err != nil {
				t.Error(err)
			}
			if action.ID != "" {
				leases <- action
			}
		}()
	}
	workers.Wait()
	close(leases)
	if len(leases) != 1 {
		t.Fatal("concurrent agents got duplicate live lease", len(leases))
	}
}

func TestExecutorVersion19MigrationPreservesHistoryAndInvalidatesUnfencedCommands(t *testing.T) {
	store := openTest(t)
	node, _ := makeNode(t, store)
	ctx := context.Background()
	if _, err := store.db.Exec(`INSERT INTO executor_actions(node_id,id,kind,payload,status,created_at,updated_at,result) VALUES(?,?,?,'{}','completed',1,1,?)`, node, ipsentinel.NewUUID(), "config.validate", "old private validation failure"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO executor_actions(node_id,id,kind,payload,status,created_at,updated_at) VALUES(?,?,?,'{}','leased',1,1)`, node, ipsentinel.NewUUID(), "control.pause"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("PRAGMA user_version=19"); err != nil {
		t.Fatal(err)
	}
	if err := migrate(store.db); err != nil {
		t.Fatal(err)
	}
	views, err := store.ExecutorActions(ctx, node)
	if err != nil || len(views) != 2 {
		t.Fatal(views, err)
	}
	var preserved, cancelled bool
	for _, view := range views {
		if view.Status == "completed" && view.Result != nil && view.Result.Message == "old private validation failure" {
			preserved = true
		}
		if view.Status == "cancelled" {
			cancelled = true
		}
	}
	if !preserved || !cancelled {
		t.Fatal("migration lost audit or retained unsafe command", views)
	}
}
