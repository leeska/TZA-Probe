package executorruntime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestInstallationInspectionLinux(t *testing.T) {
	directory := os.Getenv("TZA_RUNTIME_INSPECTION_QA")
	if directory == "" {
		t.Skip("TZA_RUNTIME_INSPECTION_QA not set")
	}
	root, err := os.MkdirTemp(directory, "inspection-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	lock, err := os.OpenFile(filepath.Join(root, ".install.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	plan := OperationPlan{FormatVersion: 1, Options: OperationOptions{Action: "install", InstallRoot: root, ReferencePath: referenceFixture(t)}, Version: "0.4.0", ArchiveSHA256: strings.Repeat("a", 64)}
	if result, err := InspectInstallation(t.Context(), plan); err != nil || result != nil {
		t.Fatal("absent installation was not resolved", result, err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectInstallation(t.Context(), plan); err == nil {
		t.Fatal("inspection bypassed an executing installer")
	}
	syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	os.Symlink("releases/"+strings.Repeat("b", 64), filepath.Join(root, "current"))
	if _, err := InspectInstallation(t.Context(), plan); err == nil {
		t.Fatal("changed selection was declared safe to retry")
	}
	os.Remove(filepath.Join(root, "current"))
	release := filepath.Join(root, "releases", plan.ArchiveSHA256)
	if err := os.MkdirAll(release, 0755); err != nil {
		t.Fatal(err)
	}
	write := func(name string, value any) string {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(release, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
		return hashBytes(raw)
	}
	runtimeHash := write("runtime.json", map[string]any{"version": "0.4.0"})
	hostHash := write("host-lock.json", map[string]any{"format_version": 1, "architecture": "x86_64", "distribution": "debian", "distribution_version": "13", "minimum_systemd": 1, "files": []any{}, "packages": []any{}})
	write("installed.json", map[string]any{"archive_sha256": plan.ArchiveSHA256, "inventory": []any{
		map[string]any{"path": "runtime.json", "sha256": runtimeHash, "mode": 0600},
		map[string]any{"path": "host-lock.json", "sha256": hostHash, "mode": 0600},
	}})
	if result, err := InspectInstallation(t.Context(), plan); err != nil || result == nil || result.State != "installed" {
		t.Fatal("complete inventory was not recovered", result, err)
	}
	os.WriteFile(filepath.Join(release, "runtime.json"), []byte("{}"), 0600)
	if _, err := InspectInstallation(t.Context(), plan); err == nil {
		t.Fatal("changed inventory was reported as installed")
	}
	t.Log("synthetic inventory only; no Python/Chromium runtime installed or executed")
}
