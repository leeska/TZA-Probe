package executorruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func referenceFixture(t *testing.T) string {
	t.Helper()
	name := os.Getenv("TZA_IPSENTINEL_INSTALLER")
	if name == "" {
		t.Skip("TZA_IPSENTINEL_INSTALLER is not set for reference-installer integration")
	}
	raw, err := readLocalFile(name, 128<<10)
	if err != nil || hashBytes(raw) != ReferenceInstallerSHA256 {
		t.Fatal("reference installer does not match the pinned upstream revision", err)
	}
	return name
}

func operationFixture(t *testing.T, action string) (fixture, Store, string, OperationOptions) {
	t.Helper()
	data := newFixture(t)
	release := data.inspect(t)
	store := newStore(t)
	if _, err := store.Stage(context.Background(), release, approved(t, release), data.source); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "installed")
	options := OperationOptions{Action: action, InstallRoot: root, ReferencePath: referenceFixture(t)}
	if action == "select" {
		options.StateDir = t.TempDir()
		options.ConfigPath = filepath.Join(t.TempDir(), "config.toml")
		options.PolicyPath = filepath.Join(t.TempDir(), "policy.toml")
		options.Unit = "ipsentinel-runtime.service"
		os.WriteFile(options.ConfigPath, []byte("config"), 0o600)
		os.WriteFile(options.PolicyPath, []byte("policy"), 0o600)
	}
	return data, store, release.digest, options
}

func operationApproval(t *testing.T, review OperationReview) []byte {
	t.Helper()
	permission := review.ApprovalTemplate
	permission.Approved = true
	raw, err := json.Marshal(permission)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestOperationApprovalSeparatesInstallationSelectionAndStaging(t *testing.T) {
	for _, action := range []string{"install", "select"} {
		t.Run(action, func(t *testing.T) {
			data, store, digest, options := operationFixture(t, action)
			review, err := PrepareOperation(context.Background(), store, digest, data.keys, data.host, data.target, options)
			if err != nil {
				t.Fatal(err)
			}
			if review.ApprovalTemplate.Approved || review.Plan.InstallerSHA256 != ReferenceInstallerSHA256 || review.Plan.SigningKeySHA256 != hashBytes(data.keys["test-only"]) {
				t.Fatal(review)
			}
			if _, err := os.Stat(options.InstallRoot); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("operation planning wrote an installation directory", err)
			}
			calls := 0
			runner := func(_ context.Context, request referenceRequest) (referenceResult, error) {
				calls++
				if request.Plan.Target != data.target || request.Plan.StagePlanSHA256 != digest {
					t.Fatal(request)
				}
				disabled := false
				if action == "install" {
					return referenceResult{State: "installed", Selected: &disabled, Release: filepath.Join(options.InstallRoot, "releases", request.Plan.ArchiveSHA256)}, nil
				}
				return referenceResult{State: "selected", Activated: &disabled, Release: request.Plan.ArchiveSHA256, Backup: filepath.Join(options.InstallRoot, "backups", strings.Repeat("a", 32))}, nil
			}
			if _, err := applyOperation(context.Background(), store, digest, data.keys, data.host, data.target, options, approved(t, data.inspect(t)), runner); err == nil || calls != 0 {
				t.Fatal("staging approval granted installation or selection", err)
			}
			permission := operationApproval(t, review)
			result, err := applyOperation(context.Background(), store, digest, data.keys, data.host, data.target, options, permission, runner)
			if err != nil || result.Activated || calls != 1 || result.Selected != (action == "select") {
				t.Fatal(result, calls, err)
			}
		})
	}
}

func TestOperationApprovalCannotFollowChangedInputs(t *testing.T) {
	for _, change := range []string{"root", "config", "policy", "unit", "current", "selection-transaction", "same-pointer-new-transaction", "action", "helper"} {
		t.Run(change, func(t *testing.T) {
			data, store, digest, options := operationFixture(t, "select")
			review, err := PrepareOperation(context.Background(), store, digest, data.keys, data.host, data.target, options)
			if err != nil {
				t.Fatal(err)
			}
			permission := operationApproval(t, review)
			switch change {
			case "root":
				options.InstallRoot += "-other"
			case "config":
				os.WriteFile(options.ConfigPath, []byte("changed"), 0o600)
			case "policy":
				os.WriteFile(options.PolicyPath, []byte("changed"), 0o600)
			case "unit":
				options.Unit = "ipsentinel-other.service"
			case "current":
				os.Mkdir(options.InstallRoot, 0o700)
				os.Symlink("releases/"+strings.Repeat("a", 64), filepath.Join(options.InstallRoot, "current"))
			case "selection-transaction":
				os.Mkdir(options.InstallRoot, 0o700)
				os.WriteFile(filepath.Join(options.InstallRoot, "selection.json"), []byte(`{"state":"prepared"}`), 0o600)
			case "same-pointer-new-transaction":
				os.Mkdir(options.InstallRoot, 0o700)
				os.WriteFile(filepath.Join(options.InstallRoot, "selection.json"), []byte(`{"state":"committed","backup":"new-operation"}`), 0o600)
			case "action":
				options.Action = "install"
				options.ConfigPath, options.PolicyPath, options.StateDir, options.Unit = "", "", "", ""
			case "helper":
				options.ReferencePath = filepath.Join(t.TempDir(), "untrusted.py")
				os.WriteFile(options.ReferencePath, []byte("print('not trusted')"), 0o600)
			}
			called := false
			_, err = applyOperation(context.Background(), store, digest, data.keys, data.host, data.target, options, permission, func(context.Context, referenceRequest) (referenceResult, error) {
				called = true
				return referenceResult{}, nil
			})
			if err == nil || called {
				t.Fatal("changed local operation reused an approval", called, err)
			}
		})
	}
}

func TestOperationRejectsUnmanagedTargetsAndUnexpectedActivation(t *testing.T) {
	data, store, digest, options := operationFixture(t, "install")
	review, err := PrepareOperation(context.Background(), store, digest, data.keys, data.host, data.target, options)
	if err != nil {
		t.Fatal(err)
	}
	active := true
	_, err = applyOperation(context.Background(), store, digest, data.keys, data.host, data.target, options, operationApproval(t, review), func(context.Context, referenceRequest) (referenceResult, error) {
		return referenceResult{State: "installed", Selected: &active, Activated: &active}, nil
	})
	if err == nil {
		t.Fatal("unexpected selection or activation was represented as installation success")
	}
	for _, invalid := range []OperationOptions{
		{Action: "start"},
		{Action: "install", InstallRoot: "/"},
		{Action: "install", InstallRoot: "relative"},
		{Action: "select", Unit: "tza-agent.service"},
		{Action: "install", Unit: "ipsentinel-runtime.service"},
	} {
		if _, err := PrepareOperation(context.Background(), store, digest, data.keys, data.host, data.target, invalid); err == nil {
			t.Fatal("unsafe operation options accepted", invalid)
		}
	}
}

func TestReferenceRunnerRejectsUnprivilegedOrUntrustedHelpers(t *testing.T) {
	if os.Geteuid() != 0 {
		if _, err := runReferenceOperation(context.Background(), referenceRequest{}); err == nil {
			t.Fatal("unprivileged runtime operation accepted")
		}
	}
	if err := trustedFile(filepath.Join(t.TempDir(), "missing.py")); err == nil {
		t.Fatal("untrusted helper path accepted")
	}
	var output boundedOutput
	if _, err := output.Write(make([]byte, 65537)); err == nil || output.Len() != 0 {
		t.Fatal("oversized helper output was buffered")
	}
}
