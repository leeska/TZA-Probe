package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	runtimepkg "github.com/leeska/TZA-Probe/internal/executorruntime"
	"github.com/leeska/TZA-Probe/internal/ipsentinel"
	"github.com/leeska/TZA-Probe/pkg/tzapkg"
)

func TestPluginEnableLinuxIntegration(t *testing.T) {
	directory := os.Getenv("TZA_PLUGIN_ENABLE_LIVE_ROOT")
	if directory == "" {
		t.Skip("requires a disposable Linux host and the real signed runtime candidate")
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 || directory != "/opt/tza-enable-qa-20261107" {
		t.Fatal("requires the explicit disposable root environment")
	}
	for _, path := range []string{runtimepkg.DefaultInstallRoot, "/var/lib/ipsentinel/state/managed", "/etc/ipsentinel/runtime.toml", "/etc/ipsentinel/runtime-policy.toml", "/etc/systemd/system/ipsentinel-runtime.service"} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("refusing to replace an existing plugin installation: %s %v", path, err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	config := RuntimeInstallConfig{
		CCURL: "https://cc.example.invalid", CCKeysPath: filepath.Join(directory, "keys"),
		ProbeVersion: "1.10.7", StagingDir: filepath.Join(directory, "staging"),
		InstallRoot: runtimepkg.DefaultInstallRoot, TrustedInstaller: filepath.Join(directory, "reference.py"),
		StateDir: "/var/lib/ipsentinel/state/managed", ConfigPath: "/etc/ipsentinel/runtime.toml",
		PolicyPath: "/etc/ipsentinel/runtime-policy.toml", Unit: "ipsentinel-runtime.service",
		Target: ipsentinel.Target{ControllerID: ipsentinel.NewUUID(), NodeID: ipsentinel.NewUUID()},
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.CCKeysPath, []byte(tzapkg.FormatPublicKey("enable-qa-only", public)), 0o600); err != nil {
		t.Fatal(err)
	}
	local, err := ProvisionRuntime(config, FileRuntimePending{Directory: filepath.Join(config.StagingDir, "agent-pending")})
	if err != nil {
		t.Fatal(err)
	}
	archive, err := os.ReadFile(filepath.Join(directory, "candidate/archive.zip"))
	if err != nil {
		t.Fatal(err)
	}
	signature, _ := json.Marshal(tzapkg.Sign(private, "enable-qa-only", "ipsentinel-runtime", "0.4.0", archive))
	release, err := runtimepkg.Inspect(archive, signature, local.Keys, local.Enrollment.Host, config.Target)
	if err != nil {
		t.Fatal(err)
	}
	review := release.Review()
	approval := review.ApprovalTemplate
	approval.Approved = true
	approved, _ := json.Marshal(approval)
	store := runtimepkg.Store{Dir: config.StagingDir}
	if _, err := store.Stage(ctx, release, approved, filepath.Join(directory, "candidate/artifacts")); err != nil {
		t.Fatal(err)
	}
	installOptions := runtimepkg.OperationOptions{Action: "install", InstallRoot: config.InstallRoot, ReferencePath: config.TrustedInstaller}
	install, err := runtimepkg.PrepareOperation(ctx, store, review.PlanSHA256, local.Keys, local.Enrollment.Host, config.Target, installOptions)
	if err != nil {
		t.Fatal(err)
	}
	installApproval := install.ApprovalTemplate
	installApproval.Approved = true
	approved, _ = json.Marshal(installApproval)
	if _, err := runtimepkg.ApplyOperation(ctx, store, review.PlanSHA256, local.Keys, local.Enrollment.Host, config.Target, installOptions, approved); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := os.Lstat("/etc/systemd/system/" + config.Unit); err == nil {
			if err := pluginCommand(cleanup, "/usr/bin/systemctl", "disable", "--now", config.Unit); err != nil {
				t.Error("service cleanup failed", err)
			}
		}
		for _, path := range []string{"/etc/systemd/system/" + config.Unit, config.ConfigPath, config.PolicyPath} {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				t.Error("configuration cleanup failed", err)
			}
		}
		if err := pluginCommand(cleanup, "/usr/bin/systemctl", "daemon-reload"); err != nil {
			t.Error(err)
		}
		for _, path := range []string{config.StateDir, config.InstallRoot} {
			if err := os.Rename(path, filepath.Join(directory, "completed-"+filepath.Base(path)+"-"+time.Now().Format("150405"))); err != nil && !os.IsNotExist(err) {
				t.Error("failed to preserve QA evidence", err)
			}
		}
	}()
	settings := runtimepkg.EnableSettings{ExpectedIP: "23.246.165.231", Country: "US"}
	request := runtimepkg.ChannelRequest{ID: ipsentinel.NewUUID(), Enrollment: local.Enrollment, State: "requested",
		StagePlanSHA256: review.PlanSHA256, ChannelInput: runtimepkg.ChannelInput{Action: "enable", Version: "0.4.0", StageRequestID: ipsentinel.NewUUID(), Enable: &settings}}
	planned, err := local.Run(ctx, request)
	if err != nil || runtimepkg.ValidateChannelPlan(request, planned) != nil {
		t.Fatal("first-use planning failed", planned, err)
	}
	if _, err := os.Lstat(config.ConfigPath); !os.IsNotExist(err) {
		t.Fatal("planning changed plugin configuration", err)
	}
	request.State, request.Operation, request.PlanSHA256, request.ApprovedAt = "approved", planned.Operation, planned.PlanSHA256, time.Now().Unix()
	result, err := local.Run(ctx, request)
	if err != nil {
		raw, _ := os.ReadFile(filepath.Join(config.StagingDir, "agent-pending", "enable-error-"+request.ID))
		t.Fatalf("first-use startup failed: %v; local evidence: %s", err, raw)
	}
	if err := runtimepkg.ValidateChannelReceipt(request, result); err != nil {
		t.Fatal(err)
	}
	request.State = "unknown"
	recovered, err := local.Reconcile(ctx, request)
	if err != nil || recovered.PlanSHA256 != result.PlanSHA256 {
		t.Fatal("read-only receipt recovery failed", err)
	}
	client := ipsentinel.NewClient(ipsentinel.DefaultSocketPath, config.Target)
	defer client.Close()
	hello, err := client.Hello(ctx, "")
	if err != nil || runtimepkg.ValidateEnabledHello(hello, request.Version, settings, time.Now()) != nil {
		t.Fatal("live plugin not ready", hello.Status.Health, err)
	}
	if _, err := exec.CommandContext(ctx, "/usr/bin/systemctl", "is-active", "--quiet", config.Unit).Output(); err != nil {
		t.Fatal("plugin service is not active", err)
	}
	raw, _ := json.MarshalIndent(result, "", "  ")
	if err := os.WriteFile(filepath.Join(directory, "startup-receipt.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	request.State = "requested"
	if _, err := local.Run(ctx, request); err == nil {
		t.Fatal("first-use startup could run a second time")
	}
}
