package storage

import (
	"testing"
	"time"

	runtimepkg "github.com/leeska/TZA-Probe/internal/executorruntime"
	"github.com/leeska/TZA-Probe/internal/ipsentinel"
)

func TestPluginEnableBindsOnlyAfterDurableVerifiedReceipt(t *testing.T) {
	store := openTest(t)
	node, _ := makeNode(t, store)
	enrollment := runtimeEnrollment()
	enrollment.Host.AgentVersion = "1.10.7"
	exchange := func(result *runtimepkg.ChannelResult) {
		t.Helper()
		if _, err := store.RuntimeExchange(t.Context(), node, runtimepkg.ChannelReport{Enrollment: enrollment, Result: result}); err != nil {
			t.Fatal(err)
		}
	}
	exchange(nil)
	stage, err := store.CreateRuntimeRequest(t.Context(), node, runtimepkg.ChannelInput{Action: "stage", Version: "0.4.0"})
	if err != nil {
		t.Fatal(err)
	}
	planned := stageResult(t, stage, false)
	exchange(&planned)
	if err := store.ApproveRuntimeRequest(t.Context(), node, stage.ID, runtimepkg.ChannelApproval{Approved: true, PlanSHA256: planned.PlanSHA256}); err != nil {
		t.Fatal(err)
	}
	exchange(&runtimepkg.ChannelResult{RequestID: stage.ID, State: "running", PlanSHA256: planned.PlanSHA256})
	staged := stageResult(t, stage, true)
	exchange(&staged)
	settings := runtimepkg.EnableSettings{ExpectedIP: "8.8.8.8", Country: "JP"}
	input := runtimepkg.ChannelInput{Action: "enable", Version: stage.Version, StageRequestID: stage.ID, Enable: &settings}
	request, err := store.CreateRuntimeRequest(t.Context(), node, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ExecutorCursor(t.Context(), node); err == nil {
		t.Fatal("a requested enablement created an authorization")
	}
	plan := runtimepkg.OperationPlan{
		FormatVersion: 1, Options: runtimepkg.OperationOptions{Action: "enable", InstallRoot: runtimepkg.DefaultInstallRoot, ReferencePath: runtimepkg.DefaultReferenceInstaller},
		Target: enrollment.Target, StagePlanSHA256: staged.PlanSHA256, ArchiveSHA256: string(bytes64('b')), Version: stage.Version,
		InstallerSHA256: runtimepkg.ReferenceInstallerSHA256, SigningKeyID: "test", SigningKeySHA256: string(bytes64('c')), Enable: &settings,
	}
	digest, _ := ipsentinel.CanonicalSHA256(plan)
	review := runtimepkg.OperationReview{Plan: plan, PlanSHA256: digest, ApprovalTemplate: runtimepkg.OperationApproval{FormatVersion: 1, Action: "enable", PlanSHA256: digest}}
	exchange(&runtimepkg.ChannelResult{RequestID: request.ID, State: "awaiting_approval", PlanSHA256: digest, Operation: &review})
	if err := store.ApproveRuntimeRequest(t.Context(), node, request.ID, runtimepkg.ChannelApproval{Approved: true, PlanSHA256: digest}); err != nil {
		t.Fatal(err)
	}
	exchange(&runtimepkg.ChannelResult{RequestID: request.ID, State: "running", PlanSHA256: digest})
	_, report, _ := executorFixture(t)
	hello := report.Hello
	hello.RuntimeVersion = stage.Version
	hello.Status.ObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
	hello.Status.Control.Mode, hello.Status.Health = ipsentinel.ModeManaged, "ready"
	hello.Status.Control.Paused, hello.Status.Control.Draining = false, false
	hello.Status.Control.ActiveRunID, hello.Status.Control.PauseReason = nil, nil
	hello.Features = []string{"run", "config", "journal", "pause", "assessment"}
	hello.LocalPolicy.HardLimitsAvailable = true
	config := runtimepkg.EnableConfig(settings)
	configDigest, _ := ipsentinel.ConfigSHA256(config)
	hello.Status.Config = &ipsentinel.ConfigRef{Revision: 1, SHA256: configDigest}
	result := runtimepkg.ChannelResult{RequestID: request.ID, State: "enabled", PlanSHA256: digest,
		OperationReceipt: &runtimepkg.OperationReceipt{FormatVersion: 1, State: "enabled", Selected: true, Activated: true, PlanSHA256: digest, Plan: plan, Release: plan.ArchiveSHA256, Hello: &hello}}
	if _, err := store.db.Exec("CREATE TRIGGER fail_enable BEFORE INSERT ON executor_bindings BEGIN SELECT RAISE(ABORT,'injected binding failure'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RuntimeExchange(t.Context(), node, runtimepkg.ChannelReport{Enrollment: enrollment, Result: &result}); err == nil {
		t.Fatal("ignored authorization transaction failure")
	}
	if _, err := store.db.Exec("DROP TRIGGER fail_enable"); err != nil {
		t.Fatal(err)
	}
	exchange(&result)
	exchange(&result)
	cursor, err := store.ExecutorCursor(t.Context(), node)
	if err != nil || cursor.ThroughSeq != 0 || cursor.Binding.Mode != ipsentinel.ModeManaged {
		t.Fatal("startup receipt did not establish exactly one new binding", cursor, err)
	}
	var count int
	if err := store.db.QueryRow("SELECT count(*) FROM executor_configs WHERE node_id=?", node).Scan(&count); err != nil || count != 1 {
		t.Fatal("startup configuration not persisted exactly once", count, err)
	}
	var received int64
	if err := store.db.QueryRow("SELECT received_at FROM executor_bindings WHERE node_id=?", node).Scan(&received); err != nil || received != 0 {
		t.Fatal("startup receipt was presented as a fresh live observation", received, err)
	}
	if _, err := store.CreateRuntimeRequest(t.Context(), node, input); err == nil {
		t.Fatal("a second enablement replaced an existing authorization")
	}
}
