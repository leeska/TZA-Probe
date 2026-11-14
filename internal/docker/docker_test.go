package docker_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/leeska/TZA-ControlCenter/internal/agent"
	"github.com/leeska/TZA-ControlCenter/internal/channel"
	"github.com/leeska/TZA-ControlCenter/internal/docker"
	"github.com/leeska/TZA-ControlCenter/internal/httpapi"
	"github.com/leeska/TZA-ControlCenter/internal/model"
	"github.com/leeska/TZA-ControlCenter/internal/module"
	"github.com/leeska/TZA-ControlCenter/internal/remote"
	"github.com/leeska/TZA-ControlCenter/internal/storage"
)

// 后台经管理通道操作节点上的 Docker：列出容器、镜像与 Compose 项目，启停容器，保存 compose
// 文件，用 WebSocket 看日志；没开 Docker 能力的节点被拒绝，写操作进审计日志。
func TestDockerOverChannel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remoteMod := &remote.Module{}
	dockerMod := &docker.Module{Remote: remoteMod}
	mods := []module.Module{remoteMod, dockerMod}
	store, err := storage.OpenWith(filepath.Join(t.TempDir(), "tza-probe.db"), module.Migrations(mods))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	node, token, err := store.SaveNode(ctx, model.Node{Name: "box", Billing: model.Billing{ResetAt: "2026-11-01T00:00:00Z", TrafficMode: "sum"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	hub := &channel.Hub{}
	remoteMod.Hub, remoteMod.Nodes, dockerMod.Nodes = hub, store, store
	admin := model.Secret()
	server := httptest.NewServer((&httpapi.API{Store: store, AdminToken: admin, Assets: http.NotFoundHandler(), Modules: mods}).Handler())
	defer server.Close()
	call := func(method, path string, body any, out any) int {
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, server.URL+"/api/v1"+path, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+admin)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", server.URL)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if out != nil {
			json.NewDecoder(res.Body).Decode(out)
		}
		return res.StatusCode
	}
	if code := call("GET", "/admin/docker/nodes/"+node, nil, nil); code != 409 {
		t.Fatal("没开 Docker 能力的节点要拒绝", code)
	}

	// 假 Docker Engine 与假 docker 命令。
	dir, _ := os.MkdirTemp("/tmp", "tzad")
	defer os.RemoveAll(dir)
	ln, err := net.Listen("unix", filepath.Join(dir, "d.sock"))
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var seen []string
	engine := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/version":
			w.Write([]byte(`{"Version":"27.3.1","ApiVersion":"1.47"}`))
		case "/containers/json":
			w.Write([]byte(`[{"Id":"abc123","Names":["/web"],"Image":"nginx","State":"running","Status":"Up 2 hours","Labels":{}}]`))
		case "/images/json":
			w.Write([]byte(`[{"Id":"sha256:0123456789abcdef","RepoTags":["nginx:latest"],"Size":1000}]`))
		case "/containers/abc123/stop":
			w.WriteHeader(204)
		case "/containers/abc123/start":
			w.WriteHeader(304)
		case "/containers/abc123/json":
			w.Write([]byte(`{"Config":{"Tty":true}}`))
		case "/containers/abc123/logs":
			w.Write([]byte("line one\r\nline two\r\n"))
		default:
			w.WriteHeader(404)
			w.Write([]byte(`{"message":"not found"}`))
		}
	})}
	go engine.Serve(ln)
	defer engine.Close()
	root := t.TempDir()
	cli := filepath.Join(root, "docker")
	os.WriteFile(cli, []byte("#!/bin/sh\ncase \"$*\" in *version*) echo 2.29.7;; esac\nexit 0\n"), 0755)
	host := &agent.DockerHost{Socket: filepath.Join(dir, "d.sock"), CLI: cli, Root: filepath.Join(root, "compose")}

	transport, err := agent.NewTransport(server.URL, token, true)
	if err != nil {
		t.Fatal(err)
	}
	go (&agent.Channel{Transport: transport, Handlers: host.Handlers(), Capabilities: []string{channel.CapabilityDocker}}).Run(ctx)
	for deadline := time.Now().Add(10 * time.Second); !hub.Status(node).Connected; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("channel never connected")
		}
	}

	var nodes []docker.NodeEntry
	call("GET", "/admin/docker", nil, &nodes)
	if len(nodes) != 1 || !nodes[0].Capable || !nodes[0].Connected {
		t.Fatalf("%+v", nodes)
	}
	var ov docker.Overview
	if code := call("GET", "/admin/docker/nodes/"+node, nil, &ov); code != 200 || ov.Error != "" || !strings.Contains(string(ov.Containers), `"/web"`) || !strings.Contains(string(ov.Images), "nginx:latest") || ov.Compose.Compose != "2.29.7" {
		t.Fatalf("概况：%d %+v", code, ov)
	}
	if code := call("POST", "/admin/docker/nodes/"+node+"/containers/abc123/stop", nil, nil); code != 200 {
		t.Fatal("停止", code)
	}
	if code := call("POST", "/admin/docker/nodes/"+node+"/containers/abc123/start", nil, nil); code != 200 {
		t.Fatal("已在运行（304）也算成功", code)
	}
	if code := call("POST", "/admin/docker/nodes/"+node+"/containers/abc123/kill", nil, nil); code != 400 {
		t.Fatal("只允许 start/stop/restart", code)
	}
	var failed map[string]string
	if code := call("DELETE", "/admin/docker/nodes/"+node+"/containers/zzz", nil, &failed); code != 404 || failed["error"] != "not found" {
		t.Fatal("Docker 的错误要原样带回", code, failed)
	}
	if code := call("PUT", "/admin/docker/nodes/"+node+"/compose/blog", map[string]string{"content": "services:\n  a:\n    image: nginx\n"}, nil); code != 200 {
		t.Fatal("保存 compose", code)
	}
	if saved, err := os.ReadFile(filepath.Join(root, "compose/blog/compose.yaml")); err != nil || !strings.Contains(string(saved), "image: nginx") {
		t.Fatal(err)
	}
	if code := call("PUT", "/admin/docker/nodes/"+node+"/compose/Blog!", map[string]string{"content": "x"}, nil); code != 400 {
		t.Fatal("项目名要校验", code)
	}

	// 日志：WebSocket 流，输出是二进制帧，结束是一条文本帧。
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v1/admin/docker/stream?kind=logs&container=abc123&tail=20&node=" + node
	header := http.Header{"Authorization": {"Bearer " + admin}, "Origin": {server.URL}}
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatal(err)
	}
	var logs strings.Builder
	for {
		kind, data, err := ws.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		if kind == websocket.BinaryMessage {
			logs.Write(data)
			continue
		}
		if !strings.Contains(string(data), `"exit":0`) {
			t.Fatal(string(data))
		}
		break
	}
	ws.Close()
	if logs.String() != "line one\r\nline two\r\n" {
		t.Fatalf("%q", logs.String())
	}
	if _, res, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Authorization": {"Bearer " + admin}, "Origin": {"https://evil.example"}}); err == nil || res.StatusCode != 403 {
		t.Fatal("别的来源不能开流")
	}
	if _, res, _ := websocket.DefaultDialer.Dial(strings.Replace(wsURL, "kind=logs", "kind=build", 1), header); res == nil || res.StatusCode != 400 {
		t.Fatal("未知的流要拒绝")
	}

	// 删除面板建的项目：down 之后目录一起删掉。
	del, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/v1/admin/docker/stream?kind=compose&action=delete&project=blog&node="+node, header)
	if err != nil {
		t.Fatal(err)
	}
	for {
		kind, data, err := del.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		if kind == websocket.TextMessage {
			if !strings.Contains(string(data), `"exit":0`) {
				t.Fatal(string(data))
			}
			break
		}
	}
	del.Close()
	if _, err := os.Stat(filepath.Join(root, "compose/blog")); !os.IsNotExist(err) {
		t.Fatal("删除项目后目录要删掉", err)
	}

	var audit []model.AuditEntry
	call("GET", "/admin/audit?limit=50", nil, &audit)
	actions := []string{}
	for _, e := range audit {
		actions = append(actions, e.Action)
	}
	for _, want := range []string{"docker.container.stop", "docker.container.remove", "docker.compose.write", "docker.logs", "docker.compose.delete"} {
		if !strings.Contains(strings.Join(actions, ","), want) {
			t.Errorf("审计日志缺 %s：%v", want, actions)
		}
	}
}
