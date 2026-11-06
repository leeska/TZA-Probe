package executorruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/ipsentinel"
)

type lifecycleStub struct {
	hello        ipsentinel.Hello
	state        string
	definition   string
	calls        []string
	startFailure bool
	verifyError  bool
	onPause      func()
	onStart      func()
}

func (local *lifecycleStub) Hello(context.Context) (ipsentinel.Hello, error) {
	hello := local.hello
	hello.Status.ObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return hello, nil
}

func (local *lifecycleStub) Pause(_ context.Context, _ string, _ ipsentinel.Fence) error {
	local.calls = append(local.calls, "pause")
	local.hello.Status.Control.Paused = true
	local.hello.Status.Control.PauseGeneration++
	reason := "operator"
	local.hello.Status.Control.PauseReason = &reason
	if local.onPause != nil {
		local.onPause()
	}
	return nil
}

func (local *lifecycleStub) Verify(context.Context, OperationPlan) error {
	local.calls = append(local.calls, "verify")
	if local.verifyError {
		return errors.New("installed runtime changed")
	}
	return nil
}

func (local *lifecycleStub) Service(_ context.Context, action, _ string) (string, error) {
	switch action {
	case "definition":
		return local.definition, nil
	case "state":
		return local.state, nil
	case "stop":
		local.calls = append(local.calls, "stop")
		local.state = "inactive"
	case "start":
		local.calls = append(local.calls, "start")
		if local.onStart != nil {
			local.onStart()
		}
		if local.startFailure {
			return "", errors.New("start failed")
		}
		local.state = "active"
	}
	return "", nil
}

func lifecycleFixture(t *testing.T) (fixture, Store, string, OperationOptions, RuntimePeer, *lifecycleStub) {
	t.Helper()
	data, store, digest, options := operationFixture(t, "select")
	if err := os.Mkdir(options.InstallRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("releases/"+strings.Repeat("d", 64), filepath.Join(options.InstallRoot, "current")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../ipsentinel/testdata/contracts/ipsentinel-executor-v1/examples/hello.response.json")
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Result ipsentinel.Hello `json:"result"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	hello := response.Result
	hello.Status.ObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
	hello.Status.Control.Mode = ipsentinel.ModeManaged
	hello.Status.Control.Paused = false
	hello.Status.Control.PauseReason = nil
	hello.Status.Control.Draining = false
	hello.Status.Control.ActiveRunID = nil
	hello.Status.Journal = ipsentinel.JournalStatus{EarliestSeq: 1, LatestSeq: 6, AckedSeq: 6}
	hello.Features = []string{"run", "journal", "pause", "config", "cancel", "assessment"}
	hello.LocalPolicy.HardLimitsAvailable = true
	hello.RuntimeVersion = "0.0.1"
	if hello.Status.Config == nil {
		hello.Status.Config = &ipsentinel.ConfigRef{Revision: 1, SHA256: strings.Repeat("a", 64)}
	}
	if hello.Status.Budget == nil {
		hello.Status.Budget = &ipsentinel.BudgetStatus{DayUTC: "2026-11-06", Limits: ipsentinel.DailyLimits{Runs: 24, Navigations: 100, NetworkBytes: 1 << 25}, NetworkAccounting: "known"}
	}
	options.Action = "drain"
	peer := RuntimePeer{SocketPath: "/run/ipsentinel/control.sock", UID: 1001, GID: 1001}
	local := &lifecycleStub{hello: hello, state: "active", definition: "[Service]\nUser=ipsentinel\nExecStart=/fixed/entry"}
	return data, store, digest, options, peer, local
}

func approveLifecycle(review LifecycleReview) OperationApproval {
	approval := review.ApprovalTemplate
	approval.Approved = true
	return approval
}

func TestLifecycleDrainSelectStartupAndEpochHandoff(t *testing.T) {
	data, store, digest, options, peer, local := lifecycleFixture(t)
	review, err := PrepareLifecycle(t.Context(), store, digest, data.keys, data.host, data.target, options, peer, local)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyLifecycle(t.Context(), store, review, review.ApprovalTemplate, local); err == nil || len(local.calls) != 0 {
		t.Fatal("lifecycle ran without its exact approval", err, local.calls)
	}
	drained, err := ApplyLifecycle(t.Context(), store, review, approveLifecycle(review), local)
	if err != nil || drained.State != "drained" || local.state != "inactive" {
		t.Fatal(drained, local.state, err)
	}
	checkpoint, _, err := store.loadDrain()
	if err != nil || !drainedHello(checkpoint.Hello) {
		t.Fatal(checkpoint, err)
	}
	selected := data.inspect(t).plan.ArchiveSHA256
	os.Remove(filepath.Join(options.InstallRoot, "current"))
	os.Symlink("releases/"+selected, filepath.Join(options.InstallRoot, "current"))
	os.WriteFile(filepath.Join(options.InstallRoot, "selection.json"), []byte(`{"state":"committed"}`), 0o600)
	options.Action = "activate"
	start, err := PrepareLifecycle(t.Context(), store, digest, data.keys, data.host, data.target, options, peer, local)
	if err != nil {
		t.Fatal(err)
	}
	local.hello.RuntimeVersion = start.Plan.Operation.Version
	local.hello.Status.Control.Epoch++
	local.hello.DescriptorSHA256 = strings.Repeat("f", 64)
	local.hello.Status.Health = "degraded"
	receipt, err := ApplyLifecycle(t.Context(), store, start, approveLifecycle(start), local)
	if err != nil || receipt.State != "started_paused" || !receipt.Hello.Status.Control.Paused || receipt.Hello.Status.Control.Epoch <= drained.Hello.Status.Control.Epoch {
		t.Fatal(receipt, err)
	}
	request := ChannelRequest{ID: ipsentinel.NewUUID(), ChannelInput: ChannelInput{Action: "activate", Version: optionsVersion(start), StageRequestID: ipsentinel.NewUUID()},
		Enrollment: Enrollment{ScopeSHA256: strings.Repeat("a", 64), Target: data.target, Host: data.host}, PlanSHA256: start.PlanSHA256, StagePlanSHA256: digest, Lifecycle: &start}
	result := ChannelResult{RequestID: request.ID, State: receipt.State, PlanSHA256: start.PlanSHA256, LifecycleReceipt: &receipt}
	receipt.Hello.Status.ObservedAt = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	if err := ValidateChannelReceipt(request, result); err != nil {
		raw, _ := json.Marshal(receipt.Hello)
		t.Fatal("durable receipt became invalid before a delayed retry", err, ipsentinel.ValidateDefinition("hello", raw, nil))
	}
	if strings.Join(local.calls, ",") != "pause,stop,verify,start" {
		t.Fatal("unexpected lifecycle side effects", local.calls)
	}
}

func optionsVersion(review LifecycleReview) string { return review.Plan.Operation.Version }

func TestLifecycleDrainRefusesActiveUnackedOrChangedExecutor(t *testing.T) {
	for _, fault := range []string{"active", "unacked", "epoch", "config", "resumed", "definition", "selection", "policy"} {
		t.Run(fault, func(t *testing.T) {
			data, store, digest, options, peer, local := lifecycleFixture(t)
			review, err := PrepareLifecycle(t.Context(), store, digest, data.keys, data.host, data.target, options, peer, local)
			if err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "active":
				run := ipsentinel.NewUUID()
				local.hello.Status.Control.ActiveRunID = &run
			case "unacked":
				local.hello.Status.Journal.AckedSeq--
			case "epoch":
				local.onPause = func() { local.hello.Status.Control.Epoch++ }
			case "config":
				local.onPause = func() {
					local.hello.Status.Config = &ipsentinel.ConfigRef{Revision: 2, SHA256: strings.Repeat("b", 64)}
				}
			case "resumed":
				local.onPause = func() { local.hello.Status.Control.Paused = false }
			case "definition":
				local.definition += "changed"
			case "selection":
				os.WriteFile(filepath.Join(options.InstallRoot, "selection.json"), []byte("{}"), 0o600)
			case "policy":
				os.WriteFile(options.PolicyPath, []byte("changed"), 0o600)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Millisecond)
			defer cancel()
			if _, err := ApplyLifecycle(ctx, store, review, approveLifecycle(review), local); err == nil || local.state != "active" {
				t.Fatal("unsafe drain stopped the service", err, local.calls)
			}
			if _, _, err := store.loadDrain(); err == nil {
				t.Fatal("incomplete drain produced an activation checkpoint")
			}
		})
	}
}

func TestLifecycleStartupFailuresNeverResumeOrRollback(t *testing.T) {
	for _, fault := range []string{"start", "inventory", "epoch", "instance", "journal", "config", "capabilities", "unpaused", "unknown-health", "checkpoint"} {
		t.Run(fault, func(t *testing.T) {
			data, store, digest, options, peer, local := lifecycleFixture(t)
			review, err := PrepareLifecycle(t.Context(), store, digest, data.keys, data.host, data.target, options, peer, local)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ApplyLifecycle(t.Context(), store, review, approveLifecycle(review), local); err != nil {
				t.Fatal(err)
			}
			os.Remove(filepath.Join(options.InstallRoot, "current"))
			os.Symlink("releases/"+data.inspect(t).plan.ArchiveSHA256, filepath.Join(options.InstallRoot, "current"))
			options.Action = "activate"
			start, err := PrepareLifecycle(t.Context(), store, digest, data.keys, data.host, data.target, options, peer, local)
			if err != nil {
				t.Fatal(err)
			}
			local.hello.RuntimeVersion = start.Plan.Operation.Version
			local.hello.Status.Control.Epoch++
			switch fault {
			case "start":
				local.startFailure = true
			case "inventory":
				local.verifyError = true
			case "epoch":
				local.hello.Status.Control.Epoch = start.Plan.Before.Epoch
			case "instance":
				local.hello.Status.ExecutorInstanceID = ipsentinel.NewUUID()
			case "journal":
				local.hello.Status.Journal.LatestSeq = 0
			case "config":
				local.hello.Status.Config = &ipsentinel.ConfigRef{Revision: 9, SHA256: strings.Repeat("a", 64)}
			case "capabilities":
				local.hello.LocalPolicy.HardLimitsAvailable = false
			case "unpaused":
				local.hello.Status.Control.Paused = false
			case "unknown-health":
				local.hello.Status.Health = "unavailable"
			case "checkpoint":
				store.saveDrain(drainCheckpoint{})
			}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
			defer cancel()
			if _, err := ApplyLifecycle(ctx, store, start, approveLifecycle(start), local); err == nil || local.state != "inactive" {
				t.Fatal("failed startup was considered healthy or left active", err, local.calls)
			}
			current, _, _ := selectionSnapshot(options.InstallRoot)
			if current != start.Plan.Operation.ExpectedCurrent {
				t.Fatal("failed startup selected an older runtime")
			}
		})
	}
}
