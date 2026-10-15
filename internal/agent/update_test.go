package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

func digest(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func newTestUpdater(t *testing.T, binary []byte) (*Updater, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the updater runs shell scripts as fake binaries")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/dl/tza-agent-linux-amd64" {
			http.NotFound(w, r)
			return
		}
		w.Write(binary)
	}))
	t.Cleanup(server.Close)
	bin := t.TempDir()
	exe := filepath.Join(bin, "tza-agent")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho old\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &Updater{Endpoint: server.URL, Executable: exe, StateDir: filepath.Join(t.TempDir(), "state"), Client: server.Client()}, exe
}

func TestUpdaterReplacesBinary(t *testing.T) {
	next := []byte("#!/bin/sh\nexit 0\n")
	u, exe := newTestUpdater(t, next)
	old, _ := os.ReadFile(exe)
	if err := u.Update(context.Background(), model.AgentUpgrade{Version: "2", Path: "/dl/tza-agent-linux-amd64", SHA256: digest(next)}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(exe); string(got) != string(next) {
		t.Fatalf("binary not replaced: %q", got)
	}
	if info, _ := os.Stat(exe); info.Mode().Perm() != 0o755 {
		t.Fatalf("mode: %v", info.Mode())
	}
	if got, _ := os.ReadFile(exe + ".previous"); string(got) != string(old) {
		t.Fatal("previous binary must be kept")
	}
	if _, err := os.Stat(filepath.Join(u.StateDir, stagedName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("applied upgrade must leave nothing staged")
	}
	// 同一个版本再点一次：什么都不做，也不重启。
	if err := u.Update(context.Background(), model.AgentUpgrade{Path: "/dl/tza-agent-linux-amd64", SHA256: digest(next)}); !errors.Is(err, ErrAlreadyCurrent) {
		t.Fatalf("same binary: %v", err)
	}
}

func TestUpdaterRefusesBadDownloads(t *testing.T) {
	next := []byte("#!/bin/sh\nexit 0\n")
	u, exe := newTestUpdater(t, next)
	old, _ := os.ReadFile(exe)
	for name, up := range map[string]model.AgentUpgrade{
		"checksum":    {Path: "/dl/tza-agent-linux-amd64", SHA256: digest([]byte("other"))},
		"bad digest":  {Path: "/dl/tza-agent-linux-amd64", SHA256: "zz"},
		"foreign url": {Path: "//evil.example/x", SHA256: digest(next)},
		"traversal":   {Path: "/dl/../api/v1/x", SHA256: digest(next)},
		"missing":     {Path: "/dl/tza-agent-linux-arm64", SHA256: digest(next)},
	} {
		if err := u.Update(context.Background(), up); err == nil {
			t.Fatalf("%s: must fail", name)
		}
		if got, _ := os.ReadFile(exe); string(got) != string(old) {
			t.Fatalf("%s: running binary must stay", name)
		}
	}
	// 跑不起来的二进制（架构不对、损坏）不换。
	broken := []byte("#!/bin/sh\nexit 1\n")
	u2, exe2 := newTestUpdater(t, broken)
	if err := u2.Update(context.Background(), model.AgentUpgrade{Path: "/dl/tza-agent-linux-amd64", SHA256: digest(broken)}); err == nil {
		t.Fatal("a binary that does not run must be refused")
	}
	if got, _ := os.ReadFile(exe2); string(got) == string(broken) {
		t.Fatal("broken binary installed")
	}
}

func TestApplyStagedDiscardsTamperedFile(t *testing.T) {
	state, bin := t.TempDir(), t.TempDir()
	exe := filepath.Join(bin, "tza-agent")
	os.WriteFile(exe, []byte("old"), 0o755)
	if applied, err := ApplyStaged(state, exe); applied || err != nil {
		t.Fatalf("nothing staged: %v %v", applied, err)
	}
	os.WriteFile(filepath.Join(state, stagedName), []byte("new"), 0o700)
	os.WriteFile(filepath.Join(state, stagedName+".sha256"), []byte(digest([]byte("other"))+"\n"), 0o600)
	if applied, err := ApplyStaged(state, exe); applied || err == nil {
		t.Fatal("tampered staged binary must be refused")
	}
	if _, err := os.Stat(filepath.Join(state, stagedName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("tampered staged binary must be discarded")
	}
	if got, _ := os.ReadFile(exe); string(got) != "old" {
		t.Fatal("running binary must stay")
	}
	os.WriteFile(filepath.Join(state, stagedName), []byte("new"), 0o700)
	os.WriteFile(filepath.Join(state, stagedName+".sha256"), []byte(digest([]byte("new"))+"\n"), 0o600)
	if applied, err := ApplyStaged(state, exe); !applied || err != nil {
		t.Fatalf("apply: %v %v", applied, err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "new" {
		t.Fatal("staged binary not installed")
	}
}

type upgradeTransport struct {
	fakeTransport
	mu      sync.Mutex
	reports [][]string
}

func (u *upgradeTransport) Report(_ context.Context, m model.Metrics) (model.ReportReply, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.reports = append(u.reports, m.Capabilities)
	return model.ReportReply{Upgrade: &model.AgentUpgrade{Version: "2"}}, nil
}

type recordingUpgrader struct{ calls chan model.AgentUpgrade }

func (r recordingUpgrader) Update(_ context.Context, up model.AgentUpgrade) error {
	r.calls <- up
	return nil
}

func TestAgentRestartsAfterUpgrade(t *testing.T) {
	transport := &upgradeTransport{}
	upgrader := recordingUpgrader{calls: make(chan model.AgentUpgrade, 4)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	restarted := false
	done := make(chan struct{})
	go func() {
		Agent{Collector: nopCollector{}, Prober: blockingProber{make(chan struct{})}, Transport: transport, Upgrader: upgrader, Restart: func() { restarted = true; cancel() }}.Run(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("agent did not restart after the upgrade")
	}
	if !restarted || len(upgrader.calls) != 1 {
		t.Fatalf("restarted=%v upgrades=%d", restarted, len(upgrader.calls))
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	caps := transport.reports[0]
	if len(caps) == 0 || caps[len(caps)-1] != model.CapabilitySelfUpdate {
		t.Fatalf("self-updating agent must report the capability: %v", caps)
	}
}
