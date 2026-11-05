package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/agent"
	"github.com/leeska/TZA-Probe/internal/ipsentinel"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/storage"
	"github.com/leeska/TZA-Probe/web"
)

func TestExecutorAPIAuthenticationAndBinding(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	node, token, err := store.SaveNode(ctx, model.Node{Name: "fixture", Billing: model.Billing{ResetAt: "2026-11-01T00:00:00Z", TrafficMode: "sum"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	admin := model.Secret()
	handler := (&API{Store: store, AdminToken: admin, Assets: web.Handler()}).Handler()
	binding := ipsentinel.Binding{Target: ipsentinel.Target{ControllerID: ipsentinel.NewUUID(), NodeID: ipsentinel.NewUUID()}, ExecutorInstanceID: ipsentinel.NewUUID(), DescriptorSHA256: string(bytes.Repeat([]byte("a"), 64)), Epoch: 1}
	body, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, key, origin string, body []byte) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, "http://localhost/api/v1"+path, bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		if key != "" {
			request.Header.Set("Authorization", "Bearer "+key)
		}
		if origin != "" {
			request.Header.Set("Origin", origin)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	path := "/admin/nodes/" + node + "/executors/ipsentinel"
	for _, key := range []string{"", token} {
		for _, method := range []string{"GET", "PUT", "DELETE"} {
			if result := call(method, path+"/schedule", key, "", []byte("{}")); result.Code != 401 {
				t.Fatal("schedule requires administrator", method, result.Code)
			}
		}
	}
	for _, method := range []string{"PUT", "DELETE"} {
		if result := call(method, path+"/schedule", admin, "https://untrusted.test", []byte("{}")); result.Code != 403 {
			t.Fatal("cross-origin schedule mutation", method, result.Code)
		}
	}
	if result := call("GET", path+"/schedule", admin, "", nil); result.Code != 200 {
		t.Fatal("empty schedule unreadable", result.Code)
	}
	for _, key := range []string{"", token} {
		if result := call("PUT", path, key, "", body); result.Code != 401 {
			t.Fatal("unauthorized binding", result.Code)
		}
	}
	if result := call("PUT", path, admin, "https://untrusted.test", body); result.Code != 403 {
		t.Fatal("cross-origin binding", result.Code)
	}
	if result := call("PUT", path, admin, "", body); result.Code != 204 {
		t.Fatal(result.Code, result.Body.String())
	}
	if result := call("GET", "/agent/executors/ipsentinel", token, "", nil); result.Code != 200 {
		t.Fatal(result.Code)
	}
	for _, key := range []string{"", token} {
		if result := call("GET", "/admin/executors", key, "", nil); result.Code != 401 {
			t.Fatal("private state exposed", result.Code)
		}
		if result := call("GET", path+"/records", key, "", nil); result.Code != 401 {
			t.Fatal("private history exposed", result.Code)
		}
		if result := call("GET", path+"/actions", key, "", nil); result.Code != 401 {
			t.Fatal("private commands exposed", result.Code)
		}
		if result := call("POST", path+"/actions", key, "", []byte(`{"kind":"control.pause"}`)); result.Code != 401 {
			t.Fatal("Agent authorized managed command", result.Code)
		}
	}
	if result := call("POST", path+"/actions", admin, "https://untrusted.test", []byte(`{"kind":"control.pause"}`)); result.Code != 403 {
		t.Fatal("cross-origin command accepted", result.Code)
	}
	if result := call("POST", path+"/actions", admin, "", []byte(`{"kind":"control.pause"}`)); result.Code != 409 {
		t.Fatal("read-only binding accepted managed command", result.Code, result.Body.String())
	}
	if result := call("POST", "/agent/executors/ipsentinel/action-results", token, "", []byte(`{"id":"not-a-lease","accepted":true}`)); result.Code != 400 {
		t.Fatal("invalid result accepted", result.Code)
	}
	duplicate := append([]byte(`{"epoch":2,`), body[1:]...)
	if result := call("PUT", path, admin, "", duplicate); result.Code != 400 {
		t.Fatal("duplicate field accepted", result.Code)
	}
	if result := call("POST", "/agent/executors/ipsentinel/records", token, "", []byte(`{}`)); result.Code != 400 {
		t.Fatal("invalid report accepted", result.Code)
	}
	if result := call("GET", path+"/records?after_seq=-1", admin, "", nil); result.Code != 400 {
		t.Fatal("invalid cursor accepted", result.Code)
	}
}

func TestPythonReadonlyUDSIntegration(t *testing.T) {
	root := os.Getenv("TZA_IPSENTINEL_ROOT")
	if root == "" {
		t.Skip("set TZA_IPSENTINEL_ROOT for live Python read-only runtime integration")
	}
	python := filepath.Join(root, ".venv/bin/python")
	directory, err := os.MkdirTemp("", "tza-python-uds-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	target := ipsentinel.Target{ControllerID: ipsentinel.NewUUID(), NodeID: ipsentinel.NewUUID()}
	const script = `
import json, os, sys
from pathlib import Path
from ipsentinel.config import Config
from ipsentinel.control_state import ControlState, canonical_hash
from ipsentinel.control_protocol import READONLY_DESCRIPTOR
from ipsentinel.control_server import UnixServer
directory = Path(sys.argv[1])
config = Config(node_id="tza-test", expected_ip="8.8.8.8", egress_url="https://api64.ipify.org", urls=("https://www.google.com",), allowed_origins=("https://api64.ipify.org","https://www.google.com"), state_dir=str(directory / "state"), page_adapters=("google_home",))
with ControlState(Path(config.state_dir)) as state:
    state.initialize(config.node_id)
    state.set_mode("managed_readonly",sys.argv[2],sys.argv[3])
    installation = state.installation()
    binding = {"target":state.target(),"executor_instance_id":installation["executor_instance_id"],"epoch":state.control()["epoch"],"descriptor_sha256":canonical_hash(READONLY_DESCRIPTOR)}
server = UnixServer(directory / "control.sock",config,{os.geteuid()})
with ControlState(Path(config.state_dir)) as state:
    binding["epoch"] = state.control()["epoch"]
print(json.dumps(binding),flush=True)
server.serve_forever(poll_interval=0.05)
`
	command := exec.CommandContext(ctx, python, "-u", "-c", script, directory, target.ControllerID, target.NodeID)
	command.Dir = root
	command.Env = append(os.Environ(), "PYTHONPATH="+filepath.Join(root, "src"))
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { command.Process.Kill(); command.Wait() })
	reader := bufio.NewReader(stdout)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("Python runtime startup: %v %s", err, stderr.String())
	}
	var binding ipsentinel.Binding
	if err := json.Unmarshal(line, &binding); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	node, token, err := store.SaveNode(ctx, model.Node{Name: "Python fixture", Billing: model.Billing{ResetAt: "2026-11-01T00:00:00Z", TrafficMode: "sum"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BindExecutor(ctx, node, binding); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer((&API{Store: store, AdminToken: model.Secret(), Assets: web.Handler()}).Handler())
	defer server.Close()
	transport, err := agent.NewTransport(server.URL, token, false)
	if err != nil {
		t.Fatal(err)
	}
	client := ipsentinel.NewClient(filepath.Join(directory, "control.sock"), target)
	defer client.Close()
	syncer := ipsentinel.Synchronizer{Reader: client, Transport: transport, Target: target}
	for range 2 {
		if err := syncer.Sync(ctx); err != nil {
			t.Fatal(err)
		}
		client.Close()
	}
	receipt, err := store.ExecutorCursor(ctx, node)
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.StatusGet(ctx, "")
	if err != nil || status.Journal.AckedSeq != receipt.ThroughSeq || receipt.ThroughSeq == 0 {
		t.Fatal(receipt, status, err)
	}
	views, err := store.Executors(ctx)
	if err != nil || len(views) != 1 || views[0].Hello == nil {
		t.Fatal(views, err)
	}
	response, err := server.Client().Get(server.URL + "/api/v1/admin/executors")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("anonymous executor state exposed")
	}
}
