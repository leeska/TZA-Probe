package storage

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	runtimepkg "github.com/leeska/TZA-Probe/internal/executorruntime"
	"github.com/leeska/TZA-Probe/internal/ipsentinel"
)

func completeRuntimeStage(t *testing.T, store *Store, node string, enrollment runtimepkg.Enrollment) runtimepkg.ChannelRequest {
	t.Helper()
	ctx := t.Context()
	if _, err := store.RuntimeExchange(ctx, node, runtimepkg.ChannelReport{Enrollment: enrollment}); err != nil {
		t.Fatal(err)
	}
	request, err := store.CreateRuntimeRequest(ctx, node, runtimepkg.ChannelInput{Action: "stage", Version: "0.4.0"})
	if err != nil {
		t.Fatal(err)
	}
	plan := stageResult(t, request, false)
	if _, err := store.RuntimeExchange(ctx, node, runtimepkg.ChannelReport{Enrollment: enrollment, Result: &plan}); err != nil {
		t.Fatal(err)
	}
	if err := store.ApproveRuntimeRequest(ctx, node, request.ID, runtimepkg.ChannelApproval{Approved: true, PlanSHA256: plan.PlanSHA256}); err != nil {
		t.Fatal(err)
	}
	started := runtimepkg.ChannelResult{RequestID: request.ID, State: "running", PlanSHA256: plan.PlanSHA256}
	if _, err := store.RuntimeExchange(ctx, node, runtimepkg.ChannelReport{Enrollment: enrollment, Result: &started}); err != nil {
		t.Fatal(err)
	}
	done := stageResult(t, request, true)
	if _, err := store.RuntimeExchange(ctx, node, runtimepkg.ChannelReport{Enrollment: enrollment, Result: &done}); err != nil {
		t.Fatal(err)
	}
	request.State, request.PlanSHA256 = "staged", plan.PlanSHA256
	return request
}

func maintenanceReview(t *testing.T, request runtimepkg.ChannelRequest, hello ipsentinel.Hello) runtimepkg.LifecycleReview {
	t.Helper()
	digest := strings.Repeat("b", 64)
	operation := runtimepkg.OperationPlan{
		FormatVersion: 1, Options: runtimepkg.OperationOptions{Action: request.Action, InstallRoot: "/opt/ipsentinel-runtime", ReferencePath: runtimepkg.DefaultReferenceInstaller,
			StateDir: "/var/lib/ipsentinel/managed", ConfigPath: "/etc/ipsentinel/runtime.toml", PolicyPath: "/etc/ipsentinel/policy.toml", Unit: "ipsentinel-runtime.service"},
		Target: request.Enrollment.Target, StagePlanSHA256: request.StagePlanSHA256, ArchiveSHA256: digest, Version: request.Version,
		InstallerSHA256: runtimepkg.ReferenceInstallerSHA256, SigningKeyID: "test", SigningKeySHA256: digest,
		ExpectedCurrent: "releases/" + digest, ConfigSHA256: digest, PolicySHA256: digest,
	}
	plan := runtimepkg.LifecyclePlan{Operation: operation, Peer: runtimepkg.RuntimePeer{SocketPath: "/run/ipsentinel/control.sock", UID: 1001, GID: 1001},
		Before: runtimepkg.RuntimeIdentity{InstanceID: hello.Status.ExecutorInstanceID, Epoch: hello.Status.Control.Epoch,
			DescriptorSHA256: hello.DescriptorSHA256, RuntimeVersion: hello.RuntimeVersion, DatabaseVersion: hello.DatabaseVersion, Config: *hello.Status.Config}, UnitSHA256: digest}
	if request.Action == "activate" {
		plan.CheckpointHash = digest
		plan.Journal = &ipsentinel.JournalStatus{EarliestSeq: 1, LatestSeq: 10, AckedSeq: 10}
	}
	hash, err := ipsentinel.CanonicalSHA256(plan)
	if err != nil {
		t.Fatal(err)
	}
	return runtimepkg.LifecycleReview{Plan: plan, PlanSHA256: hash,
		ApprovalTemplate: runtimepkg.OperationApproval{FormatVersion: 1, Action: request.Action, PlanSHA256: hash}}
}

func completeLifecycleChannel(t *testing.T, store *Store, node string, request runtimepkg.ChannelRequest, before, after ipsentinel.Hello, state string) {
	t.Helper()
	ctx := t.Context()
	review := maintenanceReview(t, request, before)
	plan := runtimepkg.ChannelResult{RequestID: request.ID, State: "awaiting_approval", PlanSHA256: review.PlanSHA256, Lifecycle: &review}
	if _, err := store.RuntimeExchange(ctx, node, runtimepkg.ChannelReport{Enrollment: request.Enrollment, Result: &plan}); err != nil {
		t.Fatal(err)
	}
	if err := store.ApproveRuntimeRequest(ctx, node, request.ID, runtimepkg.ChannelApproval{Approved: true, PlanSHA256: review.PlanSHA256}); err != nil {
		t.Fatal(err)
	}
	started := runtimepkg.ChannelResult{RequestID: request.ID, State: "running", PlanSHA256: review.PlanSHA256}
	if _, err := store.RuntimeExchange(ctx, node, runtimepkg.ChannelReport{Enrollment: request.Enrollment, Result: &started}); err != nil {
		t.Fatal(err)
	}
	result := runtimepkg.ChannelResult{RequestID: request.ID, State: state, PlanSHA256: review.PlanSHA256,
		LifecycleReceipt: &runtimepkg.LifecycleReceipt{FormatVersion: 1, State: state, PlanSHA256: review.PlanSHA256, Plan: review.Plan, Hello: after}}
	for range 2 {
		if _, err := store.RuntimeExchange(ctx, node, runtimepkg.ChannelReport{Enrollment: request.Enrollment, Result: &result}); err != nil {
			raw, _ := ipsentinel.CanonicalJSON(after)
			t.Fatal("lifecycle receipt was not replay safe", err, ipsentinel.ValidateDefinition("hello", raw, nil))
		}
	}
}

func TestRuntimeMaintenancePersistsUntilVerifiedEpochIsExplicitlyBound(t *testing.T) {
	store, node, binding, report, config := managedFixture(t)
	applyManagedConfig(t, store, node, &report, config)
	ctx := context.Background()
	enrollment := runtimeEnrollment()
	enrollment.Target = binding.Target
	source := completeRuntimeStage(t, store, node, enrollment)
	job := createManaged(t, store, node, managedJob(report))
	drain, err := store.CreateRuntimeRequest(ctx, node, runtimepkg.ChannelInput{Action: "drain", Version: source.Version, StageRequestID: source.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{ipsentinel.MethodRunSubmit, ipsentinel.MethodControlResume, ipsentinel.MethodConfigApply} {
		action := managedJob(report)
		action.ID, action.Kind, action.Config = ipsentinel.NewUUID(), kind, &config
		if err := store.CreateExecutorAction(ctx, node, action); err == nil {
			t.Fatal("maintenance allowed", kind)
		}
	}
	var status string
	if err := store.db.QueryRow("SELECT status FROM executor_actions WHERE id=?", job.ID).Scan(&status); err != nil || status != "cancelled" {
		t.Fatal("maintenance left an undispatched job eligible", status, err)
	}
	if err := store.CancelRuntimeRequest(ctx, node, drain.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.BindExecutor(ctx, node, binding); err == nil {
		t.Fatal("cancelling a drain released maintenance")
	}
	drain, err = store.CreateRuntimeRequest(ctx, node, runtimepkg.ChannelInput{Action: "drain", Version: source.Version, StageRequestID: source.ID})
	if err != nil {
		t.Fatal(err)
	}
	before := report.Hello
	before.Status.Journal = ipsentinel.JournalStatus{EarliestSeq: 1, LatestSeq: 10, AckedSeq: 10}
	before.Status.Control.Paused = true
	before.Status.Control.PauseGeneration++
	reason := "operator"
	before.Status.Control.PauseReason = &reason
	before.Status.Control.ActiveRunID = nil
	before.Status.Control.Draining = false
	completeLifecycleChannel(t, store, node, drain, before, before, "drained")
	startup, err := store.CreateRuntimeRequest(ctx, node, runtimepkg.ChannelInput{Action: "activate", Version: source.Version, StageRequestID: source.ID})
	if err != nil {
		t.Fatal(err)
	}
	after := before
	after.RuntimeVersion = startup.Version
	after.DescriptorSHA256 = strings.Repeat("e", 64)
	after.Status.Control.Epoch++
	after.Status.ObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
	after.Status.Health = "degraded"
	after.Features = []string{"run", "journal", "pause", "config", "cancel", "assessment"}
	after.LocalPolicy.HardLimitsAvailable = true
	completeLifecycleChannel(t, store, node, startup, before, after, "started_paused")
	if destination := os.Getenv("TZA_RUNTIME_UI_DATABASE"); destination != "" {
		backup, cleanup, err := store.Backup(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		raw, err := os.ReadFile(backup)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(destination, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.BindExecutor(ctx, node, binding); err == nil {
		t.Fatal("old epoch was accepted after startup")
	}
	newBinding := binding
	newBinding.Epoch, newBinding.DescriptorSHA256 = after.Status.Control.Epoch, after.DescriptorSHA256
	changed, err := store.CreateRuntimeRequest(ctx, node, runtimepkg.ChannelInput{Action: "select", Version: source.Version, StageRequestID: source.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BindExecutor(ctx, node, newBinding); err == nil {
		t.Fatal("an older startup receipt bypassed a newer runtime operation")
	}
	if err := store.CancelRuntimeRequest(ctx, node, changed.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.BindExecutor(ctx, node, newBinding); err != nil {
		t.Fatal(err)
	}
	var maintenance, paused bool
	if err := store.db.QueryRow("SELECT EXISTS(SELECT 1 FROM runtime_maintenance WHERE node_id=?)", node).Scan(&maintenance); err != nil || maintenance {
		t.Fatal("explicit verified binding did not acknowledge maintenance", maintenance, err)
	}
	if err := store.db.QueryRow("SELECT dispatch_paused FROM executor_bindings WHERE node_id=?", node).Scan(&paused); err != nil || !paused {
		t.Fatal("binding automatically resumed dispatch", paused, err)
	}
}
