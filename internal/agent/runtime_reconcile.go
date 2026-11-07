package agent

import (
	"context"
	"errors"
	"path/filepath"

	runtimepkg "github.com/leeska/TZA-Probe/internal/executorruntime"
	"github.com/leeska/TZA-Probe/internal/ipsentinel"
)

func (local *LocalRuntime) Reconcile(ctx context.Context, request runtimepkg.ChannelRequest) (runtimepkg.ChannelResult, error) {
	result := runtimepkg.ChannelResult{RequestID: request.ID, PlanSHA256: request.PlanSHA256}
	if local == nil || !request.Enrollment.SameScope(local.Enrollment) || request.State != "unknown" {
		return result, errors.New("runtime inspection requires the original node and unresolved request")
	}
	review := runtimepkg.ChannelResult{RequestID: request.ID, State: "awaiting_approval", PlanSHA256: request.PlanSHA256, Review: request.Review, Operation: request.Operation, Lifecycle: request.Lifecycle}
	if err := runtimepkg.ValidateChannelPlan(request, review); err != nil {
		return result, err
	}
	original := *local
	original.Enrollment = request.Enrollment
	local = &original
	if request.Action == "enable" {
		root, err := (FileRuntimePending{Directory: filepath.Join(local.Config.StagingDir, "agent-pending")}).open()
		if err != nil {
			return result, err
		}
		defer root.Close()
		raw, err := readRuntimeJournal(root, "enabled.json")
		if err != nil {
			return result, errors.New("plugin initialization has no completed startup receipt; local inspection required")
		}
		if err := ipsentinel.DecodeStrict(raw, &result); err != nil {
			return result, err
		}
		if err := runtimepkg.ValidateChannelReceipt(request, result); err != nil {
			return result, err
		}
		return result, nil
	}
	if request.Action == "stage" {
		receipt, err := (runtimepkg.Store{Dir: local.Config.StagingDir}).Verify(ctx, request.PlanSHA256, local.Keys, local.Enrollment.Host, local.Enrollment.Target)
		if err != nil {
			return result, err
		}
		result.State, result.Receipt = "staged", &receipt
		return result, nil
	}
	if request.Action != "install" {
		return result, errors.New("runtime inspection cannot repeat lifecycle operations")
	}
	preflight := request
	preflight.State, preflight.PlanSHA256, preflight.Review, preflight.Operation, preflight.Lifecycle = "requested", "", nil, nil, nil
	current, err := local.Run(ctx, preflight)
	if err != nil || !sameRuntimeReview(request, current) {
		return result, errors.New("installation inputs changed; automatic inspection cannot resolve the operation")
	}
	receipt, err := runtimepkg.InspectInstallation(ctx, request.Operation.Plan)
	if err != nil {
		return result, err
	}
	if receipt == nil {
		result.State, result.ErrorCode = "failed", "operation_not_applied"
	} else {
		result.State, result.OperationReceipt = "installed", receipt
	}
	return result, nil
}
