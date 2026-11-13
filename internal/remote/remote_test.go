package remote_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/leeska/TZA-Probe/internal/agent"
	"github.com/leeska/TZA-Probe/internal/channel"
	"github.com/leeska/TZA-Probe/internal/httpapi"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/module"
	"github.com/leeska/TZA-Probe/internal/remote"
	"github.com/leeska/TZA-Probe/internal/storage"
)

func TestShellOverChannel(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "tza-probe.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	node, token, err := store.SaveNode(ctx, model.Node{Name: "managed", Billing: model.Billing{ResetAt: "2026-11-01T00:00:00Z", TrafficMode: "sum"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	hub := &channel.Hub{}
	admin := model.Secret()
	server := httptest.NewServer((&httpapi.API{Store: store, AdminToken: admin, Assets: http.NotFoundHandler(), Modules: []module.Module{&remote.Module{Hub: hub, Nodes: store}}}).Handler())
	defer server.Close()

	call := func(method, path string, body any) *http.Response {
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, server.URL+path, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+admin)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", server.URL)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	// No channel yet: the command fails cleanly.
	if res := call("POST", "/api/v1/admin/remote/exec", map[string]any{"node_id": node, "command": "true"}); res.StatusCode != 502 {
		t.Fatalf("offline exec %d", res.StatusCode)
	}

	transport, err := agent.NewTransport(server.URL, token, true)
	if err != nil {
		t.Fatal(err)
	}
	go (&agent.Channel{Transport: transport, Handlers: agent.ShellHandlers(), Capabilities: []string{channel.CapabilityShell}}).Run(ctx)
	deadline := time.Now().Add(10 * time.Second)
	for !hub.Status(node).Connected {
		if time.Now().After(deadline) {
			t.Fatal("channel never connected")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if caps := hub.Status(node).Capabilities; len(caps) != 1 || caps[0] != "shell" {
		t.Fatalf("capabilities %v", caps)
	}

	res := call("POST", "/api/v1/admin/remote/exec", map[string]any{"node_id": node, "command": "echo hello; echo oops >&2; exit 3", "timeout_seconds": 10})
	var result remote.ExecResult
	json.NewDecoder(res.Body).Decode(&result)
	if res.StatusCode != 200 || result.ExitCode != 3 || !strings.Contains(result.Output, "hello") || !strings.Contains(result.Output, "oops") {
		t.Fatalf("exec %d %+v", res.StatusCode, result)
	}
	res = call("POST", "/api/v1/admin/remote/exec", map[string]any{"node_id": node, "command": "sleep 5", "timeout_seconds": 1})
	json.NewDecoder(res.Body).Decode(&result)
	if res.StatusCode != 200 || result.ExitCode == 0 || !strings.Contains(result.Error, "超时") {
		t.Fatalf("timeout %d %+v", res.StatusCode, result)
	}

	// The terminal needs a same-origin browser request.
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v1/admin/remote/terminal?node=" + node + "&cols=100&rows=30"
	header := http.Header{"Authorization": []string{"Bearer " + admin}}
	if _, resp, err := websocket.DefaultDialer.Dial(wsURL, header); err == nil || resp == nil || resp.StatusCode != 403 {
		t.Fatalf("cross-origin terminal allowed: %v", err)
	}
	header.Set("Origin", server.URL)
	term, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	term.WriteJSON(map[string]any{"i": "echo pty-$((40+2))\n"})
	got := ""
	term.SetReadDeadline(time.Now().Add(10 * time.Second))
	for !strings.Contains(got, "pty-42") {
		kind, data, err := term.ReadMessage()
		if err != nil {
			t.Fatalf("terminal read: %v (got %q)", err, got)
		}
		if kind == websocket.BinaryMessage {
			got += string(data)
		}
	}
	term.WriteJSON(map[string]any{"i": "exit 7\n"})
	for {
		kind, data, err := term.ReadMessage()
		if err != nil {
			t.Fatalf("no exit message: %v", err)
		}
		if kind == websocket.TextMessage {
			if !strings.Contains(string(data), `"exit":7`) {
				t.Fatalf("exit %s", data)
			}
			break
		}
	}

	time.Sleep(100 * time.Millisecond)
	entries, err := store.AuditLog(ctx, 0, 50, node)
	if err != nil {
		t.Fatal(err)
	}
	actions := []string{}
	for _, e := range entries {
		actions = append(actions, e.Action)
	}
	joined := strings.Join(actions, ",")
	if strings.Count(joined, "shell.exec") != 3 || !strings.Contains(joined, "shell.terminal.closed") || !strings.Contains(joined, "shell.terminal,") && !strings.HasSuffix(joined, "shell.terminal") {
		t.Fatalf("audit %v", actions)
	}
	for _, e := range entries {
		if e.Action == "shell.exec" && strings.Contains(e.Detail, "echo hello") && (e.Actor != "api-key" || !strings.Contains(e.Detail, "exit 3")) {
			t.Fatalf("exec audit %+v", e)
		}
		if strings.Contains(joined, "POST /api/v1/admin/remote/exec") {
			t.Fatal("generic audit entry not replaced")
		}
	}
}
