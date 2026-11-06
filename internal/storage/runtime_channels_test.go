package storage

import (
	"context"
	"strings"
	"testing"
	"time"

	runtimepkg "github.com/leeska/TZA-Probe/internal/executorruntime"
	"github.com/leeska/TZA-Probe/internal/ipsentinel"
)

func runtimeEnrollment() runtimepkg.Enrollment {
	return runtimepkg.Enrollment{
		ScopeSHA256: string(bytes64('a')),
		Target:      ipsentinel.Target{ControllerID: ipsentinel.NewUUID(), NodeID: ipsentinel.NewUUID()},
		Host:        runtimepkg.Host{Platform: "linux-amd64", ProbeVersion: "1.9.2", AgentVersion: "1.9.2"},
	}
}

func bytes64(value byte) []byte {
	return []byte(strings.Repeat(string(value), 64))
}

func stageResult(t *testing.T, request runtimepkg.ChannelRequest, approved bool) runtimepkg.ChannelResult {
	t.Helper()
	plan := runtimepkg.Plan{
		FormatVersion: 1, Target: request.Enrollment.Target, Package: "ipsentinel-runtime", Version: request.Version,
		ArchiveSHA256: string(bytes64('b')), DescriptorSHA256: string(bytes64('c')), SigningKeyID: "test",
		Host: request.Enrollment.Host, Permissions: []string{"network.public_https", "browser.execute"},
		Artifacts: []runtimepkg.Artifact{{ID: "runtime", Kind: "runtime", Version: request.Version, SHA256: string(bytes64('d')), SizeBytes: 1024}}, TotalBytes: 1024,
	}
	digest, err := ipsentinel.CanonicalSHA256(plan)
	if err != nil {
		t.Fatal(err)
	}
	review := runtimepkg.Review{Plan: plan, PlanSHA256: digest, ApprovalTemplate: runtimepkg.Approval{FormatVersion: 1, PlanSHA256: digest}}
	if !approved {
		return runtimepkg.ChannelResult{RequestID: request.ID, State: "awaiting_approval", PlanSHA256: digest, Review: &review}
	}
	return runtimepkg.ChannelResult{RequestID: request.ID, State: "staged", PlanSHA256: digest, Receipt: &runtimepkg.Receipt{FormatVersion: 1, State: "staged", PlanSHA256: digest, Plan: plan, StagedAt: time.Now().UTC()}}
}

func TestRuntimeChannelApprovalAndDurableReplay(t *testing.T) {
	store := openTest(t)
	node, _ := makeNode(t, store)
	ctx := context.Background()
	enrollment := runtimeEnrollment()
	if _, err := store.CreateRuntimeRequest(ctx, node, runtimepkg.ChannelInput{Action: "stage", Version: "0.4.0"}); err == nil {
		t.Fatal("request preceded a node heartbeat")
	}
	if _, err := store.RuntimeExchange(ctx, node, runtimepkg.ChannelReport{Enrollment: enrollment}); err != nil {
		t.Fatal(err)
	}
	request, err := store.CreateRuntimeRequest(ctx, node, runtimepkg.ChannelInput{Action: "stage", Version: "0.4.0"})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := store.RuntimeExchange(ctx, node, runtimepkg.ChannelReport{Enrollment: enrollment})
	if err != nil || reply.Request == nil || reply.Request.ID != request.ID {
		t.Fatal(reply, err)
	}
	planned := stageResult(t, request, false)
	for range 2 {
		reply, err = store.RuntimeExchange(ctx, node, runtimepkg.ChannelReport{Enrollment: enrollment, Result: &planned})
		if err != nil || reply.Request != nil {
			t.Fatal("duplicate plan changed the approval gate", reply, err)
		}
	}
	if err := store.ApproveRuntimeRequest(ctx, node, request.ID, runtimepkg.ChannelApproval{Approved: true, PlanSHA256: planned.PlanSHA256}); err != nil {
		t.Fatal(err)
	}
	reply, err = store.RuntimeExchange(ctx, node, runtimepkg.ChannelReport{Enrollment: enrollment})
	if err != nil || reply.Request == nil || reply.Request.State != "approved" || reply.Request.Review == nil {
		t.Fatal(reply, err)
	}
	running := runtimepkg.ChannelResult{RequestID: request.ID, State: "running", PlanSHA256: planned.PlanSHA256}
	if _, err := store.RuntimeExchange(ctx, node, runtimepkg.ChannelReport{Enrollment: enrollment, Result: &running}); err != nil {
		t.Fatal(err)
	}
	if err := store.CancelRuntimeRequest(ctx, node, request.ID); err == nil {
		t.Fatal("running operation was remotely cancelled")
	}
	receipt := stageResult(t, request, true)
	for range 2 {
		reply, err = store.RuntimeExchange(ctx, node, runtimepkg.ChannelReport{Enrollment: enrollment, Result: &receipt})
		if err != nil || reply.Request != nil {
			t.Fatal("durable staging receipt was not replay safe", reply, err)
		}
	}
	install, err := store.CreateRuntimeRequest(ctx, node, runtimepkg.ChannelInput{Action: "install", Version: "0.4.0", StageRequestID: request.ID})
	if err != nil || install.StagePlanSHA256 != planned.PlanSHA256 {
		t.Fatal(install, err)
	}
	operation := runtimepkg.OperationPlan{
		FormatVersion: 1, Options: runtimepkg.OperationOptions{Action: "install", InstallRoot: "/opt/runtime", ReferencePath: "/opt/helper"},
		Target: enrollment.Target, StagePlanSHA256: planned.PlanSHA256, ArchiveSHA256: string(bytes64('b')), Version: "0.4.0",
		InstallerSHA256: runtimepkg.ReferenceInstallerSHA256, SigningKeySHA256: string(bytes64('e')),
	}
	digest, _ := ipsentinel.CanonicalSHA256(operation)
	review := runtimepkg.OperationReview{Plan: operation, PlanSHA256: digest, ApprovalTemplate: runtimepkg.OperationApproval{FormatVersion: 1, Action: "install", PlanSHA256: digest}}
	planReport := runtimepkg.ChannelResult{RequestID: install.ID, State: "awaiting_approval", PlanSHA256: digest, Operation: &review}
	if _, err := store.RuntimeExchange(ctx, node, runtimepkg.ChannelReport{Enrollment: enrollment, Result: &planReport}); err != nil {
		t.Fatal(err)
	}
	views, err := store.RuntimeChannels(ctx)
	if err != nil || len(views) != 1 || len(views[0].Requests) != 2 || views[0].Requests[0].Operation == nil {
		t.Fatal(views, err)
	}
}

func TestRuntimeChannelRejectsUnapprovedOrChangedResults(t *testing.T) {
	store := openTest(t)
	node, _ := makeNode(t, store)
	ctx := context.Background()
	enrollment := runtimeEnrollment()
	if _, err := store.RuntimeExchange(ctx, node, runtimepkg.ChannelReport{Enrollment: enrollment}); err != nil {
		t.Fatal(err)
	}
	request, err := store.CreateRuntimeRequest(ctx, node, runtimepkg.ChannelInput{Action: "stage", Version: "0.4.0"})
	if err != nil {
		t.Fatal(err)
	}
	receipt := stageResult(t, request, true)
	if _, err := store.RuntimeExchange(ctx, node, runtimepkg.ChannelReport{Enrollment: enrollment, Result: &receipt}); err == nil {
		t.Fatal("terminal receipt bypassed approval")
	}
	plan := stageResult(t, request, false)
	plan.PlanSHA256 = string(bytes64('f'))
	if _, err := store.RuntimeExchange(ctx, node, runtimepkg.ChannelReport{Enrollment: enrollment, Result: &plan}); err == nil {
		t.Fatal("incorrect plan digest accepted")
	}
	plan = stageResult(t, request, false)
	plan.Review.Plan.Target.NodeID = ipsentinel.NewUUID()
	if _, err := store.RuntimeExchange(ctx, node, runtimepkg.ChannelReport{Enrollment: enrollment, Result: &plan}); err == nil {
		t.Fatal("review for another target accepted")
	}
	changed := enrollment
	changed.ScopeSHA256 = string(bytes64('0'))
	reply, err := store.RuntimeExchange(ctx, node, runtimepkg.ChannelReport{Enrollment: changed})
	if err != nil || reply.Request != nil {
		t.Fatal(reply, err)
	}
	if _, err := store.RuntimeExchange(ctx, node, runtimepkg.ChannelReport{Enrollment: changed, Result: &receipt}); err == nil {
		t.Fatal("old scope receipt accepted after configuration change")
	}
	views, _ := store.RuntimeChannels(ctx)
	if len(views) != 1 || views[0].Requests[0].State != "cancelled" || views[0].Requests[0].ErrorCode != "node_configuration_changed" {
		t.Fatal(views)
	}
}

func TestRuntimeChannelRejectsExpiredApprovalAndActivatedReceipt(t *testing.T) {
	store := openTest(t)
	node, _ := makeNode(t, store)
	ctx := t.Context()
	enrollment := runtimeEnrollment()
	if _, err := store.RuntimeExchange(ctx, node, runtimepkg.ChannelReport{Enrollment: enrollment}); err != nil {
		t.Fatal(err)
	}
	request, err := store.CreateRuntimeRequest(ctx, node, runtimepkg.ChannelInput{Action: "stage", Version: "0.4.0"})
	if err != nil {
		t.Fatal(err)
	}
	planned := stageResult(t, request, false)
	if _, err := store.RuntimeExchange(ctx, node, runtimepkg.ChannelReport{Enrollment: enrollment, Result: &planned}); err != nil {
		t.Fatal(err)
	}
	if err := store.ApproveRuntimeRequest(ctx, node, request.ID, runtimepkg.ChannelApproval{Approved: true, PlanSHA256: strings.Repeat("0", 64)}); err == nil {
		t.Fatal("approval accepted another plan digest")
	}
	if err := store.ApproveRuntimeRequest(ctx, node, request.ID, runtimepkg.ChannelApproval{Approved: true, PlanSHA256: planned.PlanSHA256}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE runtime_requests SET payload=json_set(payload,'$.approved_at',?) WHERE id=?`, time.Now().Add(-16*time.Minute).Unix(), request.ID); err != nil {
		t.Fatal(err)
	}
	running := runtimepkg.ChannelResult{RequestID: request.ID, PlanSHA256: planned.PlanSHA256, State: "running"}
	if _, err := store.RuntimeExchange(ctx, node, runtimepkg.ChannelReport{Enrollment: enrollment, Result: &running}); err == nil {
		t.Fatal("expired approval permitted execution")
	}
	reply, err := store.RuntimeExchange(ctx, node, runtimepkg.ChannelReport{Enrollment: enrollment})
	if err != nil || reply.Request != nil {
		t.Fatal(reply, err)
	}
	views, err := store.RuntimeChannels(ctx)
	if err != nil || views[0].Requests[0].State != "expired" {
		t.Fatal(views, err)
	}
	request, err = store.CreateRuntimeRequest(ctx, node, runtimepkg.ChannelInput{Action: "stage", Version: "0.4.0"})
	if err != nil {
		t.Fatal(err)
	}
	planned = stageResult(t, request, false)
	if _, err := store.RuntimeExchange(ctx, node, runtimepkg.ChannelReport{Enrollment: enrollment, Result: &planned}); err != nil {
		t.Fatal(err)
	}
	if err := store.ApproveRuntimeRequest(ctx, node, request.ID, runtimepkg.ChannelApproval{Approved: true, PlanSHA256: planned.PlanSHA256}); err != nil {
		t.Fatal(err)
	}
	running.RequestID, running.PlanSHA256 = request.ID, planned.PlanSHA256
	if _, err := store.RuntimeExchange(ctx, node, runtimepkg.ChannelReport{Enrollment: enrollment, Result: &running}); err != nil {
		t.Fatal(err)
	}
	receipt := stageResult(t, request, true)
	receipt.Receipt.Activated = true
	if _, err := store.RuntimeExchange(ctx, node, runtimepkg.ChannelReport{Enrollment: enrollment, Result: &receipt}); err == nil {
		t.Fatal("staging receipt claimed runtime activation")
	}
	receipt.Receipt.Activated = false
	if _, err := store.RuntimeExchange(ctx, node, runtimepkg.ChannelReport{Enrollment: enrollment, Result: &receipt}); err != nil {
		t.Fatal(err)
	}
	receipt.Receipt.StagedAt = receipt.Receipt.StagedAt.Add(time.Second)
	if _, err := store.RuntimeExchange(ctx, node, runtimepkg.ChannelReport{Enrollment: enrollment, Result: &receipt}); err == nil {
		t.Fatal("terminal installation receipt was rewritten")
	}
}

func TestRuntimeChannelVersion21UpgradePreservesNodes(t *testing.T) {
	store := openTest(t)
	node, _ := makeNode(t, store)
	if _, err := store.db.Exec("DROP TABLE runtime_requests; DROP TABLE runtime_channels; PRAGMA user_version=21;"); err != nil {
		t.Fatal(err)
	}
	if err := migrate(store.db); err != nil {
		t.Fatal(err)
	}
	var version, nodes int
	if err := store.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 24 {
		t.Fatal(version, err)
	}
	if err := store.db.QueryRow("SELECT count(*) FROM nodes WHERE id=?", node).Scan(&nodes); err != nil || nodes != 1 {
		t.Fatal("schema upgrade lost the existing node", nodes, err)
	}
	views, err := store.RuntimeChannels(t.Context())
	if err != nil || len(views) != 0 {
		t.Fatal("schema upgrade authorized an installation", views, err)
	}
}

func TestRuntimeReconciliationCannotResolveOtherScopeOrUnstartedRequest(t *testing.T) {
	store := openTest(t)
	node, _ := makeNode(t, store)
	enrollment := runtimeEnrollment()
	if _, err := store.RuntimeExchange(t.Context(), node, runtimepkg.ChannelReport{Enrollment: enrollment}); err != nil {
		t.Fatal(err)
	}
	request, err := store.CreateRuntimeRequest(t.Context(), node, runtimepkg.ChannelInput{Action: "stage", Version: "0.4.0"})
	if err != nil {
		t.Fatal(err)
	}
	planned := stageResult(t, request, false)
	if _, err := store.RuntimeExchange(t.Context(), node, runtimepkg.ChannelReport{Enrollment: enrollment, Result: &planned}); err != nil {
		t.Fatal(err)
	}
	request.PlanSHA256 = planned.PlanSHA256
	failed := runtimepkg.ChannelResult{RequestID: request.ID, State: "failed", PlanSHA256: planned.PlanSHA256, ErrorCode: "operation_not_applied"}
	if _, err := store.RuntimeExchange(t.Context(), node, runtimepkg.ChannelReport{Enrollment: enrollment, Result: &failed}); err == nil {
		t.Fatal("inspection result bypassed execution approval")
	}
	if err := store.ApproveRuntimeRequest(t.Context(), node, request.ID, runtimepkg.ChannelApproval{Approved: true, PlanSHA256: planned.PlanSHA256}); err != nil {
		t.Fatal(err)
	}
	running := runtimepkg.ChannelResult{RequestID: request.ID, State: "running", PlanSHA256: planned.PlanSHA256}
	if _, err := store.RuntimeExchange(t.Context(), node, runtimepkg.ChannelReport{Enrollment: enrollment, Result: &running}); err != nil {
		t.Fatal(err)
	}
	unknown := running
	unknown.State, unknown.ErrorCode = "unknown", "operation_interrupted"
	if _, err := store.RuntimeExchange(t.Context(), node, runtimepkg.ChannelReport{Enrollment: enrollment, Result: &unknown}); err != nil {
		t.Fatal(err)
	}
	changed := enrollment
	changed.ScopeSHA256 = strings.Repeat("b", 64)
	reply, err := store.RuntimeExchange(t.Context(), node, runtimepkg.ChannelReport{Enrollment: changed, Reconcile: true})
	if err != nil || reply.Check != nil || reply.Request != nil {
		t.Fatal("changed scope received an old operation", reply, err)
	}
	if _, err := store.RuntimeExchange(t.Context(), node, runtimepkg.ChannelReport{Enrollment: changed, Result: &failed, Reconcile: true}); err == nil {
		t.Fatal("another local identity resolved the old request")
	}
	if _, err := store.CreateRuntimeRequest(t.Context(), node, runtimepkg.ChannelInput{Action: "stage", Version: "0.4.0"}); err == nil {
		t.Fatal("scope change hid an unresolved operation")
	}
}
