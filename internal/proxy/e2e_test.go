package proxy_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/agent"
	"github.com/leeska/TZA-Probe/internal/channel"
	"github.com/leeska/TZA-Probe/internal/httpapi"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/module"
	"github.com/leeska/TZA-Probe/internal/proxy"
	"github.com/leeska/TZA-Probe/internal/remote"
	"github.com/leeska/TZA-Probe/internal/storage"
	"github.com/leeska/TZA-Probe/internal/users"
)

// 主控经管理通道把内核与配置下发到节点：首次安装内核并应用配置；没有变化不重发；停用的使用者
// 从配置里去掉；删掉入站后节点上的服务被移除；分享链接只给能用这个入站的使用者。
func TestProxyOverChannel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	userMod, proxyMod, remoteMod := &users.Module{}, &proxy.Module{}, &remote.Module{}
	mods := []module.Module{remoteMod, userMod, proxyMod}
	store, err := storage.OpenWith(filepath.Join(t.TempDir(), "tza-probe.db"), module.Migrations(mods))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	node, token, err := store.SaveNode(ctx, model.Node{Name: "jp", Billing: model.Billing{ResetAt: "2026-11-01T00:00:00Z", TrafficMode: "sum"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	hub := &channel.Hub{}
	remoteMod.Hub, remoteMod.Nodes = hub, store
	userMod.Store = &users.Store{DB: store.DB()}
	cacheDir := t.TempDir()
	proxyMod.Store, proxyMod.Users, proxyMod.Hub, proxyMod.Nodes, proxyMod.Kernels = &proxy.Store{DB: store.DB()}, userMod.Store, hub, store, &proxy.Cache{Dir: cacheDir}
	proxyMod.Init()
	// 预先放好「已下载」的内核，测试不访问 GitHub。
	fake := []byte("#!/bin/sh\nexit 0\n")
	os.WriteFile(filepath.Join(cacheDir, fmt.Sprintf("xray-%s-%s", proxy.Kernels["xray"].Version, runtime.GOARCH)), fake, 0600)
	admin := model.Secret()
	server := httptest.NewServer((&httpapi.API{Store: store, AdminToken: admin, Assets: http.NotFoundHandler(), Modules: mods}).Handler())
	defer server.Close()
	call := func(method, path string, body any, out any) int {
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, server.URL+"/api/v1"+path, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+admin)
		req.Header.Set("Content-Type", "application/json")
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

	// 节点：管理通道上带 proxy 能力，systemd 与内核检查都用假的。
	root := t.TempDir()
	var mu sync.Mutex
	var commands []string
	host := &agent.ProxyHost{Root: root, Systemd: func() bool { return true }, Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		mu.Lock()
		commands = append(commands, name+" "+strings.Join(args, " "))
		mu.Unlock()
		if name == "systemctl" && args[0] == "is-active" {
			return []byte("active"), nil
		}
		if name == "id" {
			return []byte(fmt.Sprint(os.Getgid())), nil
		}
		return nil, nil
	}}
	transport, err := agent.NewTransport(server.URL, token, true)
	if err != nil {
		t.Fatal(err)
	}
	go (&agent.Channel{Transport: transport, Handlers: host.Handlers(), Capabilities: []string{channel.CapabilityProxy}}).Run(ctx)
	for deadline := time.Now().Add(10 * time.Second); !hub.Status(node).Connected; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("channel never connected")
		}
	}
	applies := func() int {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, c := range commands {
			if strings.HasPrefix(c, "systemctl restart tza-proxy-xray") {
				n++
			}
		}
		return n
	}

	var a, b users.View
	call("POST", "/admin/users", map[string]any{"name": "甲", "enabled": true}, &a)
	call("POST", "/admin/users", map[string]any{"name": "乙", "enabled": true}, &b)
	var in proxy.Inbound
	if code := call("POST", "/admin/proxy/inbounds", map[string]any{"node_id": node, "name": "日本", "kernel": "xray", "protocol": "vless-reality", "port": 443, "enabled": true, "address": "jp.example.com", "reality": map[string]any{"dest": "www.example.org:443"}}, &in); code != 201 {
		t.Fatal(code)
	}
	if code := call("POST", "/admin/proxy/inbounds", map[string]any{"node_id": node, "name": "撞端口", "kernel": "sing-box", "protocol": "ss2022", "port": 443, "enabled": true}, nil); code != 400 {
		t.Fatal("同一节点端口冲突要拒绝", code)
	}
	var synced struct{ Nodes []proxy.NodeView }
	call("POST", "/admin/proxy/sync", nil, &synced)
	if len(synced.Nodes) != 1 || synced.Nodes[0].Error != "" || !synced.Nodes[0].Kernels["xray"].Synced || !synced.Nodes[0].Kernels["xray"].Active {
		t.Fatalf("首次同步：%+v", synced.Nodes)
	}
	config, err := os.ReadFile(filepath.Join(root, "etc/tza-proxy/xray.json"))
	if err != nil || !bytes.Contains(config, []byte(a.ProxyUUID)) || !bytes.Contains(config, []byte(b.ProxyUUID)) {
		t.Fatalf("配置里要有两位使用者：%s", config)
	}
	if installed, err := os.ReadFile(filepath.Join(root, "usr/local/lib/tza-probe/kernels/xray", proxy.Kernels["xray"].Version, "xray")); err != nil || !bytes.Equal(installed, fake) {
		t.Fatal("内核要原样装到节点上", err)
	}
	if applies() != 1 {
		t.Fatal(applies())
	}
	call("POST", "/admin/proxy/sync", nil, &synced)
	if applies() != 1 {
		t.Fatal("没有变化不能重启服务")
	}
	call("PUT", "/admin/users/"+b.ID, map[string]any{"name": "乙", "enabled": false}, nil)
	call("POST", "/admin/proxy/sync", nil, &synced)
	config, _ = os.ReadFile(filepath.Join(root, "etc/tza-proxy/xray.json"))
	if bytes.Contains(config, []byte(b.ProxyUUID)) || !bytes.Contains(config, []byte(a.ProxyUUID)) || applies() != 2 {
		t.Fatalf("停用的使用者要去掉：%s", config)
	}
	var links []proxy.Link
	call("GET", "/admin/proxy/inbounds/"+in.ID+"/links", nil, &links)
	if len(links) != 1 || links[0].Name != "甲" || !strings.HasPrefix(links[0].Link, "vless://"+a.ProxyUUID+"@jp.example.com:443?") {
		t.Fatalf("链接：%+v", links)
	}
	call("DELETE", "/admin/proxy/inbounds/"+in.ID, nil, nil)
	call("POST", "/admin/proxy/sync", nil, &synced)
	if _, err := os.Stat(filepath.Join(root, "etc/tza-proxy/xray.json")); err == nil {
		t.Fatal("删掉入站后节点上的配置要移除")
	}
	if !strings.Contains(strings.Join(commands, "\n"), "systemctl disable --now tza-proxy-xray") {
		t.Fatal(commands)
	}
}
