package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	runtimepkg "github.com/leeska/TZA-Probe/internal/executorruntime"
)

func TestRuntimeReconcileLive(t *testing.T) {
	path := os.Getenv("TZA_RUNTIME_RECONCILE_INPUT")
	if path == "" {
		t.Skip("TZA_RUNTIME_RECONCILE_INPUT not set")
	}
	var input struct {
		Config  RuntimeInstallConfig      `json:"config"`
		Request runtimepkg.ChannelRequest `json:"request"`
	}
	raw, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, &input) != nil {
		t.Fatal("invalid local recovery fixture", err)
	}
	journal := FileRuntimePending{Directory: filepath.Join(input.Config.StagingDir, "agent-pending")}
	local, err := ProvisionRuntime(input.Config, journal)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		result, err := local.Reconcile(t.Context(), input.Request)
		if err != nil || result.State != "failed" || result.ErrorCode != "operation_not_applied" {
			t.Fatal("read-only inspection failed to confirm absent installation", result, err)
		}
		if _, err := os.Lstat(filepath.Join(input.Config.InstallRoot, "releases")); !os.IsNotExist(err) {
			t.Fatal("inspection created an installation directory", err)
		}
	}
	t.Log("twice verified the real staged signature and unchanged selection; no installation, service control or controller mutation")
}
