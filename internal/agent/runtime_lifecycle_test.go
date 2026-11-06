package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	runtimepkg "github.com/leeska/TZA-Probe/internal/executorruntime"
	"github.com/leeska/TZA-Probe/internal/ipsentinel"
)

type lifecycleTransport struct {
	request runtimepkg.ChannelRequest
	drop    bool
	final   string
}

func (transport *lifecycleTransport) RuntimeExchange(_ context.Context, report runtimepkg.ChannelReport) (runtimepkg.ChannelReply, error) {
	if result := report.Result; result != nil {
		switch result.State {
		case "running":
			transport.request.State = "running"
		case "started_paused", "unknown":
			transport.final = result.State
			if transport.drop {
				transport.drop = false
				return runtimepkg.ChannelReply{}, errors.New("lost committed lifecycle receipt")
			}
			return runtimepkg.ChannelReply{}, nil
		}
	}
	if transport.final != "" {
		return runtimepkg.ChannelReply{}, nil
	}
	return runtimepkg.ChannelReply{Request: &transport.request}, nil
}

type lifecycleWorker struct {
	review     runtimepkg.LifecycleReview
	hello      ipsentinel.Hello
	executions int
}

func (worker *lifecycleWorker) Run(_ context.Context, request runtimepkg.ChannelRequest) (runtimepkg.ChannelResult, error) {
	if request.State == "requested" {
		return runtimepkg.ChannelResult{RequestID: request.ID, State: "awaiting_approval", PlanSHA256: worker.review.PlanSHA256, Lifecycle: &worker.review}, nil
	}
	worker.executions++
	return runtimepkg.ChannelResult{RequestID: request.ID, State: "started_paused", PlanSHA256: worker.review.PlanSHA256,
		LifecycleReceipt: &runtimepkg.LifecycleReceipt{FormatVersion: 1, State: "started_paused", PlanSHA256: worker.review.PlanSHA256, Plan: worker.review.Plan, Hello: worker.hello}}, nil
}

func TestLifecycleInstallerLostReceiptAndInterruptedStart(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		t.Run(map[bool]string{false: "lost_terminal_receipt", true: "interrupted_after_start_marker"}[interrupted], func(t *testing.T) {
			base, _, _ := runtimeInstallerFixture(t)
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
			hello.RuntimeVersion = "0.4.0"
			hello.Status.Control.Paused = true
			hello.Status.Control.PauseGeneration = 1
			reason := "operator"
			hello.Status.Control.PauseReason = &reason
			hello.Status.Control.Epoch = 2
			hello.Status.Journal = ipsentinel.JournalStatus{EarliestSeq: 1, LatestSeq: 5, AckedSeq: 4}
			value := strings.Repeat("a", 64)
			plan := runtimepkg.LifecyclePlan{
				Operation: runtimepkg.OperationPlan{FormatVersion: 1, Target: base.Enrollment.Target, StagePlanSHA256: value, ArchiveSHA256: value,
					Version: "0.4.0", InstallerSHA256: runtimepkg.ReferenceInstallerSHA256, SigningKeyID: "test", SigningKeySHA256: value,
					ExpectedCurrent: "releases/" + value, ConfigSHA256: value, PolicySHA256: value,
					Options: runtimepkg.OperationOptions{Action: "activate", InstallRoot: "/opt/ipsentinel-runtime", ReferencePath: runtimepkg.DefaultReferenceInstaller,
						StateDir: "/var/lib/ipsentinel/managed", ConfigPath: "/etc/ipsentinel/config.toml", PolicyPath: "/etc/ipsentinel/policy.toml", Unit: "ipsentinel-runtime.service"}},
				Peer:       runtimepkg.RuntimePeer{SocketPath: "/run/ipsentinel/control.sock", UID: 1001, GID: 1001},
				Before:     runtimepkg.RuntimeIdentity{InstanceID: hello.Status.ExecutorInstanceID, Epoch: 1, DescriptorSHA256: value, RuntimeVersion: "0.3.0", DatabaseVersion: 2, Config: *hello.Status.Config},
				UnitSHA256: value, CheckpointHash: value, Journal: &ipsentinel.JournalStatus{EarliestSeq: 1, LatestSeq: 4, AckedSeq: 4},
			}
			digest, _ := ipsentinel.CanonicalSHA256(plan)
			review := runtimepkg.LifecycleReview{Plan: plan, PlanSHA256: digest, ApprovalTemplate: runtimepkg.OperationApproval{FormatVersion: 1, Action: "activate", PlanSHA256: digest}}
			request := runtimepkg.ChannelRequest{ID: ipsentinel.NewUUID(), ChannelInput: runtimepkg.ChannelInput{Action: "activate", Version: "0.4.0", StageRequestID: ipsentinel.NewUUID()},
				Enrollment: base.Enrollment, State: "approved", ApprovedAt: time.Now().Unix(), PlanSHA256: digest, StagePlanSHA256: value, Lifecycle: &review}
			transport := &lifecycleTransport{request: request, drop: true}
			worker := &lifecycleWorker{review: review, hello: hello}
			journal := FileRuntimePending{Directory: filepath.Join(t.TempDir(), "lifecycle")}
			installer := RuntimeInstaller{Transport: transport, Worker: worker, Enrollment: base.Enrollment, Journal: journal}
			if interrupted {
				transport.request.State, transport.drop = "running", false
				if err := journal.Save(RuntimePending{Enrollment: base.Enrollment, Result: runtimepkg.ChannelResult{RequestID: request.ID, State: "running", PlanSHA256: digest}}); err != nil {
					t.Fatal(err)
				}
			}
			err = installer.Sync(t.Context())
			if interrupted && (err != nil || worker.executions != 0 || transport.final != "unknown") {
				t.Fatal("interrupted startup was executed again", err, worker.executions, transport.final)
			}
			if !interrupted && (err == nil || worker.executions != 1 || transport.final != "started_paused") {
				t.Fatal("startup did not persist its lost terminal receipt", err, worker.executions, transport.final)
			}
			if err := installer.Sync(t.Context()); err != nil {
				t.Fatal(err)
			}
			if worker.executions > 1 {
				t.Fatal("receipt retry repeated service startup")
			}
		})
	}
}
