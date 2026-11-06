package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/ipsentinel"
	"github.com/leeska/TZA-Probe/internal/model"
)

func TestPluginBootstrapPinsAssetsAndWorksOffline(t *testing.T) {
	reference, err := os.ReadFile("../agentdist/bin/ipsentinel-install-runtime.py")
	if err != nil {
		t.Fatal(err)
	}
	public, _, _ := ed25519.GenerateKey(nil)
	var calls atomic.Int32
	var tamper atomic.Bool
	var invalidIdentity atomic.Bool
	var invalidVersion atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		switch request.URL.Path {
		case "/api/v1/install/identity":
			var body map[string]string
			if json.NewDecoder(request.Body).Decode(&body) != nil || body["token"] != request.Header.Get("Authorization")[7:] {
				t.Error("missing node authentication")
			}
			node := ipsentinel.NewUUID()
			if invalidIdentity.Load() {
				node = "invalid;node"
			}
			version := "1.10.4"
			if invalidVersion.Load() {
				version = ""
			}
			json.NewEncoder(writer).Encode(map[string]string{
				"controller_id": ipsentinel.NewUUID(), "node_id": node,
				"cc_url": "https://cc.example.com", "probe_version": version,
			})
		case "/dl/runtime-cc-keys":
			writer.Write([]byte("test:" + base64.StdEncoding.EncodeToString(public)))
		case "/dl/ipsentinel-install-runtime.py":
			if tamper.Load() {
				writer.Write([]byte("modified"))
			} else {
				writer.Write(reference)
			}
		default:
			t.Error("bootstrap requested a runtime dependency", request.URL.Path)
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	transport, err := NewTransport(server.URL, model.Secret(), false)
	if err != nil {
		t.Fatal(err)
	}
	plugins := Plugins{Transport: transport, Directory: t.TempDir()}
	config, err := plugins.bootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 || !ipsentinel.ValidUUID(config.Target.NodeID) {
		t.Fatalf("unexpected bootstrap: calls=%d config=%+v", calls.Load(), config)
	}
	for _, path := range []string{config.CCKeysPath, config.TrustedInstaller, filepath.Join(filepath.Dir(config.CCKeysPath), "config.json")} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("private state: %s %v", path, err)
		}
	}
	tamper.Store(true)
	retry, err := plugins.bootstrap(context.Background())
	if err != nil || *retry != *config || calls.Load() != 3 {
		t.Fatal("restart changed pinned identity or fetched new trust", err)
	}
	plugins.Directory = t.TempDir()
	if _, err := plugins.bootstrap(context.Background()); err == nil {
		t.Fatal("accepted modified installer")
	}
	invalidIdentity.Store(true)
	plugins.Directory = t.TempDir()
	before := calls.Load()
	if _, err := plugins.bootstrap(context.Background()); err == nil || calls.Load() != before+1 {
		t.Fatal("invalid identity did not stop setup")
	}
	invalidIdentity.Store(false)
	invalidVersion.Store(true)
	plugins.Directory = t.TempDir()
	before = calls.Load()
	if _, err := plugins.bootstrap(context.Background()); err == nil || calls.Load() != before+1 {
		t.Fatal("invalid version did not stop setup before caching")
	}
	server.Close()
	plugins.Directory = filepath.Dir(filepath.Dir(config.CCKeysPath))
	if _, err := plugins.bootstrap(context.Background()); err != nil {
		t.Fatal("cached configuration unavailable offline", err)
	}
	transport.token = model.Secret()
	if _, err := plugins.bootstrap(context.Background()); err == nil {
		t.Fatal("rebound node reused old plugin identity")
	}
}

type blockingPlugins struct {
	started chan struct{}
	stopped chan struct{}
}

func (plugins blockingPlugins) Run(ctx context.Context) {
	close(plugins.started)
	<-ctx.Done()
	time.Sleep(20 * time.Millisecond)
	close(plugins.stopped)
}

func TestIntegratedPluginsDoNotBlockProbesAndJoinOnStop(t *testing.T) {
	transport := &fakeTransport{finished: map[string]bool{}, lease: []model.Job{{ID: "tcp", Task: model.Probe{Kind: "tcp"}}}}
	plugins := blockingPlugins{started: make(chan struct{}), stopped: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		Agent{Collector: nopCollector{}, Prober: blockingProber{}, Transport: transport, Plugins: plugins}.Run(ctx)
		close(done)
	}()
	select {
	case <-plugins.started:
	case <-time.After(time.Second):
		t.Fatal("integrated plugins did not start")
	}
	deadline := time.Now().Add(time.Second)
	for {
		_, finished := transport.snapshot()
		if finished == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("plugin operation blocked normal probes")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
		select {
		case <-plugins.stopped:
		default:
			t.Fatal("Agent exited without joining plugin worker")
		}
	case <-time.After(time.Second):
		t.Fatal("Agent failed to stop plugins")
	}
}
