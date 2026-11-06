package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/agent"
	"github.com/leeska/TZA-Probe/internal/ipsentinel"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/storage"
	"github.com/leeska/TZA-Probe/web"
)

type lostExecutorReceipt struct {
	*agent.HTTPTransport
}

func (transport lostExecutorReceipt) SaveExecutorRecords(ctx context.Context, report ipsentinel.SyncReport) (ipsentinel.SyncReceipt, error) {
	if _, err := transport.HTTPTransport.SaveExecutorRecords(ctx, report); err != nil {
		return ipsentinel.SyncReceipt{}, err
	}
	return ipsentinel.SyncReceipt{}, errors.New("deliberately lost durable HTTP receipt")
}

func TestInstalledLinuxRuntimeThroughTZAAgent(t *testing.T) {
	socket := os.Getenv("TZA_EXECUTOR_LIVE_SOCKET")
	if socket == "" {
		t.Skip("TZA_EXECUTOR_LIVE_SOCKET is not set for an isolated installed Linux executor")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("installed worker acceptance requires Linux")
	}
	raw, err := os.ReadFile(os.Getenv("TZA_EXECUTOR_LIVE_INPUT"))
	if err != nil {
		t.Fatal(err)
	}
	var input struct {
		Target ipsentinel.Target `json:"target"`
		Config ipsentinel.Config `json:"config"`
	}
	if err := ipsentinel.DecodeStrict(raw, &input); err != nil || len(input.Config.Services) != 1 {
		t.Fatal("one explicitly approved service is required", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	client := ipsentinel.NewClient(socket, input.Target)
	defer client.Close()
	var hello ipsentinel.Hello
	for {
		hello, err = client.Hello(ctx, "")
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("installed executor did not become reachable", err)
		}
		time.Sleep(250 * time.Millisecond)
	}
	if hello.RuntimeVersion != "0.4.0" || hello.Status.Control.Mode != ipsentinel.ModeManaged || !ipsentinel.HasFeature(hello, "run") || !hello.LocalPolicy.HardLimitsAvailable {
		t.Fatal("installed runtime is not executable under its local policy and isolation", hello)
	}
	store, err := storage.Open(filepath.Join(t.TempDir(), "live-controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	node, token, err := store.SaveNode(ctx, model.Node{
		Name:    "isolated installed executor acceptance",
		Billing: model.Billing{ResetAt: "2026-11-01T00:00:00Z", TrafficMode: "sum"},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	binding := ipsentinel.Binding{
		Target: input.Target, ExecutorInstanceID: hello.Status.ExecutorInstanceID,
		DescriptorSHA256: hello.DescriptorSHA256, Epoch: hello.Status.Control.Epoch, Mode: ipsentinel.ModeManaged,
	}
	if err := store.BindExecutor(ctx, node, binding); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer((&API{Store: store, AdminToken: model.Secret(), Assets: web.Handler()}).Handler())
	defer server.Close()
	transport, err := agent.NewTransport(server.URL, token, false)
	if err != nil {
		t.Fatal(err)
	}
	syncer := ipsentinel.Synchronizer{Reader: client, Transport: transport, Target: input.Target}
	syncOnce := func() {
		t.Helper()
		if err := syncer.Sync(ctx); err != nil {
			t.Fatal("real Agent HTTP/UDS synchronization failed", err)
		}
	}
	queue := func(action ipsentinel.ExecutorAction) {
		t.Helper()
		action.ID = ipsentinel.NewUUID()
		if err := store.CreateExecutorAction(ctx, node, action); err != nil {
			t.Fatal(err)
		}
		syncOnce()
		syncOnce()
	}
	syncOnce()
	queue(ipsentinel.ExecutorAction{Kind: ipsentinel.MethodConfigValidate, Config: &input.Config})
	queue(ipsentinel.ExecutorAction{Kind: ipsentinel.MethodConfigApply, Config: &input.Config})
	status, err := client.StatusGet(ctx, "")
	if err != nil || status.Config == nil {
		t.Fatal("runtime did not apply the approved config", status, err)
	}
	initialRuns := status.Budget.UsedRuns
	now := time.Now().UTC()
	job := ipsentinel.ExecutorAction{
		Kind: ipsentinel.MethodRunSubmit, JobID: ipsentinel.NewUUID(),
		ConfigRevision: status.Config.Revision, ConfigSHA256: status.Config.SHA256,
		ServiceID: input.Config.Services[0].ServiceID, Purpose: ipsentinel.PurposeObserve, Phase: ipsentinel.PhaseBaseline,
		NotBefore: now.Add(-time.Second).Format(time.RFC3339), ExpiresAt: now.Add(5 * time.Minute).Format(time.RFC3339),
		TimeoutMS: input.Config.Limits.TimeoutMS,
	}
	queue(job)
	var terminal ipsentinel.Run
	for {
		terminal, err = client.RunGet(ctx, "", job.JobID)
		if err != nil {
			t.Fatal(err)
		}
		if terminal.Result != nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("real worker did not reach terminal")
		}
		time.Sleep(250 * time.Millisecond)
	}
	if terminal.State != ipsentinel.StateCompleted || terminal.Result.Resources.Cleanup != "complete" || !terminal.Result.Resources.HardLimitsVerified || terminal.Result.Resources.OOMKillCount != 0 {
		t.Fatal("real installed browser did not complete with verified limits and cleanup", terminal)
	}
	fault := ipsentinel.Synchronizer{Reader: client, Transport: lostExecutorReceipt{transport}, Target: input.Target}
	if err := fault.Sync(ctx); err == nil {
		t.Fatal("lost HTTP receipt was treated as an ACK")
	}
	durable, err := store.ExecutorCursor(ctx, node)
	if err != nil {
		t.Fatal(err)
	}
	before, err := client.StatusGet(ctx, "")
	if err != nil || before.Journal.AckedSeq >= durable.ThroughSeq {
		t.Fatal("runtime ACK advanced before a durable receipt reached the Agent", before, durable, err)
	}
	syncOnce()
	queue(job)
	again, err := client.RunGet(ctx, "", job.JobID)
	if err != nil || again.RunID != terminal.RunID {
		t.Fatal("logical job replay started another installed browser run", again, terminal, err)
	}
	after, err := client.StatusGet(ctx, "")
	if err != nil || after.Budget.UsedRuns != initialRuns+1 || after.Journal.AckedSeq != durable.ThroughSeq {
		t.Fatal("budget or committed ACK changed on replay", after, durable, err)
	}
	actions, err := store.ExecutorActions(ctx, node)
	if err != nil {
		t.Fatal(err)
	}
	observed := false
	for _, action := range actions {
		if action.Kind == ipsentinel.MethodRunSubmit && action.Run != nil && action.Run.RunID == terminal.RunID && action.Run.State == ipsentinel.StateCompleted {
			observed = true
		}
	}
	if !observed {
		t.Fatal("native completed result was not projected into TZA commands")
	}
	result, _ := json.Marshal(map[string]any{
		"runtime_version": hello.RuntimeVersion, "instance": hello.Status.ExecutorInstanceID,
		"epoch": hello.Status.Control.Epoch, "run_id": terminal.RunID,
		"through_seq": durable.ThroughSeq, "used_runs": after.Budget.UsedRuns,
		"resources": terminal.Result.Resources, "agent_uid": os.Geteuid(),
	})
	t.Log(string(result))
}
