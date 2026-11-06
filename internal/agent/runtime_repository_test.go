package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	runtimepkg "github.com/leeska/TZA-Probe/internal/executorruntime"
	"github.com/leeska/TZA-Probe/internal/ipsentinel"
)

func TestReviewedCCRuntimeApprovalChannel(t *testing.T) {
	address := os.Getenv("TZA_RUNTIME_REPOSITORY_URL")
	if address == "" {
		t.Skip("CC runtime fixture integration is not configured")
	}
	keysRaw, err := os.ReadFile(os.Getenv("TZA_RUNTIME_REPOSITORY_KEYS"))
	if err != nil {
		t.Fatal(err)
	}
	keys, err := parseRuntimeKeys(keysRaw)
	if err != nil {
		t.Fatal(err)
	}
	enrollment := runtimepkg.Enrollment{
		ScopeSHA256: strings.Repeat("a", 64),
		Target:      ipsentinel.Target{ControllerID: os.Getenv("TZA_RUNTIME_TARGET_CONTROLLER"), NodeID: os.Getenv("TZA_RUNTIME_TARGET_NODE")},
		Host:        runtimepkg.Host{Platform: os.Getenv("TZA_RUNTIME_HOST_PLATFORM"), ProbeVersion: os.Getenv("TZA_RUNTIME_PROBE_VERSION"), AgentVersion: os.Getenv("TZA_RUNTIME_AGENT_VERSION")},
	}
	local := &LocalRuntime{Config: RuntimeInstallConfig{CCURL: address, StagingDir: os.Getenv("TZA_RUNTIME_STAGING_DIR") + "-agent"}, Keys: keys, Enrollment: enrollment}
	request := runtimepkg.ChannelRequest{ID: ipsentinel.NewUUID(), ChannelInput: runtimepkg.ChannelInput{Action: "stage", Version: os.Getenv("TZA_RUNTIME_VERSION")}, Enrollment: enrollment, State: "requested"}
	review, err := local.Run(context.Background(), request)
	if err != nil || review.State != "awaiting_approval" || review.Review == nil {
		t.Fatal(review, err)
	}
	request.State = "approved"
	request.PlanSHA256 = review.PlanSHA256
	request.Review = review.Review
	if err := (FileRuntimePending{Directory: filepath.Join(t.TempDir(), "journal")}).Save(RuntimePending{Enrollment: enrollment, Result: review}); err != nil {
		t.Fatal(err)
	}
	receipt, err := local.Run(context.Background(), request)
	if err != nil || receipt.State != "staged" || receipt.Receipt == nil || receipt.PlanSHA256 != review.PlanSHA256 {
		t.Fatal(receipt, err)
	}
	staged, err := (runtimepkg.Store{Dir: filepath.Clean(local.Config.StagingDir)}).Verify(context.Background(), receipt.PlanSHA256, local.Keys, enrollment.Host, enrollment.Target)
	if err != nil || staged.PlanSHA256 != receipt.PlanSHA256 || staged.Activated {
		t.Fatal(staged, err)
	}
}
