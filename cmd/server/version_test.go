package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func versionFixture(t *testing.T) (string, string) {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git unavailable")
	}
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "internal/model"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "internal/model/model.go"), []byte("package model\nvar Version = \"1.0.0-dev\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitRun := func(args ...string) {
		t.Helper()
		c := exec.Command(git, append([]string{"-C", repo}, args...)...)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
	}
	gitRun("init", "-q", "-b", "main")
	gitRun("add", ".")
	gitRun("-c", "user.name=Version test", "-c", "user.email=test@example.invalid", "commit", "-qm", "fixture")
	gitRun("tag", "v1.5.2")
	return repo, git
}

func TestSourceVersionTagDevelopmentAndArchive(t *testing.T) {
	repo, git := versionFixture(t)
	script, err := filepath.Abs("../../scripts/version.sh")
	if err != nil {
		t.Fatal(err)
	}
	resolve := func(path string, args ...string) string {
		t.Helper()
		c := exec.Command("sh", append([]string{script, path}, args...)...)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("version: %s %v", out, err)
		}
		return strings.TrimSpace(string(out))
	}
	if got := resolve(repo); got != "v1.5.2" {
		t.Fatalf("tag lost: %s", got)
	}
	model := filepath.Join(repo, "internal/model/model.go")
	if err := os.WriteFile(model, []byte("package model\nvar Version = \"1.6.0-dev\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c := exec.Command(git, "-C", repo, "-c", "user.name=Version test", "-c", "user.email=test@example.invalid", "commit", "-qam", "next version")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("commit: %s %v", out, err)
	}
	if got := resolve(repo); !strings.HasPrefix(got, "1.6.0-dev+") {
		t.Fatalf("development version: %s", got)
	}
	if got := resolve(repo, "override"); got != "1.6.0-dev+override" {
		t.Fatalf("SHA override: %s", got)
	}
	archive := t.TempDir()
	if err := os.MkdirAll(filepath.Join(archive, "internal/model"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archive, "internal/model/model.go"), []byte("package model\nvar Version = \"1.6.0-dev\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := resolve(archive); got != "1.6.0-dev" {
		t.Fatalf("archive version: %s", got)
	}
}

// Run the real source installer until its first install operation. Mocked
// commands prevent system changes and network access while recording the
// versions passed to all Server/Agent build invocations.
func TestSourceInstallerPreservesOldReleaseTag(t *testing.T) {
	repo, git := versionFixture(t)
	script, err := filepath.Abs("../../deploy/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "go-args")
	commands := map[string]string{
		"id":      "#!/bin/sh\nprintf '0\\n'\n",
		"cp":      "#!/bin/sh\nexit 0\n",
		"install": "#!/bin/sh\nexit 99\n",
		"go":      "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$TZA_VERSION_GO_ARGS\"\n",
		"git":     "#!/bin/sh\nif [ \"$1\" = clone ]; then exec \"$TZA_VERSION_REAL_GIT\" clone --quiet --branch \"$5\" \"$TZA_VERSION_REPO\" \"$7\"; fi\nexec \"$TZA_VERSION_REAL_GIT\" \"$@\"\n",
	}
	for name, text := range commands {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(text), 0700); err != nil {
			t.Fatal(err)
		}
	}
	c := exec.Command("sh", script, "--ref", "v1.5.2", "--no-systemd")
	c.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "TZA_VERSION_REAL_GIT="+git, "TZA_VERSION_REPO="+repo, "TZA_VERSION_GO_ARGS="+log)
	out, err := c.CombinedOutput()
	if e, ok := err.(*exec.ExitError); !ok || e.ExitCode() != 99 {
		t.Fatalf("installer did not stop at mocked install: %s %v", out, err)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	calls := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(calls) != 3 {
		t.Fatalf("expected Server and two Agent builds: %q", calls)
	}
	for _, call := range calls {
		if !strings.Contains(call, "internal/model.Version=v1.5.2") {
			t.Fatalf("tag lost by installer: %s", call)
		}
	}
}
