package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/leeska/TZA-Probe/internal/agent"
	runtimepkg "github.com/leeska/TZA-Probe/internal/executorruntime"
	"github.com/leeska/TZA-Probe/internal/ipsentinel"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/storage"
	"github.com/leeska/TZA-Probe/web"
)

type runtimeLiveWorker struct {
	local      *agent.LocalRuntime
	executions int
}

func (worker *runtimeLiveWorker) Run(ctx context.Context, request runtimepkg.ChannelRequest) (runtimepkg.ChannelResult, error) {
	if request.State == "approved" {
		worker.executions++
	}
	return worker.local.Run(ctx, request)
}

type runtimeLiveTransport struct {
	*agent.HTTPTransport
	dropState string
}

func (transport *runtimeLiveTransport) RuntimeExchange(ctx context.Context, report runtimepkg.ChannelReport) (runtimepkg.ChannelReply, error) {
	reply, err := transport.HTTPTransport.RuntimeExchange(ctx, report)
	if err == nil && report.Result != nil && report.Result.State == transport.dropState {
		transport.dropState = ""
		return runtimepkg.ChannelReply{}, errors.New("deliberately lost committed runtime HTTP receipt")
	}
	return reply, err
}

func TestInstalledRuntimeChannelLinux(t *testing.T) {
	name := os.Getenv("TZA_RUNTIME_LINUX_INPUT")
	if name == "" {
		t.Skip("set TZA_RUNTIME_LINUX_INPUT for a disposable privileged Debian 13 installation")
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Fatal("the installed runtime channel acceptance requires privileged Linux")
	}
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	var input struct {
		Config          agent.RuntimeInstallConfig `json:"config"`
		StagePlanSHA256 string                     `json:"stage_plan_sha256"`
		EvidenceDir     string                     `json:"evidence_dir"`
	}
	if ipsentinel.DecodeStrict(raw, &input) != nil {
		t.Fatal("invalid isolated installation input")
	}
	journal := agent.FileRuntimePending{Directory: filepath.Join(input.Config.StagingDir, "agent-pending")}
	local, err := agent.ProvisionRuntime(input.Config, journal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(input.Config.InstallRoot, "current")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("acceptance requires an unselected isolated installation root", err)
	}
	source, err := (runtimepkg.Store{Dir: input.Config.StagingDir}).Verify(t.Context(), input.StagePlanSHA256, local.Keys, local.Enrollment.Host, local.Enrollment.Target)
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	node, token, err := store.SaveNode(t.Context(), model.Node{Name: "isolated installation channel", Billing: model.Billing{ResetAt: "2026-11-01T00:00:00Z", TrafficMode: "sum"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer((&API{Store: store, AdminToken: model.Secret(), Assets: web.Handler()}).Handler())
	defer server.Close()
	httpTransport, err := agent.NewTransport(server.URL, token, false)
	if err != nil {
		t.Fatal(err)
	}
	transport := &runtimeLiveTransport{HTTPTransport: httpTransport}
	worker := &runtimeLiveWorker{local: local}
	installer := agent.RuntimeInstaller{Transport: transport, Worker: worker, Enrollment: local.Enrollment, Journal: journal}
	if err := installer.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	stage, err := store.CreateRuntimeRequest(t.Context(), node, runtimepkg.ChannelInput{Action: "stage", Version: source.Plan.Version})
	if err != nil {
		t.Fatal(err)
	}
	review := runtimepkg.Review{Plan: source.Plan, PlanSHA256: source.PlanSHA256, ApprovalTemplate: runtimepkg.Approval{FormatVersion: 1, PlanSHA256: source.PlanSHA256}}
	planned := runtimepkg.ChannelResult{RequestID: stage.ID, State: "awaiting_approval", PlanSHA256: source.PlanSHA256, Review: &review}
	if _, err := transport.RuntimeExchange(t.Context(), runtimepkg.ChannelReport{Enrollment: local.Enrollment, Result: &planned}); err != nil {
		t.Fatal(err)
	}
	if err := store.ApproveRuntimeRequest(t.Context(), node, stage.ID, runtimepkg.ChannelApproval{Approved: true, PlanSHA256: source.PlanSHA256}); err != nil {
		t.Fatal(err)
	}
	running := runtimepkg.ChannelResult{RequestID: stage.ID, State: "running", PlanSHA256: source.PlanSHA256}
	if _, err := transport.RuntimeExchange(t.Context(), runtimepkg.ChannelReport{Enrollment: local.Enrollment, Result: &running}); err != nil {
		t.Fatal(err)
	}
	staged := runtimepkg.ChannelResult{RequestID: stage.ID, State: "staged", PlanSHA256: source.PlanSHA256, Receipt: &source}
	if _, err := transport.RuntimeExchange(t.Context(), runtimepkg.ChannelReport{Enrollment: local.Enrollment, Result: &staged}); err != nil {
		t.Fatal(err)
	}
	for index, action := range []string{"install", "select"} {
		request, err := store.CreateRuntimeRequest(t.Context(), node, runtimepkg.ChannelInput{Action: action, Version: source.Plan.Version, StageRequestID: stage.ID})
		if err != nil {
			t.Fatal(err)
		}
		if err := installer.Sync(t.Context()); err != nil {
			t.Fatal("actual installation plan failed", err)
		}
		views, err := store.RuntimeChannels(t.Context())
		if err != nil || len(views) != 1 || views[0].Requests[0].ID != request.ID || views[0].Requests[0].State != "awaiting_approval" {
			t.Fatal(views, err)
		}
		planned := views[0].Requests[0]
		if err := store.ApproveRuntimeRequest(t.Context(), node, request.ID, runtimepkg.ChannelApproval{Approved: true, PlanSHA256: planned.PlanSHA256}); err != nil {
			t.Fatal(err)
		}
		state := "installed"
		if action == "select" {
			state = "selected"
		}
		transport.dropState = state
		if err := installer.Sync(t.Context()); err == nil {
			t.Fatal("lost installation receipt was not exposed")
		}
		pending, err := journal.Load()
		if err != nil || pending == nil || pending.Result.State != state || worker.executions != index+1 {
			t.Fatal("installation did not retain its completed receipt", pending, err)
		}
		if err := installer.Sync(t.Context()); err != nil {
			t.Fatal("completed receipt retry failed", err)
		}
		if worker.executions != index+1 {
			t.Fatal("receipt retry re-executed the installer")
		}
		views, err = store.RuntimeChannels(t.Context())
		if err != nil || views[0].Requests[0].State != state || views[0].Requests[0].OperationReceipt == nil || views[0].Requests[0].OperationReceipt.Activated {
			t.Fatal(views, err)
		}
		evidence, err := json.MarshalIndent(views[0].Requests[0], "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(input.EvidenceDir, action+"-channel-receipt.json"), evidence, 0o600); err != nil {
			t.Fatal(err)
		}
		if action == "install" {
			if _, err := os.Lstat(filepath.Join(input.Config.InstallRoot, "current")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("install selected a release without its own approval", err)
			}
		}
		t.Logf("%s confirmed through Agent HTTP, completed receipt replayed without re-execution", state)
	}
	selected, err := os.Readlink(filepath.Join(input.Config.InstallRoot, "current"))
	if err != nil || selected != "releases/"+source.Plan.ArchiveSHA256 {
		t.Fatal(selected, err)
	}
	status, err := exec.Command("systemctl", "show", "-p", "ActiveState", "--value", input.Config.Unit).Output()
	if err != nil || string(status) != "inactive\n" {
		t.Fatal("installation selected or started an unexpected service state", string(status), err)
	}
	t.Logf("real candidate %s; helper %s; executions=%d; service=inactive; activated=false", source.Plan.ArchiveSHA256, runtimepkg.ReferenceInstallerSHA256, worker.executions)
}
