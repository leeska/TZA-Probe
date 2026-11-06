package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	runtimepkg "github.com/leeska/TZA-Probe/internal/executorruntime"
	"github.com/leeska/TZA-Probe/internal/ipsentinel"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/storage"
)

type runtimeTransportStub struct {
	store     *storage.Store
	node      string
	dropState string
}

func (transport *runtimeTransportStub) RuntimeExchange(ctx context.Context, report runtimepkg.ChannelReport) (runtimepkg.ChannelReply, error) {
	reply, err := transport.store.RuntimeExchange(ctx, transport.node, report)
	if err == nil && report.Result != nil && report.Result.State == transport.dropState {
		transport.dropState = ""
		return runtimepkg.ChannelReply{}, errors.New("deliberately lost committed runtime response")
	}
	return reply, err
}

type runtimeWorkerStub struct {
	plans       int
	executions  int
	changed     bool
	beforeApply func()
	duringPlan  func()
}

func (worker *runtimeWorkerStub) Run(_ context.Context, request runtimepkg.ChannelRequest) (runtimepkg.ChannelResult, error) {
	result := runtimepkg.ChannelResult{RequestID: request.ID}
	plan := runtimepkg.Plan{
		FormatVersion: 1, Target: request.Enrollment.Target, Package: "ipsentinel-runtime", Version: request.Version,
		ArchiveSHA256: strings.Repeat("a", 64), DescriptorSHA256: strings.Repeat("b", 64),
		SigningKeyID: "test", Host: request.Enrollment.Host, Permissions: []string{"network.public_https", "browser.execute"},
		Artifacts:  []runtimepkg.Artifact{{ID: "runtime", Kind: "runtime", Version: request.Version, SHA256: strings.Repeat("c", 64), SizeBytes: 1024}},
		TotalBytes: 1024,
	}
	if worker.changed && worker.plans > 0 {
		plan.ArchiveSHA256 = strings.Repeat("d", 64)
	}
	digest, _ := ipsentinel.CanonicalSHA256(plan)
	if request.State == "requested" {
		worker.plans++
		if worker.duringPlan != nil {
			worker.duringPlan()
		}
		result.State, result.PlanSHA256 = "awaiting_approval", digest
		result.Review = &runtimepkg.Review{Plan: plan, PlanSHA256: digest, ApprovalTemplate: runtimepkg.Approval{FormatVersion: 1, PlanSHA256: digest}}
		return result, nil
	}
	worker.executions++
	if worker.beforeApply != nil {
		worker.beforeApply()
	}
	result.State, result.PlanSHA256 = "staged", digest
	result.Receipt = &runtimepkg.Receipt{FormatVersion: 1, State: "staged", Plan: plan, PlanSHA256: digest, StagedAt: time.Now().UTC()}
	return result, nil
}

func runtimeInstallerFixture(t *testing.T) (RuntimeInstaller, *runtimeWorkerStub, *runtimeTransportStub) {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	node, _, err := store.SaveNode(t.Context(), model.Node{Name: "runtime acceptance", Billing: model.Billing{ResetAt: "2026-11-01T00:00:00Z", TrafficMode: "sum"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	enrollment := runtimepkg.Enrollment{
		ScopeSHA256: strings.Repeat("f", 64), Target: ipsentinel.Target{ControllerID: ipsentinel.NewUUID(), NodeID: ipsentinel.NewUUID()},
		Host: runtimepkg.Host{Platform: "linux-amd64", ProbeVersion: "1.9.2", AgentVersion: "1.9.2"},
	}
	transport := &runtimeTransportStub{store: store, node: node}
	worker := &runtimeWorkerStub{}
	journal := FileRuntimePending{Directory: filepath.Join(t.TempDir(), "journal")}
	installer := RuntimeInstaller{Transport: transport, Worker: worker, Enrollment: enrollment, Journal: journal}
	if err := installer.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	return installer, worker, transport
}

func runtimeRequestState(t *testing.T, transport *runtimeTransportStub) runtimepkg.ChannelRequest {
	t.Helper()
	views, err := transport.store.RuntimeChannels(t.Context())
	if err != nil || len(views) != 1 || len(views[0].Requests) != 1 {
		t.Fatal(views, err)
	}
	return views[0].Requests[0]
}

func approveRuntimeFixture(t *testing.T, installer RuntimeInstaller, transport *runtimeTransportStub) runtimepkg.ChannelRequest {
	t.Helper()
	if _, err := transport.store.CreateRuntimeRequest(t.Context(), transport.node, runtimepkg.ChannelInput{Action: "stage", Version: "0.4.0"}); err != nil {
		t.Fatal(err)
	}
	if err := installer.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	request := runtimeRequestState(t, transport)
	if request.State != "awaiting_approval" {
		t.Fatal(request)
	}
	if err := transport.store.ApproveRuntimeRequest(t.Context(), transport.node, request.ID, runtimepkg.ChannelApproval{Approved: true, PlanSHA256: request.PlanSHA256}); err != nil {
		t.Fatal(err)
	}
	return request
}

func TestRuntimeInstallerApprovalAndLostReceipt(t *testing.T) {
	for _, lost := range []string{"none", "awaiting_approval", "staged"} {
		t.Run(lost, func(t *testing.T) {
			installer, worker, transport := runtimeInstallerFixture(t)
			if _, err := transport.store.CreateRuntimeRequest(t.Context(), transport.node, runtimepkg.ChannelInput{Action: "stage", Version: "0.4.0"}); err != nil {
				t.Fatal(err)
			}
			transport.dropState = lost
			err := installer.Sync(t.Context())
			if lost == "awaiting_approval" && err == nil || lost != "awaiting_approval" && err != nil {
				t.Fatal("unexpected plan receipt outcome", err)
			}
			if worker.executions != 0 {
				t.Fatal("unapproved runtime operation executed")
			}
			planned := runtimeRequestState(t, transport)
			if planned.State != "awaiting_approval" {
				t.Fatal(planned)
			}
			if err := transport.store.ApproveRuntimeRequest(t.Context(), transport.node, planned.ID, runtimepkg.ChannelApproval{Approved: true, PlanSHA256: planned.PlanSHA256}); err != nil {
				t.Fatal(err)
			}
			err = installer.Sync(t.Context())
			if lost == "staged" && err == nil || lost != "staged" && err != nil {
				t.Fatal("unexpected execution receipt outcome", err)
			}
			if worker.executions != 1 || runtimeRequestState(t, transport).State != "staged" {
				t.Fatal("approved operation did not produce its exact terminal receipt")
			}
			if err := installer.Sync(t.Context()); err != nil {
				t.Fatal(err)
			}
			pending, err := installer.Journal.Load()
			if err != nil || pending != nil || worker.executions != 1 {
				t.Fatal("receipt retry executed the operation again", pending, err, worker.executions)
			}
		})
	}
}

func TestRuntimeInstallerLostStartReceiptDoesNotExecute(t *testing.T) {
	installer, worker, transport := runtimeInstallerFixture(t)
	approveRuntimeFixture(t, installer, transport)
	transport.dropState = "running"
	if err := installer.Sync(t.Context()); err == nil || worker.executions != 0 {
		t.Fatal("missing durable start acknowledgment permitted execution", err)
	}
	if err := installer.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if runtimeRequestState(t, transport).State != "unknown" || worker.executions != 0 {
		t.Fatal("uncertain start was resubmitted")
	}
}

func TestRuntimeInstallerCrashMarkerCannotRepeatOperation(t *testing.T) {
	installer, worker, transport := runtimeInstallerFixture(t)
	request := approveRuntimeFixture(t, installer, transport)
	running := runtimepkg.ChannelResult{RequestID: request.ID, PlanSHA256: request.PlanSHA256, State: "running"}
	if _, err := transport.RuntimeExchange(t.Context(), runtimepkg.ChannelReport{Enrollment: installer.Enrollment, Result: &running}); err != nil {
		t.Fatal(err)
	}
	if err := installer.Journal.Save(RuntimePending{Enrollment: installer.Enrollment, Result: running}); err != nil {
		t.Fatal(err)
	}
	if err := installer.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if worker.executions != 0 || runtimeRequestState(t, transport).State != "unknown" {
		t.Fatal("interrupted operation was executed again")
	}
}

func TestRuntimeInstallerRechecksInputsAndCancellation(t *testing.T) {
	for _, change := range []string{"release", "cancel"} {
		t.Run(change, func(t *testing.T) {
			installer, worker, transport := runtimeInstallerFixture(t)
			request := approveRuntimeFixture(t, installer, transport)
			if change == "release" {
				worker.changed = true
			} else {
				worker.duringPlan = func() {
					if err := transport.store.CancelRuntimeRequest(t.Context(), transport.node, request.ID); err != nil {
						t.Fatal(err)
					}
				}
			}
			err := installer.Sync(t.Context())
			if change == "cancel" && err == nil {
				t.Fatal("cancelled operation received permission to execute")
			}
			if worker.executions != 0 {
				t.Fatal("changed or cancelled operation executed")
			}
			current := runtimeRequestState(t, transport)
			if change == "release" && current.State != "failed" || change == "cancel" && current.State != "cancelled" {
				t.Fatal(current)
			}
		})
	}
}

func TestRuntimeJournalDurabilityLocksAndUnsafeFiles(t *testing.T) {
	journal := FileRuntimePending{Directory: filepath.Join(t.TempDir(), "private")}
	identity, err := journal.Identity()
	if err != nil {
		t.Fatal(err)
	}
	again, err := journal.Identity()
	if err != nil || identity != again {
		t.Fatal(identity, again, err)
	}
	unlock, err := journal.Lock()
	if err != nil {
		t.Fatal(err)
	}
	if otherUnlock, err := journal.Lock(); err == nil {
		otherUnlock()
		t.Fatal("concurrent installer obtained the node operation lock")
	}
	unlock()
	installer, _, _ := runtimeInstallerFixture(t)
	pending := RuntimePending{Enrollment: installer.Enrollment, Result: runtimeUnknown(ipsentinel.NewUUID(), strings.Repeat("a", 64))}
	if err := journal.Save(pending); err != nil {
		t.Fatal(err)
	}
	loaded, err := journal.Load()
	if err != nil || loaded == nil || loaded.Result != pending.Result {
		t.Fatal(loaded, err)
	}
	if err := journal.Clear(pending.Result.RequestID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(journal.Directory, "receipts", pending.Result.RequestID+"-unknown.json")); err != nil {
		t.Fatal("acknowledged evidence was not archived", err)
	}
	if err := os.WriteFile(filepath.Join(journal.Directory, "damaged"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("damaged", filepath.Join(journal.Directory, "pending.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Load(); err == nil {
		t.Fatal("symlinked pending journal was accepted")
	}
}
