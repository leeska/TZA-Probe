package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/account"
	"github.com/leeska/TZA-Probe/internal/agent"
	"github.com/leeska/TZA-Probe/internal/ipsentinel"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/storage"
	"github.com/leeska/TZA-Probe/web"
)

func TestPythonManagedAdmissionUDSIntegration(t *testing.T) {
	testPythonManagedAdmission(t, false)
}

func TestPythonScheduledAdmissionUDSIntegration(t *testing.T) {
	testPythonManagedAdmission(t, true)
}

func testPythonManagedAdmission(t *testing.T, scheduled bool) {
	t.Helper()
	root := os.Getenv("TZA_IPSENTINEL_ROOT")
	if root == "" {
		t.Skip("set TZA_IPSENTINEL_ROOT for real Python protocol and SQLite; worker is an admission-only fixture")
	}
	directory, err := os.MkdirTemp("/tmp", "tza-managed-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	qaDirectory := os.Getenv("TZA_EXECUTOR_BROWSER_QA")
	duration := 30 * time.Second
	if scheduled {
		duration = 90 * time.Second
	}
	if qaDirectory != "" {
		duration = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()
	target := ipsentinel.Target{ControllerID: ipsentinel.NewUUID(), NodeID: ipsentinel.NewUUID()}
	script, err := filepath.Abs("testdata/managed_executor.py")
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "contracts/ipsentinel-executor-v1/examples/config.json")
	command := exec.CommandContext(ctx, filepath.Join(root, ".venv/bin/python"), "-u", script, directory, target.ControllerID, target.NodeID, configPath)
	command.Dir = root
	command.Env = append(os.Environ(), "PYTHONPATH="+filepath.Join(root, "src"))
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { command.Process.Kill(); command.Wait() })
	line, err := bufio.NewReader(stdout).ReadBytes('\n')
	if err != nil {
		t.Fatalf("Python admission fixture startup: %v %s", err, stderr.String())
	}
	var binding ipsentinel.Binding
	if err := json.Unmarshal(line, &binding); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	node, token, err := store.SaveNode(ctx, model.Node{Name: "managed admission fixture", Billing: model.Billing{ResetAt: "2026-11-01T00:00:00Z", TrafficMode: "sum"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BindExecutor(ctx, node, binding); err != nil {
		t.Fatal(err)
	}
	accountDir := t.TempDir()
	username, password, _, err := account.Ensure(accountDir)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer((&API{Store: store, AdminToken: model.Secret(), AccountDir: accountDir, Assets: web.Handler()}).Handler())
	defer server.Close()
	transport, err := agent.NewTransport(server.URL, token, false)
	if err != nil {
		t.Fatal(err)
	}
	client := ipsentinel.NewClient(filepath.Join(directory, "control.sock"), target)
	defer client.Close()
	syncer := ipsentinel.Synchronizer{Reader: client, Transport: transport, Target: target}
	syncOnce := func() {
		t.Helper()
		if err := syncer.Sync(ctx); err != nil {
			t.Fatal(err)
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
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var config ipsentinel.Config
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	queue(ipsentinel.ExecutorAction{Kind: ipsentinel.MethodConfigValidate, Config: &config})
	queue(ipsentinel.ExecutorAction{Kind: ipsentinel.MethodConfigApply, Config: &config})
	status, err := client.StatusGet(ctx, "")
	if err != nil || status.Config == nil {
		t.Fatal(status, err)
	}
	now := time.Now().UTC()
	job := ipsentinel.ExecutorAction{Kind: ipsentinel.MethodRunSubmit, JobID: ipsentinel.NewUUID(), ConfigRevision: status.Config.Revision, ConfigSHA256: status.Config.SHA256, ServiceID: "google", Purpose: "observe", Phase: "baseline", NotBefore: now.Add(-time.Second).Format(time.RFC3339), ExpiresAt: now.Add(5 * time.Minute).Format(time.RFC3339), TimeoutMS: 30000}
	queue(job)
	first, err := client.RunGet(ctx, "", job.JobID)
	if err != nil || first.State != ipsentinel.StateAccepted {
		t.Fatal(first, err)
	}
	queue(job)
	second, err := client.RunGet(ctx, "", job.JobID)
	if err != nil || first.RunID != second.RunID {
		t.Fatal("logical job retransmission started another run", first, second, err)
	}
	queue(ipsentinel.ExecutorAction{Kind: ipsentinel.MethodRunCancel, JobID: job.JobID})
	deadline := time.Now().Add(3 * time.Second)
	for {
		terminal, err := client.RunGet(ctx, "", job.JobID)
		if err != nil {
			t.Fatal(err)
		}
		if terminal.Result != nil {
			if terminal.State != ipsentinel.StateCancelled {
				t.Fatal(terminal)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cancellation never reached terminal")
		}
		time.Sleep(20 * time.Millisecond)
	}
	syncOnce()
	queue(ipsentinel.ExecutorAction{Kind: ipsentinel.MethodControlPause})
	status, err = client.StatusGet(ctx, "")
	if err != nil || !status.Control.Paused {
		t.Fatal(status, err)
	}
	if err := store.CreateExecutorAction(ctx, node, ipsentinel.ExecutorAction{ID: ipsentinel.NewUUID(), Kind: ipsentinel.MethodControlResume, ExpectedPauseGeneration: status.Control.PauseGeneration + 1}); !errors.Is(err, model.ErrConflict) {
		t.Fatal("stale pause generation accepted", err)
	}
	queue(ipsentinel.ExecutorAction{Kind: ipsentinel.MethodControlResume, ExpectedPauseGeneration: status.Control.PauseGeneration})
	status, err = client.StatusGet(ctx, "")
	if err != nil || status.Control.Paused {
		t.Fatal(status, err)
	}
	receipt, err := store.ExecutorCursor(ctx, node)
	if err != nil || receipt.ThroughSeq == 0 || receipt.ThroughSeq != status.Journal.AckedSeq {
		t.Fatal("journal not durably acknowledged", receipt, status.Journal, err)
	}
	actions, err := store.ExecutorActions(ctx, node)
	if err != nil {
		t.Fatal(err)
	}
	var observedTerminal bool
	for _, action := range actions {
		if action.Status == "rejected" || action.Status == "leased" || action.Status == "pending" {
			t.Fatalf("unresolved action %+v", action)
		}
		if action.Kind == ipsentinel.MethodRunSubmit && action.Run != nil && action.Run.State == ipsentinel.StateCancelled {
			observedTerminal = true
		}
	}
	if !observedTerminal {
		t.Fatal("native cancelled result not projected")
	}
	pendingJob := ipsentinel.ExecutorAction{ID: ipsentinel.NewUUID(), Kind: ipsentinel.MethodRunSubmit, JobID: ipsentinel.NewUUID(), ConfigRevision: status.Config.Revision, ConfigSHA256: status.Config.SHA256, ServiceID: "google", Purpose: "observe", Phase: "baseline", NotBefore: time.Now().UTC().Add(-time.Second).Format(time.RFC3339), ExpiresAt: time.Now().UTC().Add(5 * time.Minute).Format(time.RFC3339), TimeoutMS: 30000}
	if err := store.CreateExecutorAction(ctx, node, pendingJob); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateExecutorAction(ctx, node, ipsentinel.ExecutorAction{ID: ipsentinel.NewUUID(), Kind: ipsentinel.MethodRunCancel, JobID: pendingJob.JobID}); err != nil {
		t.Fatal(err)
	}
	actions, err = store.ExecutorActions(ctx, node)
	if err != nil {
		t.Fatal(err)
	}
	for _, pending := range actions {
		if pending.ID == pendingJob.ID && pending.Status != "cancelled" {
			t.Fatal("never-leased job was not cancelled locally", pending)
		}
	}
	if scheduled {
		syncOnce()
		due := time.Now().UTC().Truncate(time.Minute).Add(time.Minute)
		input := ipsentinel.ScheduleInput{ScheduleSpec: ipsentinel.ScheduleSpec{
			Enabled: true, Timezone: "UTC", Config: *status.Config, ObserveServices: []string{"google"},
			Slots: []ipsentinel.ScheduleSlot{{Time: due.Format("15:04"), Phase: ipsentinel.PhaseFollowup}}, TimeoutMS: 30000,
		}}
		if err := store.SaveExecutorSchedule(ctx, node, input); err != nil {
			t.Fatal(err)
		}
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		var scheduledJob string
		for scheduledJob == "" {
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case now := <-ticker.C:
				if err := store.TickExecutorSchedules(ctx, now); err != nil {
					t.Fatal(err)
				}
				syncOnce()
				view, err := store.ExecutorSchedule(ctx, node)
				if err != nil {
					t.Fatal(err)
				}
				if len(view.Recent) > 0 {
					entry := view.Recent[0]
					if len(view.Recent) != 1 || entry.Status != "queued" || entry.ActionStatus != "accepted" {
						t.Fatal("scheduled admission not durably confirmed", view)
					}
					scheduledJob = entry.JobID
				}
			}
		}
		first, err := client.RunGet(ctx, "", scheduledJob)
		if err != nil || first.JobID != ipsentinel.ScheduledJobID(node, binding, due.Format("2006-01-02"), due.Format("15:04")) {
			t.Fatal("scheduled job identity changed in real protocol", first, err)
		}
		if err := store.TickExecutorSchedules(ctx, time.Now()); err != nil {
			t.Fatal(err)
		}
		syncOnce()
		again, err := client.RunGet(ctx, "", scheduledJob)
		if err != nil || again.RunID != first.RunID {
			t.Fatal("scheduled replay created another run", again, err)
		}
		queue(ipsentinel.ExecutorAction{Kind: ipsentinel.MethodRunCancel, JobID: scheduledJob})
		t.Log("central schedule reached real Python admission with stable job/run identity; admission-only worker, no Chromium execution")
	}
	t.Log("real Python protocol, SQLite, UDS and Go/HTTP lifecycle passed; injected admission-only worker, no browser or Linux isolation acceptance")
	if qaDirectory != "" {
		if err := os.MkdirAll(qaDirectory, 0700); err != nil {
			t.Fatal(err)
		}
		metadata, err := json.Marshal(map[string]string{"url": server.URL, "username": username, "password": password})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(qaDirectory, "browser-fixture.json"), metadata, 0600); err != nil {
			t.Fatal(err)
		}
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-ticker.C:
				if _, err := os.Stat(filepath.Join(qaDirectory, "done")); err == nil {
					return
				}
				if err := store.TickExecutorSchedules(ctx, time.Now()); err != nil {
					t.Log("fixture schedule unavailable")
				}
				if err := syncer.Sync(ctx); err != nil {
					t.Log("fixture synchronization unavailable")
				}
			}
		}
	}
}
