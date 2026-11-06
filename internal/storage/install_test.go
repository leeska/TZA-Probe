package storage

import (
	"errors"
	"testing"
	"time"

	runtimepkg "github.com/leeska/TZA-Probe/internal/executorruntime"
	"github.com/leeska/TZA-Probe/internal/ipsentinel"
	"github.com/leeska/TZA-Probe/internal/model"
)

func setRuntimeDisabled(t *testing.T, store *Store, id string, disabled bool) {
	t.Helper()
	nodes, err := store.Nodes(t.Context(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range nodes {
		if view.ID == id {
			node := view.Node
			node.RuntimeInstallDisabled = disabled
			if _, _, err := store.SaveNode(t.Context(), node, false); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatal("node missing")
}

func TestDisabledRuntimeBlocksDispatchAndApprovalButPreservesReceipts(t *testing.T) {
	store := openTest(t)
	node, _ := makeNode(t, store)
	enrollment := runtimeEnrollment()
	report := runtimepkg.ChannelReport{Enrollment: enrollment}
	if _, err := store.RuntimeExchange(t.Context(), node, report); err != nil {
		t.Fatal(err)
	}
	request, err := store.CreateRuntimeRequest(t.Context(), node, runtimepkg.ChannelInput{Action: "stage", Version: "0.4.0"})
	if err != nil {
		t.Fatal("node should allow plugins by default", err)
	}
	setRuntimeDisabled(t, store, node, true)
	reply, err := store.RuntimeExchange(t.Context(), node, report)
	if err != nil || reply.Request != nil {
		t.Fatal("disabled node received queued request", reply, err)
	}
	planned := stageResult(t, request, false)
	report.Result = &planned
	if _, err := store.RuntimeExchange(t.Context(), node, report); err != nil {
		t.Fatal("late receipt was rejected", err)
	}
	approval := runtimepkg.ChannelApproval{Approved: true, PlanSHA256: planned.PlanSHA256}
	if err := store.ApproveRuntimeRequest(t.Context(), node, request.ID, approval); !errors.Is(err, model.ErrConflict) {
		t.Fatal("disabled node approved queued plan", err)
	}
	setRuntimeDisabled(t, store, node, false)
	if err := store.ApproveRuntimeRequest(t.Context(), node, request.ID, approval); err != nil {
		t.Fatal(err)
	}
	setRuntimeDisabled(t, store, node, true)
	report.Result = nil
	reply, err = store.RuntimeExchange(t.Context(), node, report)
	if err != nil || reply.Request != nil {
		t.Fatal("approved request bypassed disabled switch", reply, err)
	}
	setRuntimeDisabled(t, store, node, false)
	reply, err = store.RuntimeExchange(t.Context(), node, report)
	if err != nil || reply.Request == nil || reply.Request.ID != request.ID {
		t.Fatal("re-enabled node lost pending request", reply, err)
	}
}

func TestDisabledRuntimeBlocksManagedActionCreationAndLeases(t *testing.T) {
	store, node, _, report, config := managedFixture(t)
	action := ipsentinel.ExecutorAction{ID: ipsentinel.NewUUID(), Kind: ipsentinel.MethodConfigValidate, Config: &config}
	if err := store.CreateExecutorAction(t.Context(), node, action); err != nil {
		t.Fatal(err)
	}
	setRuntimeDisabled(t, store, node, true)
	if leased, err := store.LeaseExecutorAction(t.Context(), node, report.Hello); err != nil || leased.ID != "" {
		t.Fatal("disabled node received queued action", leased, err)
	}
	action.ID = ipsentinel.NewUUID()
	if err := store.CreateExecutorAction(t.Context(), node, action); !errors.Is(err, model.ErrConflict) {
		t.Fatal("disabled node accepted new action", err)
	}
	setRuntimeDisabled(t, store, node, false)
	leased := leaseManaged(t, store, node, report.Hello)
	setRuntimeDisabled(t, store, node, true)
	digest, err := ipsentinel.ConfigSHA256(config)
	if err != nil {
		t.Fatal(err)
	}
	finishManaged(t, store, node, leased, ipsentinel.ActionResult{Accepted: true, Validation: &ipsentinel.Validation{Valid: true, ConfigSHA256: digest, Issues: []ipsentinel.ValidationIssue{}}})
}
