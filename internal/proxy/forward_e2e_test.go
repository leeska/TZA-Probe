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
	"slices"
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

// fakeNode is a node on the management channel with fake systemd and kernel.
type fakeNode struct {
	id    string
	root  string
	mu    sync.Mutex
	stats []byte // the next `xray api statsquery -reset` answer
}

func (n *fakeNode) setStats(raw string) {
	n.mu.Lock()
	n.stats = []byte(raw)
	n.mu.Unlock()
}

func (n *fakeNode) config(t *testing.T) map[string]any {
	raw, err := os.ReadFile(filepath.Join(n.root, "etc/tza-proxy/xray.json"))
	if err != nil {
		return nil
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func (n *fakeNode) unit() string {
	raw, _ := os.ReadFile(filepath.Join(n.root, "etc/systemd/system/tza-proxy-xray.service"))
	return string(raw)
}

func inboundTags(doc map[string]any) []string {
	tags := []string{}
	list, _ := doc["inbounds"].([]any)
	for _, in := range list {
		tags = append(tags, in.(map[string]any)["tag"].(string))
	}
	slices.Sort(tags)
	return tags
}

// 端口转发与隧道：入口节点开转发入站，隧道再经加密出站连到出口节点上这条规则专用的端口；
// 流量按规则累计并计入它的使用者，使用者用完额度后规则自动停掉；端口和入站共用冲突检查。
func TestForwardsOverChannel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	userMod, proxyMod, remoteMod := &users.Module{}, &proxy.Module{}, &remote.Module{}
	mods := []module.Module{remoteMod, userMod, proxyMod}
	store, err := storage.OpenWith(filepath.Join(t.TempDir(), "tza-probe.db"), module.Migrations(mods))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	hub := &channel.Hub{}
	remoteMod.Hub, remoteMod.Nodes = hub, store
	userMod.Store = &users.Store{DB: store.DB()}
	cacheDir := t.TempDir()
	proxyMod.Store, proxyMod.Users, proxyMod.Hub, proxyMod.Nodes, proxyMod.Kernels = &proxy.Store{DB: store.DB()}, userMod.Store, hub, store, &proxy.Cache{Dir: cacheDir}
	proxyMod.Init()
	os.WriteFile(filepath.Join(cacheDir, fmt.Sprintf("xray-%s-%s", proxy.Kernels["xray"].Version, runtime.GOARCH)), []byte("#!/bin/sh\nexit 0\n"), 0600)
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
	start := func(name string) *fakeNode {
		id, token, err := store.SaveNode(ctx, model.Node{Name: name, Billing: model.Billing{ResetAt: "2026-11-01T00:00:00Z", TrafficMode: "sum"}}, true)
		if err != nil {
			t.Fatal(err)
		}
		n := &fakeNode{id: id, root: t.TempDir()}
		host := &agent.ProxyHost{Root: n.root, Systemd: func() bool { return true }, Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			switch {
			case name == "systemctl" && args[0] == "is-active":
				return []byte("active"), nil
			case name == "id":
				return []byte(fmt.Sprint(os.Getgid())), nil
			case len(args) > 1 && args[0] == "api" && args[1] == "statsquery":
				n.mu.Lock()
				defer n.mu.Unlock()
				out := n.stats
				n.stats = nil
				return out, nil
			}
			return nil, nil
		}}
		transport, err := agent.NewTransport(server.URL, token, true)
		if err != nil {
			t.Fatal(err)
		}
		go (&agent.Channel{Transport: transport, Handlers: host.Handlers(), Capabilities: []string{channel.CapabilityProxy}}).Run(ctx)
		for deadline := time.Now().Add(10 * time.Second); !hub.Status(id).Connected; time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal("channel never connected")
			}
		}
		return n
	}
	jp, hk := start("jp"), start("hk")
	sync := func() {
		var synced struct{ Nodes []proxy.NodeView }
		call("POST", "/admin/proxy/sync", nil, &synced)
		for _, v := range synced.Nodes {
			if v.Error != "" || v.Kernels["xray"].Error != "" {
				t.Fatalf("同步出错：%+v", v)
			}
		}
	}

	var friend users.View
	call("POST", "/admin/users", map[string]any{"name": "朋友", "enabled": true, "traffic_limit": 10000}, &friend)
	var direct, relay proxy.Forward
	if code := call("POST", "/admin/proxy/forwards", map[string]any{"name": "DNS", "enabled": true, "node_id": jp.id, "port": 5353, "network": "udp", "target": "1.1.1.1", "target_port": 53}, &direct); code != 201 {
		t.Fatal(code)
	}
	if code := call("POST", "/admin/proxy/forwards", map[string]any{"name": "中转", "enabled": true, "user_id": friend.ID, "node_id": jp.id, "port": 8443, "target": "example.org", "target_port": 443, "exit": map[string]any{"node_id": hk.id, "address": "hk.example.com", "port": 9443}}, &relay); code != 201 {
		t.Fatal(code)
	}
	if relay.Exit == nil || relay.Exit.Key == "" || relay.Network != "tcp,udp" {
		t.Fatalf("隧道要生成密钥、默认 TCP+UDP：%+v", relay)
	}
	for _, c := range []struct {
		body map[string]any
		why  string
	}{
		{map[string]any{"name": "撞转发", "enabled": true, "node_id": jp.id, "port": 8443, "target": "a.example", "target_port": 1}, "入口端口和别的转发冲突"},
		{map[string]any{"name": "撞出口", "enabled": true, "node_id": hk.id, "port": 9443, "target": "a.example", "target_port": 1}, "端口和隧道出口冲突"},
		{map[string]any{"name": "同节点", "enabled": true, "node_id": jp.id, "port": 1, "target": "a.example", "target_port": 1, "exit": map[string]any{"node_id": jp.id, "address": "a.example", "port": 2}}, "出口不能是入口"},
		{map[string]any{"name": "统计口", "enabled": true, "node_id": jp.id, "port": proxy.StatsAPIPort, "target": "a.example", "target_port": 1}, "统计接口端口"},
		{map[string]any{"name": "没人", "enabled": true, "user_id": "nobody", "node_id": jp.id, "port": 3, "target": "a.example", "target_port": 1}, "使用者要存在"},
	} {
		if code := call("POST", "/admin/proxy/forwards", c.body, nil); code != 400 {
			t.Fatalf("%s：%d", c.why, code)
		}
	}
	if code := call("POST", "/admin/proxy/inbounds", map[string]any{"node_id": hk.id, "name": "撞隧道", "kernel": "sing-box", "protocol": "ss2022", "port": 9443, "enabled": true}, nil); code != 400 {
		t.Fatal("入站也不能占隧道出口的端口", code)
	}

	sync()
	jpDoc, hkDoc := jp.config(t), hk.config(t)
	want := []string{"fw-" + direct.ID, "fw-" + relay.ID}
	slices.Sort(want)
	if tags := inboundTags(jpDoc); !slices.Equal(tags, want) {
		t.Fatalf("入口节点：%v", tags)
	}
	if tags := inboundTags(hkDoc); strings.Join(tags, ",") != "tun-"+relay.ID {
		t.Fatalf("出口节点：%v", tags)
	}
	raw, _ := json.Marshal(jpDoc)
	for _, want := range []string{`"outboundTag":"fw-` + relay.ID + `-out"`, `"address":"hk.example.com"`, `"port":9443`, relay.Exit.Key, `"statsInboundDownlink":true`, `"network":"udp"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("入口配置缺 %s：%s", want, raw)
		}
	}
	if !strings.Contains(jp.unit(), "Environment=XRAY_BUF_SPLICE=disable") {
		t.Error("xray 服务要关掉 splice，否则直接转发的下行不计数")
	}

	// 流量：入口入站的计数记到规则上，有使用者的再计入他的用量；用完额度后规则停掉。
	jp.setStats(`{"stat":[{"name":"inbound>>>fw-` + direct.ID + `>>>traffic>>>uplink","value":100},{"name":"inbound>>>fw-` + direct.ID + `>>>traffic>>>downlink","value":200},{"name":"inbound>>>fw-` + relay.ID + `>>>traffic>>>uplink","value":3000},{"name":"inbound>>>fw-` + relay.ID + `>>>traffic>>>downlink","value":8000},{"name":"inbound>>>api>>>traffic>>>downlink","value":5}]}`)
	sync()
	var data struct{ Forwards []proxy.Forward }
	call("GET", "/admin/proxy", nil, &data)
	got := map[string]proxy.Forward{}
	for _, f := range data.Forwards {
		got[f.ID] = f
	}
	if got[direct.ID].TrafficUp != 100 || got[direct.ID].TrafficDown != 200 || got[relay.ID].TrafficUp != 3000 || got[relay.ID].TrafficDown != 8000 {
		t.Fatalf("规则流量：%+v", data.Forwards)
	}
	var list []users.View
	call("GET", "/admin/users", nil, &list)
	if len(list) != 1 || list[0].TrafficUsed != 11000 || list[0].Status != users.StatusExceeded {
		t.Fatalf("计入使用者并超额：%+v", list)
	}
	sync() // 超额后的那一轮（Run 会立刻再同步，测试里手动）
	if tags := inboundTags(jp.config(t)); strings.Join(tags, ",") != "fw-"+direct.ID {
		t.Fatalf("超额使用者的转发要停掉：%v", tags)
	}
	if hk.config(t) != nil {
		t.Fatal("出口节点没有规则了，配置要移除")
	}
	// 编辑不改流量计数；删除后入口节点也清空。
	call("PUT", "/admin/proxy/forwards/"+direct.ID, map[string]any{"name": "DNS2", "enabled": true, "node_id": jp.id, "port": 5353, "network": "udp", "target": "8.8.8.8", "target_port": 53, "traffic_up": 0}, nil)
	call("GET", "/admin/proxy", nil, &data)
	for _, f := range data.Forwards {
		if f.ID == direct.ID && (f.Name != "DNS2" || f.TrafficUp != 100) {
			t.Fatalf("编辑保留计数：%+v", f)
		}
	}
	call("DELETE", "/admin/proxy/forwards/"+direct.ID, nil, nil)
	call("DELETE", "/admin/proxy/forwards/"+relay.ID, nil, nil)
	sync()
	if jp.config(t) != nil {
		t.Fatal("没有规则后入口节点配置要移除")
	}
}
