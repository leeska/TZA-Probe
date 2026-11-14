package sites_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leeska/TZA-ControlCenter/internal/agent"
	"github.com/leeska/TZA-ControlCenter/internal/certs"
	"github.com/leeska/TZA-ControlCenter/internal/channel"
	"github.com/leeska/TZA-ControlCenter/internal/httpapi"
	"github.com/leeska/TZA-ControlCenter/internal/model"
	"github.com/leeska/TZA-ControlCenter/internal/module"
	"github.com/leeska/TZA-ControlCenter/internal/remote"
	"github.com/leeska/TZA-ControlCenter/internal/sites"
	"github.com/leeska/TZA-ControlCenter/internal/storage"
)

// 网站：经管理通道把站点写进节点的 nginx；同节点同域名冲突要拒绝；选了证书的站点自动给证书
// 加下发目标，证书到位前「等待」，到位后才写进 nginx；删掉站点后节点上的文件也删掉。
func TestSitesOverChannel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remoteMod, certsMod, sitesMod := &remote.Module{}, &certs.Module{}, &sites.Module{}
	mods := []module.Module{remoteMod, certsMod, sitesMod}
	store, err := storage.OpenWith(filepath.Join(t.TempDir(), "tza.db"), module.Migrations(mods))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	node, token, err := store.SaveNode(ctx, model.Node{Name: "web", Billing: model.Billing{ResetAt: "2026-11-01T00:00:00Z", TrafficMode: "sum"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	hub := &channel.Hub{}
	remoteMod.Hub, remoteMod.Nodes = hub, store
	certStore := &certs.Store{DB: store.DB()}
	sitesMod.Store, sitesMod.Hub, sitesMod.Nodes, sitesMod.Certs = &sites.Store{DB: store.DB()}, hub, store, certStore
	sitesMod.Init()
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
	conf := t.TempDir()
	host := &agent.NginxHost{ConfDir: conf, Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "nginx" && args[0] == "-v" {
			return []byte("nginx version: nginx/1.26.3"), nil
		}
		if name == "systemctl" && args[0] == "is-active" {
			return []byte("active"), nil
		}
		return nil, nil
	}}
	transport, err := agent.NewTransport(server.URL, token, true)
	if err != nil {
		t.Fatal(err)
	}
	go (&agent.Channel{Transport: transport, Handlers: host.Handlers(), Capabilities: []string{channel.CapabilitySites}}).Run(ctx)
	for deadline := time.Now().Add(10 * time.Second); !hub.Status(node).Connected; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("channel never connected")
		}
	}

	var app sites.Site
	if code := call("POST", "/admin/sites", map[string]any{"node_id": node, "enabled": true, "domains": []string{"app.example.com"}, "kind": "proxy", "upstream": "http://127.0.0.1:3000", "websocket": true}, &app); code != 201 {
		t.Fatal(code)
	}
	if code := call("POST", "/admin/sites", map[string]any{"node_id": node, "enabled": true, "domains": []string{"app.example.com"}, "kind": "static", "root": "/var/www"}, nil); code != 400 {
		t.Fatal("同节点同域名要拒绝", code)
	}
	call("POST", "/admin/sites/sync", nil, nil)
	written, err := os.ReadFile(filepath.Join(conf, "tza-site-"+app.ID+".conf"))
	if err != nil || !strings.Contains(string(written), "proxy_pass http://127.0.0.1:3000;") {
		t.Fatalf("站点要写进 nginx：%v %s", err, written)
	}

	// 证书联动：先建 DNS 账号和证书（直接写库，不签发）。
	dns, _ := certStore.SaveDNS(ctx, certs.DNSAccount{Name: "cf", Token: "tok"})
	cert, err := certStore.SaveCert(ctx, "", certs.CertInput{Domains: []string{"*.example.com", "example.com"}, DNSID: dns.ID})
	if err != nil {
		t.Fatal(err)
	}
	if code := call("POST", "/admin/sites", map[string]any{"node_id": node, "enabled": true, "domains": []string{"deep.a.example.com"}, "kind": "static", "root": "/var/www", "tls": "cert", "cert_id": cert.ID}, nil); code != 400 {
		t.Fatal("证书不覆盖域名要拒绝", code)
	}
	var secure sites.Site
	if code := call("POST", "/admin/sites", map[string]any{"node_id": node, "enabled": true, "domains": []string{"secure.example.com"}, "kind": "static", "root": "/var/www", "tls": "cert", "cert_id": cert.ID, "force_https": true}, &secure); code != 201 {
		t.Fatal(code)
	}
	got, _ := certStore.Cert(ctx, cert.ID)
	if len(got.Targets) != 1 || got.Targets[0].NodeID != node || got.Targets[0].Dir != "/etc/tza-certs/_.example.com" || !strings.Contains(got.Targets[0].Reload, "reload nginx") {
		t.Fatalf("要自动加证书下发目标：%+v", got.Targets)
	}
	call("POST", "/admin/sites/sync", nil, nil)
	var list struct {
		Views map[string]sites.SiteView
	}
	call("GET", "/admin/sites", nil, &list)
	if list.Views[secure.ID].State != sites.StateWaiting || list.Views[app.ID].State != sites.StateOK {
		t.Fatalf("证书没到位时等待：%+v", list.Views)
	}
	if _, err := os.Stat(filepath.Join(conf, "tza-site-"+secure.ID+".conf")); err == nil {
		t.Fatal("证书没到位时不能写进 nginx")
	}
	// 证书签好并下发到这台节点后，站点写进去，用的是下发目录里的文件。
	certStore.Update(ctx, cert.ID, func(c *certs.Cert) {
		c.CertPEM, c.KeyPEM, c.Serial, c.Status = "x", "y", "01", certs.StatusValid
		c.Targets[0].Deployed = "01"
	})
	call("POST", "/admin/sites/sync", nil, nil)
	written, err = os.ReadFile(filepath.Join(conf, "tza-site-"+secure.ID+".conf"))
	if err != nil || !strings.Contains(string(written), "ssl_certificate /etc/tza-certs/_.example.com/fullchain.pem;") || !strings.Contains(string(written), "http2 on;") {
		t.Fatalf("证书到位后写入：%v %s", err, written)
	}

	// 删除：节点上的文件跟着删。
	call("DELETE", "/admin/sites/"+app.ID, nil, nil)
	call("DELETE", "/admin/sites/"+secure.ID, nil, nil)
	call("POST", "/admin/sites/sync", nil, nil)
	if left, _ := os.ReadDir(conf); len(left) != 0 {
		t.Fatalf("删除后要清掉：%v", left)
	}
	cards, _ := sitesMod.Overview(ctx)
	if cards[0].Value != "0" {
		t.Fatalf("%+v", cards)
	}
}
