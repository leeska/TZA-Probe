package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/agent"
	runtimepkg "github.com/leeska/TZA-Probe/internal/executorruntime"
	"github.com/leeska/TZA-Probe/internal/ipsentinel"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/storage"
	"github.com/leeska/TZA-Probe/web"
)

type runtimeUpgradeInput struct {
	Config      agent.RuntimeInstallConfig `json:"config"`
	StagePlans  map[string]string          `json:"stage_plans"`
	Evidence    string                     `json:"evidence_dir"`
	AgentBinary string                     `json:"agent_binary"`
	ToolsDir    string                     `json:"tools_dir"`
	Versions    []string                   `json:"versions"`
	ConfigPlan  ipsentinel.Config          `json:"config_plan"`
}

type runtimeUpgradeQA struct {
	t         *testing.T
	input     runtimeUpgradeInput
	store     *storage.Store
	node      string
	local     *agent.LocalRuntime
	installer agent.RuntimeInstaller
	transport *runtimeLiveTransport
	worker    *runtimeLiveWorker
	control   runtimepkg.LocalLifecycle
	sources   map[string]runtimepkg.ChannelRequest
}

func TestRuntimeUpgradeRollbackWithRealChromiumLinux(t *testing.T) {
	name := os.Getenv("TZA_RUNTIME_UPGRADE_LINUX_INPUT")
	if name == "" {
		t.Skip("set TZA_RUNTIME_UPGRADE_LINUX_INPUT for an isolated Debian 13 runtime upgrade")
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Fatal("runtime upgrade acceptance requires privileged Linux")
	}
	raw, err := os.ReadFile(name)
	var input runtimeUpgradeInput
	if err != nil || ipsentinel.DecodeStrict(raw, &input) != nil || len(input.Versions) != 2 || input.Config.Peer.UID == 0 || input.Config.Peer.GID == 0 {
		t.Fatal("two signed staged candidates and a non-root peer are required", err)
	}
	if !strings.HasPrefix(input.Config.Unit, "ipsentinel-tza-upgrade-qa-") ||
		!strings.HasPrefix(input.Config.InstallRoot, "/opt/tza-upgrade-runtime-qa-") ||
		!strings.HasPrefix(input.Config.StateDir, "/var/lib/ipsentinel/tza-upgrade-qa-") {
		t.Fatal("live upgrade acceptance is restricted to isolated QA service and data paths")
	}
	journal := agent.FileRuntimePending{Directory: filepath.Join(input.Config.StagingDir, "agent-pending")}
	local, err := agent.ProvisionRuntime(input.Config, journal)
	if err != nil {
		t.Fatal(err)
	}
	for _, executable := range []string{input.AgentBinary, "/usr/local/libexec/tza/tza-runtime-installer"} {
		output, err := exec.CommandContext(t.Context(), executable, "--version").CombinedOutput()
		if err != nil {
			t.Fatalf("Linux Agent/helper preflight failed for %s: %v: %s", executable, err, output)
		}
	}
	store, err := storage.Open(filepath.Join(t.TempDir(), "upgrade-controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	node, token, err := store.SaveNode(t.Context(), model.Node{Name: "isolated upgrade acceptance", Billing: model.Billing{ResetAt: "2026-11-01T00:00:00Z", TrafficMode: "sum"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer((&API{Store: store, AdminToken: model.Secret(), Assets: web.Handler()}).Handler())
	defer server.Close()
	httpTransport, err := agent.NewTransport(server.URL, token, true)
	if err != nil {
		t.Fatal(err)
	}
	transport := &runtimeLiveTransport{HTTPTransport: httpTransport}
	worker := &runtimeLiveWorker{local: local}
	qa := runtimeUpgradeQA{t: t, input: input, store: store, node: node, local: local, transport: transport, worker: worker,
		control: runtimepkg.LocalLifecycle{Peer: input.Config.Peer, Target: input.Config.Target}, sources: map[string]runtimepkg.ChannelRequest{}}
	qa.installer = agent.RuntimeInstaller{Transport: transport, Worker: worker, Enrollment: local.Enrollment, Journal: journal}
	qa.exchange()
	for _, version := range input.Versions {
		qa.registerStaged(version)
	}
	hello := qa.hello(input.Versions[0], false)
	if err := store.BindExecutor(t.Context(), node, upgradeBinding(input.Config.Target, hello)); err != nil {
		t.Fatal(err)
	}
	stopAgent := qa.startAgent(server.URL, token)
	defer stopAgent()
	qa.awaitSync(hello.Status.Control.Epoch)
	qa.applyConfig()
	hello = qa.hello(input.Versions[0], true)
	configRef := *hello.Status.Config
	baseline := qa.observe()
	runs := []ipsentinel.Run{baseline}
	qa.save("baseline.json", map[string]any{"hello": hello, "run": baseline})
	qa.operation("install", input.Versions[1])
	for _, version := range []string{input.Versions[1], input.Versions[0]} {
		previous := qa.hello(hello.RuntimeVersion, true)
		drain := qa.operation("drain", version)
		if drain.LifecycleReceipt == nil {
			t.Fatal("missing durable drain receipt", drain)
		}
		qa.operation("select", version)
		startup := qa.operation("activate", version)
		if startup.LifecycleReceipt == nil {
			t.Fatal("missing verified startup receipt", startup)
		}
		hello = startup.LifecycleReceipt.Hello
		if hello.RuntimeVersion != version || hello.Status.ExecutorInstanceID != previous.Status.ExecutorInstanceID ||
			hello.Status.Control.Epoch <= previous.Status.Control.Epoch || !hello.Status.Control.Paused ||
			hello.Status.Config == nil || *hello.Status.Config != configRef || hello.Status.Budget == nil ||
			hello.Status.Budget.UsedRuns != previous.Status.Budget.UsedRuns {
			t.Fatal("upgrade/rollback did not preserve paused installation, config, budget and fresh epoch", hello)
		}
		if err := store.BindExecutor(t.Context(), node, upgradeBinding(input.Config.Target, previous)); err == nil {
			t.Fatal("old epoch unexpectedly released maintenance")
		}
		qa.exchange()
		if err := store.BindExecutor(t.Context(), node, upgradeBinding(input.Config.Target, hello)); err != nil {
			t.Fatal("explicit new epoch binding failed", err)
		}
		qa.awaitSync(hello.Status.Control.Epoch)
		qa.applyConfig()
		qa.action(ipsentinel.ExecutorAction{Kind: ipsentinel.MethodControlResume, ExpectedPauseGeneration: hello.Status.Control.PauseGeneration}, false)
		hello = qa.hello(version, true)
		run := qa.observe()
		runs = append(runs, run)
		qa.save("transition-"+version+".json", map[string]any{"before": previous, "drain": drain, "startup": startup, "after": hello, "run": run})
	}
	records, err := store.ExecutorRecords(t.Context(), node, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range runs {
		preserved := false
		for _, record := range records {
			if record.Kind == ipsentinel.RecordRunTerminal {
				var result ipsentinel.RunResult
				if json.Unmarshal(record.Data, &result) == nil && result.RunID == run.RunID {
					preserved = true
				}
			}
		}
		if !preserved {
			t.Fatal("browser terminal record was lost", run.RunID)
		}
	}
	final := qa.hello(input.Versions[0], true)
	if worker.executions != 7 {
		t.Fatal("upgrade and rollback did not execute exactly seven approved helper operations", worker.executions)
	}
	qa.save("final.json", map[string]any{"hello": final, "runs": runs, "records": records, "helper_executions": worker.executions})
	t.Logf("real Chromium %s -> %s -> %s; non-root Agent UID=%d; retained config/budget/journal; explicit fresh-epoch binding and resume", input.Versions[0], input.Versions[1], input.Versions[0], input.Config.Peer.UID)
}

func (qa *runtimeUpgradeQA) registerStaged(version string) {
	qa.t.Helper()
	receipt, err := (runtimepkg.Store{Dir: qa.input.Config.StagingDir}).Verify(qa.t.Context(), qa.input.StagePlans[version], qa.local.Keys, qa.local.Enrollment.Host, qa.input.Config.Target)
	if err != nil || receipt.Plan.Version != version {
		qa.t.Fatal("pre-staged signed real candidate is invalid", version, err)
	}
	request, err := qa.store.CreateRuntimeRequest(qa.t.Context(), qa.node, runtimepkg.ChannelInput{Action: "stage", Version: version})
	if err != nil {
		qa.t.Fatal(err)
	}
	review := runtimepkg.Review{Plan: receipt.Plan, PlanSHA256: receipt.PlanSHA256, ApprovalTemplate: runtimepkg.Approval{FormatVersion: 1, PlanSHA256: receipt.PlanSHA256}}
	qa.report(runtimepkg.ChannelResult{RequestID: request.ID, State: "awaiting_approval", PlanSHA256: review.PlanSHA256, Review: &review})
	if err := qa.store.ApproveRuntimeRequest(qa.t.Context(), qa.node, request.ID, runtimepkg.ChannelApproval{Approved: true, PlanSHA256: review.PlanSHA256}); err != nil {
		qa.t.Fatal(err)
	}
	qa.report(runtimepkg.ChannelResult{RequestID: request.ID, State: "running", PlanSHA256: receipt.PlanSHA256})
	qa.report(runtimepkg.ChannelResult{RequestID: request.ID, State: "staged", PlanSHA256: receipt.PlanSHA256, Receipt: &receipt})
	qa.sources[version] = request
}

func (qa *runtimeUpgradeQA) operation(action, version string) runtimepkg.ChannelRequest {
	qa.t.Helper()
	qa.exchange()
	request, err := qa.store.CreateRuntimeRequest(qa.t.Context(), qa.node, runtimepkg.ChannelInput{Action: action, Version: version, StageRequestID: qa.sources[version].ID})
	if err != nil {
		qa.t.Fatal(action, err)
	}
	qa.exchange()
	planned := qa.request(request.ID)
	if planned.State != "awaiting_approval" {
		qa.t.Fatal("node could not generate its exact local operation plan", planned)
	}
	qa.save(action+"-"+version+"-plan.json", planned)
	if err := qa.store.ApproveRuntimeRequest(qa.t.Context(), qa.node, request.ID, runtimepkg.ChannelApproval{Approved: true, PlanSHA256: planned.PlanSHA256}); err != nil {
		qa.t.Fatal(err)
	}
	expected := map[string]string{"install": "installed", "select": "selected", "drain": "drained", "activate": "started_paused"}[action]
	before := qa.worker.executions
	qa.transport.dropState = expected
	if err := qa.installer.Sync(qa.t.Context()); err == nil {
		qa.t.Fatal("test expected the committed terminal HTTP reply to be lost", action)
	}
	finished := qa.request(request.ID)
	qa.save(action+"-"+version+"-outcome.json", finished)
	if finished.State != expected || qa.worker.executions != before+1 {
		qa.t.Fatal("real helper did not complete exactly once", finished, before, qa.worker.executions)
	}
	qa.exchange()
	if qa.worker.executions != before+1 {
		qa.t.Fatal("terminal receipt replay repeated a runtime/service side effect", action)
	}
	qa.save(action+"-"+version+"-receipt.json", finished)
	qa.t.Logf("%s %s completed; lost HTTP receipt replay did not repeat its helper", action, version)
	return finished
}

func (qa *runtimeUpgradeQA) action(action ipsentinel.ExecutorAction, terminal bool) ipsentinel.ActionView {
	qa.t.Helper()
	hello, err := qa.control.Hello(qa.t.Context())
	if err != nil {
		qa.t.Fatal(err)
	}
	qa.awaitSync(hello.Status.Control.Epoch)
	action.ID = ipsentinel.NewUUID()
	if err := qa.store.CreateExecutorAction(qa.t.Context(), qa.node, action); err != nil {
		qa.t.Fatal(action.Kind, err)
	}
	var result ipsentinel.ActionView
	upgradeWait(qa.t, 150*time.Second, "ordinary Agent completing "+action.Kind, func() bool {
		views, err := qa.store.ExecutorActions(qa.t.Context(), qa.node)
		if err != nil {
			qa.t.Fatal(err)
		}
		for _, view := range views {
			if view.ID != action.ID {
				continue
			}
			if view.Status == "rejected" || view.Status == "expired" || view.Status == "cancelled" {
				qa.save("rejected-"+action.ID+".json", view)
				qa.t.Fatal("real managed action rejected", view)
			}
			if terminal && view.Run != nil && view.Run.Result != nil || !terminal && view.Status == "completed" {
				result = view
				return true
			}
		}
		return false
	})
	return result
}

func (qa *runtimeUpgradeQA) applyConfig() {
	qa.t.Helper()
	hello, err := qa.control.Hello(qa.t.Context())
	if err != nil {
		qa.t.Fatal(err)
	}
	revision := int64(0)
	if hello.Status.Config != nil {
		revision = hello.Status.Config.Revision
	}
	qa.action(ipsentinel.ExecutorAction{Kind: ipsentinel.MethodConfigValidate, Config: &qa.input.ConfigPlan}, false)
	qa.action(ipsentinel.ExecutorAction{Kind: ipsentinel.MethodConfigApply, Config: &qa.input.ConfigPlan, ExpectedRevision: revision}, false)
}

func (qa *runtimeUpgradeQA) observe() ipsentinel.Run {
	qa.t.Helper()
	hello, err := qa.control.Hello(qa.t.Context())
	if err != nil || hello.Status.Config == nil {
		qa.t.Fatal("native config unavailable", hello, err)
	}
	now := time.Now().UTC()
	result := qa.action(ipsentinel.ExecutorAction{Kind: ipsentinel.MethodRunSubmit, JobID: ipsentinel.NewUUID(), ConfigRevision: hello.Status.Config.Revision,
		ConfigSHA256: hello.Status.Config.SHA256, ServiceID: "cloudflare", Purpose: ipsentinel.PurposeObserve, Phase: ipsentinel.PhaseBaseline,
		NotBefore: now.Add(-time.Second).Format(time.RFC3339), ExpiresAt: now.Add(5 * time.Minute).Format(time.RFC3339), TimeoutMS: qa.input.ConfigPlan.Limits.TimeoutMS}, true)
	run := *result.Run
	if run.State != ipsentinel.StateCompleted || run.Result.Resources.Cleanup != "complete" || !run.Result.Resources.HardLimitsVerified || run.Result.Resources.OOMKillCount != 0 {
		qa.t.Fatal("real Chromium failed isolation, completion or cleanup validation", run)
	}
	upgradeWait(qa.t, 40*time.Second, "ordinary Agent ACK of all journal entries", func() bool {
		current, err := qa.control.Hello(qa.t.Context())
		return err == nil && current.Status.Journal.AckedSeq == current.Status.Journal.LatestSeq
	})
	qa.t.Logf("real Chromium completed: run=%s epoch=%d", run.RunID, run.Result.Epoch)
	return run
}

func (qa *runtimeUpgradeQA) hello(version string, ready bool) ipsentinel.Hello {
	qa.t.Helper()
	var hello ipsentinel.Hello
	var lastError error
	defer func() {
		if qa.t.Failed() {
			qa.save("hello-"+version+"-failure.json", map[string]any{"hello": hello, "error": fmt.Sprint(lastError), "require_ready": ready})
		}
	}()
	upgradeWait(qa.t, 100*time.Second, "managed hello "+version, func() bool {
		value, err := qa.control.Hello(qa.t.Context())
		hello, lastError = value, err
		return err == nil && value.RuntimeVersion == version && value.Status.Control.Mode == ipsentinel.ModeManaged && value.LocalPolicy.HardLimitsAvailable &&
			(!ready || value.Status.Health == "ready" && !value.Status.Control.Paused)
	})
	return hello
}

func (qa *runtimeUpgradeQA) awaitSync(epoch int64) {
	qa.t.Helper()
	expected, err := qa.control.Hello(qa.t.Context())
	if err != nil {
		qa.t.Fatal(err)
	}
	upgradeWait(qa.t, 70*time.Second, fmt.Sprintf("ordinary non-root Agent sync of epoch %d", epoch), func() bool {
		views, err := qa.store.Executors(qa.t.Context())
		if err != nil {
			qa.t.Fatal(err)
		}
		for _, view := range views {
			if view.NodeID == qa.node && view.Hello != nil && view.Hello.Status.Control.Epoch == epoch && time.Now().Unix()-view.ReceivedAt < 30 &&
				view.Hello.Status.Control.Paused == expected.Status.Control.Paused && view.Hello.Status.Control.PauseGeneration == expected.Status.Control.PauseGeneration &&
				sameUpgradeConfig(view.Hello.Status.Config, expected.Status.Config) {
				return true
			}
		}
		return false
	})
}

func sameUpgradeConfig(first, second *ipsentinel.ConfigRef) bool {
	if first == nil || second == nil {
		return first == nil && second == nil
	}
	return *first == *second
}

func (qa *runtimeUpgradeQA) startAgent(endpoint, token string) func() {
	qa.t.Helper()
	path := filepath.Join(qa.input.ToolsDir, "agent.json")
	raw, _ := json.Marshal(map[string]any{"endpoint": endpoint, "token": token, "allow_http": true,
		"ipsentinel": map[string]any{"socket_path": qa.input.Config.Peer.SocketPath, "target": qa.input.Config.Target}})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		qa.t.Fatal(err)
	}
	if err := os.Chown(path, int(qa.input.Config.Peer.UID), int(qa.input.Config.Peer.GID)); err != nil {
		qa.t.Fatal(err)
	}
	log, err := os.OpenFile(filepath.Join(qa.input.Evidence, "ordinary-agent.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		qa.t.Fatal(err)
	}
	command := exec.Command(qa.input.AgentBinary, "--config", path)
	command.Dir, command.Env = "/", []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8", "HOME=" + qa.input.ToolsDir}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Credential: &syscall.Credential{Uid: qa.input.Config.Peer.UID, Gid: qa.input.Config.Peer.GID, Groups: []uint32{}}}
	command.Stdout, command.Stderr = log, log
	if err := command.Start(); err != nil {
		_ = log.Close()
		qa.t.Fatal(err)
	}
	return func() {
		_ = command.Process.Signal(syscall.SIGTERM)
		done := make(chan error, 1)
		go func() { done <- command.Wait() }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			if err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL); err != nil {
				qa.t.Error("cannot terminate isolated non-root Agent process group", err)
				return
			}
			<-done
		}
		_ = log.Close()
		_ = os.Remove(path)
	}
}

func (qa *runtimeUpgradeQA) request(id string) runtimepkg.ChannelRequest {
	qa.t.Helper()
	views, err := qa.store.RuntimeChannels(qa.t.Context())
	if err != nil {
		qa.t.Fatal(err)
	}
	for _, view := range views {
		for _, request := range view.Requests {
			if request.ID == id {
				return request
			}
		}
	}
	qa.t.Fatal("runtime request disappeared", id)
	return runtimepkg.ChannelRequest{}
}

func (qa *runtimeUpgradeQA) exchange() {
	qa.t.Helper()
	if err := qa.installer.Sync(qa.t.Context()); err != nil {
		qa.t.Fatal("installation channel failed", err)
	}
}

func (qa *runtimeUpgradeQA) report(result runtimepkg.ChannelResult) {
	qa.t.Helper()
	if _, err := qa.transport.RuntimeExchange(qa.t.Context(), runtimepkg.ChannelReport{Enrollment: qa.local.Enrollment, Result: &result}); err != nil {
		qa.t.Fatal(err)
	}
}

func (qa *runtimeUpgradeQA) save(name string, value any) {
	qa.t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		qa.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(qa.input.Evidence, name), raw, 0o600); err != nil {
		qa.t.Fatal(err)
	}
}

func upgradeBinding(target ipsentinel.Target, hello ipsentinel.Hello) ipsentinel.Binding {
	return ipsentinel.Binding{Target: target, ExecutorInstanceID: hello.Status.ExecutorInstanceID, DescriptorSHA256: hello.DescriptorSHA256, Epoch: hello.Status.Control.Epoch, Mode: ipsentinel.ModeManaged}
}

func upgradeWait(t *testing.T, timeout time.Duration, name string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("timed out waiting for", name)
}
