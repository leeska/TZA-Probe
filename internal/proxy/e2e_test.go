package proxy_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
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
	var statsOut []byte // the next `xray api statsquery -reset` answer
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
		if len(args) > 1 && args[0] == "api" && args[1] == "statsquery" {
			mu.Lock()
			defer mu.Unlock()
			out := statsOut
			statsOut = nil
			return out, nil
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
	// 流量：节点上读到的按人计数记到使用者本期用量上，订阅里带用量头。
	mu.Lock()
	statsOut = []byte(`{"stat":[{"name":"user>>>` + a.ID + `>>>traffic>>>uplink","value":1000},{"name":"user>>>` + a.ID + `>>>traffic>>>downlink","value":"5000"},{"name":"user>>>gone>>>traffic>>>downlink","value":7}]}`)
	mu.Unlock()
	call("POST", "/admin/proxy/sync", nil, &synced)
	var after users.View
	var list []users.View
	call("GET", "/admin/users", nil, &list)
	for _, u := range list {
		if u.ID == a.ID {
			after = u
		}
	}
	if after.TrafficUsed != 6000 || after.TrafficUp != 1000 || after.TrafficDown != 5000 {
		t.Fatalf("用量：%+v", after)
	}
	sub := func(query, agent string) (*http.Response, string) {
		req, _ := http.NewRequest("GET", server.URL+"/api/v1/sub/"+a.Token+query, nil)
		req.Header.Set("User-Agent", agent)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res, string(raw)
	}
	res, body := sub("", "mihomo/1.19")
	if res.StatusCode != 200 || !strings.Contains(body, `name: "日本"`) || !strings.Contains(body, a.ProxyUUID) || res.Header.Get("Subscription-Userinfo") != "upload=1000; download=5000; total=0" {
		t.Fatalf("Clash 订阅：%d %s %s", res.StatusCode, res.Header, body)
	}
	if res, body = sub("?format=base64", ""); res.StatusCode != 200 {
		t.Fatal(res.StatusCode)
	} else if raw, _ := base64.StdEncoding.DecodeString(body); !strings.HasPrefix(string(raw), "vless://"+a.ProxyUUID+"@jp.example.com:443?") {
		t.Fatalf("base64 订阅：%s", raw)
	}
	if res, _ = sub("", "SFA/1.12"); res.Header.Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatal("sing-box 客户端要拿到 JSON", res.Header)
	}
	// 规则模板：新建后设为默认，订阅里用它的规则；?template= 可以临时换成内置的全局代理。
	var tpl proxy.Template
	if code := call("POST", "/admin/proxy/templates", map[string]any{"name": "AI 走代理", "rules": "DOMAIN-SUFFIX,openai.com,代理\nGEOSITE,cn,DIRECT\nMATCH,DIRECT"}, &tpl); code != 201 {
		t.Fatal(code)
	}
	if code := call("POST", "/admin/proxy/templates", map[string]any{"name": "坏的", "rules": "FOO,bar,DIRECT"}, nil); code != 400 {
		t.Fatal("规则有错要拒绝", code)
	}
	if code := call("PUT", "/admin/proxy/templates/rule", map[string]any{"name": "改内置", "rules": "MATCH,代理"}, nil); code != 400 {
		t.Fatal("内置模板不能改", code)
	}
	call("PUT", "/admin/proxy/settings", map[string]any{"default_template": tpl.ID}, nil)
	if _, body = sub("?format=clash", ""); !strings.Contains(body, `- "DOMAIN-SUFFIX,openai.com,节点选择"`) || !strings.Contains(body, `- "MATCH,DIRECT"`) {
		t.Fatalf("默认模板：%s", body)
	}
	if _, body = sub("?format=clash&template=global", ""); strings.Contains(body, "openai") || !strings.Contains(body, `- "MATCH,节点选择"`) {
		t.Fatalf("指定模板：%s", body)
	}
	// 外部订阅：保存时立即拉取，节点带前缀并入订阅；只给指定的使用者。
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(base64.StdEncoding.EncodeToString([]byte("trojan://pw@x.example.com:443?sni=x.example.com#X1\ntuic://a@b:1#no"))))
	}))
	defer upstream.Close()
	var src struct {
		ID        string `json:"id"`
		NodeCount int    `json:"node_count"`
		Skipped   int    `json:"skipped"`
		Error     string `json:"error"`
	}
	if code := call("POST", "/admin/proxy/sources", map[string]any{"name": "机场", "url": upstream.URL, "enabled": true, "prefix": "外"}, &src); code != 201 || src.NodeCount != 1 || src.Skipped != 1 || src.Error != "" {
		t.Fatalf("外部订阅：%d %+v", code, src)
	}
	if _, body = sub("?format=clash", ""); !strings.Contains(body, `"name":"外 X1"`) {
		t.Fatalf("合并外部节点：%s", body)
	}
	call("PUT", "/admin/proxy/sources/"+src.ID, map[string]any{"name": "机场", "url": upstream.URL, "enabled": true, "prefix": "外", "user_ids": []string{b.ID}}, nil)
	if _, body = sub("?format=clash", ""); strings.Contains(body, "外 X1") {
		t.Fatal("没给这位使用者的外部节点不能出现")
	}
	// 用完额度的人在下一轮被移出配置；订阅照常能取，客户端据此显示用量。
	call("PUT", "/admin/users/"+a.ID, map[string]any{"name": "甲", "enabled": true, "traffic_limit": 4000}, nil)
	call("POST", "/admin/proxy/sync", nil, &synced)
	config, _ = os.ReadFile(filepath.Join(root, "etc/tza-proxy/xray.json"))
	if bytes.Contains(config, []byte(a.ProxyUUID)) {
		t.Fatal("超额的使用者要从配置里去掉")
	}
	if res, _ = sub("", "clash"); res.StatusCode != 200 {
		t.Fatal(res.StatusCode)
	}
	if res, _ = sub("", ""); res.Header.Get("Subscription-Userinfo") != "upload=1000; download=5000; total=4000" {
		t.Fatal(res.Header)
	}
	req, _ := http.NewRequest("GET", server.URL+"/api/v1/sub/"+strings.Repeat("0", 32), nil)
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != 404 {
		t.Fatal("未知 token 要 404")
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
