package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/agentsig"
	"github.com/leeska/TZA-Probe/internal/model"
)

func digest(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// testKey is the operator key the tests pin; foreignKey stands for a key the
// Server (or anyone else) made up.
var testPub, testKey, _ = ed25519.GenerateKey(rand.Reader)
var _, foreignKey, _ = ed25519.GenerateKey(rand.Reader)

func pinned() map[string]ed25519.PublicKey { return map[string]ed25519.PublicKey{"ops": testPub} }

// signed is the upgrade a Server relays for binary, signed with key as version.
func signed(key ed25519.PrivateKey, binary []byte, version string) model.AgentUpgrade {
	sig := agentsig.Sign(key, "ops", runtime.GOARCH, version, digest(binary))
	return model.AgentUpgrade{Version: version, Path: "/dl/tza-agent-linux-amd64", SHA256: sig.SHA256, Arch: sig.Arch, KeyID: sig.KeyID, Sig: sig.Sig}
}

// stage puts binary into state as a downloaded upgrade with signature sig.
func stage(t *testing.T, state string, binary []byte, sig agentsig.Signature) {
	t.Helper()
	os.MkdirAll(state, 0o700)
	os.WriteFile(filepath.Join(state, stagedName), binary, 0o700)
	raw, _ := json.Marshal(sig)
	os.WriteFile(filepath.Join(state, stagedName+".sig"), raw, 0o600)
}

func testAgentVersion(t *testing.T) {
	t.Helper()
	previous := model.Version
	model.Version = "1.0.0"
	t.Cleanup(func() { model.Version = previous })
}

func newTestUpdater(t *testing.T, binary []byte) (*Updater, string) {
	t.Helper()
	testAgentVersion(t)
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
	return &Updater{Endpoint: server.URL, Executable: exe, StateDir: filepath.Join(t.TempDir(), "state"), Keys: pinned(), Client: server.Client()}, exe
}

func TestUpdaterReplacesBinary(t *testing.T) {
	next := []byte("#!/bin/sh\nexit 0\n")
	u, exe := newTestUpdater(t, next)
	old, _ := os.ReadFile(exe)
	if err := u.Update(context.Background(), signed(testKey, next, "1.1.0")); err != nil {
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
	if err := u.Update(context.Background(), signed(testKey, next, "1.1.0")); !errors.Is(err, ErrAlreadyCurrent) {
		t.Fatalf("same binary: %v", err)
	}
}

func TestUpdaterRefusesBadDownloads(t *testing.T) {
	next := []byte("#!/bin/sh\nexit 0\n")
	u, exe := newTestUpdater(t, next)
	old, _ := os.ReadFile(exe)
	good := signed(testKey, next, "1.1.0")
	withPath := func(path string) model.AgentUpgrade { up := good; up.Path = path; return up }
	otherArch := good
	otherArch.Arch = "mips"
	for name, up := range map[string]model.AgentUpgrade{
		"checksum":       signed(testKey, []byte("other"), "1.1.0"),
		"bad digest":     {Path: "/dl/tza-agent-linux-amd64", SHA256: "zz"},
		"foreign url":    withPath("//evil.example/x"),
		"traversal":      withPath("/dl/../api/v1/x"),
		"missing":        withPath("/dl/tza-agent-linux-arm64"),
		"unsigned":       {Version: "1.1.0", Path: "/dl/tza-agent-linux-amd64", SHA256: digest(next)},
		"foreign key":    signed(foreignKey, next, "1.1.0"),
		"older":          signed(testKey, next, "0.9.0"),
		"pre-release":    signed(testKey, next, "1.1.0-rc1"),
		"arch mismatch":  otherArch,
		"version edited": func() model.AgentUpgrade { up := good; up.Version = "9.9.9"; return up }(),
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
	if err := u2.Update(context.Background(), signed(testKey, broken, "1.1.0")); err == nil {
		t.Fatal("a binary that does not run must be refused")
	}
	if got, _ := os.ReadFile(exe2); string(got) == string(broken) {
		t.Fatal("broken binary installed")
	}
	// 节点上没钉公钥：什么都不收。
	u3, exe3 := newTestUpdater(t, next)
	u3.Keys = nil
	if err := u3.Update(context.Background(), good); err == nil {
		t.Fatal("an Agent without a pinned key must refuse upgrades")
	}
	if got, _ := os.ReadFile(exe3); string(got) == string(next) {
		t.Fatal("upgrade installed without a pinned key")
	}
}

func TestApplyStagedDiscardsTamperedFile(t *testing.T) {
	testAgentVersion(t)
	state, bin := t.TempDir(), t.TempDir()
	exe := filepath.Join(bin, "tza-agent")
	os.WriteFile(exe, []byte("old"), 0o755)
	if applied, err := ApplyStaged(state, exe, pinned()); applied || err != nil {
		t.Fatalf("nothing staged: %v %v", applied, err)
	}
	sign := func(key ed25519.PrivateKey, binary []byte, version string) agentsig.Signature {
		return agentsig.Sign(key, "ops", runtime.GOARCH, version, digest(binary))
	}
	// 以完整权限运行的 --apply-update 不信状态目录里的东西：内容被换、签名不对、没签名、
	// 版本更旧、或节点没钉公钥，都丢掉不装。
	for name, c := range map[string]struct {
		binary []byte
		sig    agentsig.Signature
		keys   map[string]ed25519.PublicKey
	}{
		"swapped binary": {[]byte("evil"), sign(testKey, []byte("new"), "1.1.0"), pinned()},
		"foreign key":    {[]byte("new"), sign(foreignKey, []byte("new"), "1.1.0"), pinned()},
		"unsigned":       {[]byte("new"), agentsig.Signature{SHA256: digest([]byte("new"))}, pinned()},
		"older":          {[]byte("new"), sign(testKey, []byte("new"), "0.1.0"), pinned()},
		"no pinned key":  {[]byte("new"), sign(testKey, []byte("new"), "1.1.0"), nil},
	} {
		stage(t, state, c.binary, c.sig)
		if applied, err := ApplyStaged(state, exe, c.keys); applied || err == nil {
			t.Fatalf("%s: staged binary must be refused", name)
		}
		if _, err := os.Stat(filepath.Join(state, stagedName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s: refused staged binary must be discarded", name)
		}
		if got, _ := os.ReadFile(exe); string(got) != "old" {
			t.Fatalf("%s: running binary must stay", name)
		}
	}
	stage(t, state, []byte("new"), sign(testKey, []byte("new"), "1.1.0"))
	if applied, err := ApplyStaged(state, exe, pinned()); !applied || err != nil {
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
