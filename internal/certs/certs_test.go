package certs_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/agent"
	"github.com/leeska/TZA-Probe/internal/certs"
	"github.com/leeska/TZA-Probe/internal/channel"
	"github.com/leeska/TZA-Probe/internal/httpapi"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/module"
	"github.com/leeska/TZA-Probe/internal/remote"
	"github.com/leeska/TZA-Probe/internal/storage"
)

func TestNormalizeDomains(t *testing.T) {
	got, err := certs.NormalizeDomains([]string{" Example.COM. ", "*.example.com", "example.com", ""})
	if err != nil || strings.Join(got, ",") != "example.com,*.example.com" {
		t.Fatal(got, err)
	}
	for _, bad := range [][]string{{}, {"localhost"}, {"a.*.example.com"}, {"-a.example.com"}, {"exa mple.com"}} {
		if _, err := certs.NormalizeDomains(bad); err == nil {
			t.Errorf("%v 要拒绝", bad)
		}
	}
	if certs.DefaultDir("*.example.com") != "/etc/tza-certs/_.example.com" {
		t.Fatal(certs.DefaultDir("*.example.com"))
	}
}

func TestCloudflare(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(403)
			w.Write([]byte(`{"success":false,"errors":[{"message":"Invalid API Token"}]}`))
			return
		}
		switch {
		case r.URL.Path == "/zones" && r.URL.Query().Get("name") == "example.com":
			w.Write([]byte(`{"success":true,"result":[{"id":"z1","name":"example.com","name_servers":["ns1.example.net"]}]}`))
		case r.URL.Path == "/zones":
			w.Write([]byte(`{"success":true,"result":[]}`))
		case r.URL.Path == "/zones/z1/dns_records" && r.Method == "POST":
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"type":"TXT"`) {
				t.Error(string(body))
			}
			w.Write([]byte(`{"success":true,"result":{"id":"r1"}}`))
		case r.URL.Path == "/zones/z1/dns_records/r1" && r.Method == "DELETE":
			w.Write([]byte(`{"success":true,"result":{"id":"r1"}}`))
		case r.URL.Path == "/user/tokens/verify":
			w.Write([]byte(`{"success":true,"result":{"status":"active"}}`))
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	cf := &certs.Cloudflare{Token: "tok", BaseURL: srv.URL}
	if err := cf.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	zone, err := cf.Zone(ctx, "_acme-challenge.a.b.example.com")
	if err != nil || zone.ID != "z1" || zone.NameServers[0] != "ns1.example.net" {
		t.Fatal(zone, err)
	}
	if strings.Join(seen[1:4], "|") != "GET /zones?name=acme-challenge.a.b.example.com|GET /zones?name=a.b.example.com|GET /zones?name=b.example.com" && !strings.Contains(strings.Join(seen, "|"), "name=example.com") {
		t.Fatal(seen)
	}
	if id, err := cf.AddTXT(ctx, "z1", "_acme-challenge.example.com", "v"); err != nil || id != "r1" {
		t.Fatal(id, err)
	}
	if err := cf.DeleteRecord(ctx, "z1", "r1"); err != nil {
		t.Fatal(err)
	}
	if _, err := cf.Zone(ctx, "nothing.org"); err == nil {
		t.Fatal("找不到 zone 要报错")
	}
	bad := &certs.Cloudflare{Token: "wrong", BaseURL: srv.URL}
	if err := bad.Verify(ctx); err == nil || !strings.Contains(err.Error(), "Invalid API Token") {
		t.Fatal("Cloudflare 的错误要带回来", err)
	}
}

// fakeIssuer signs with its own CA; fail makes the next Issue fail.
type fakeIssuer struct {
	mu    sync.Mutex
	calls int
	fail  error
	life  time.Duration
	age   time.Duration // how long ago the certificate starts
}

func (f *fakeIssuer) Issue(_ context.Context, domains []string, dns certs.DNSProvider, log func(string)) (certs.Issued, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail != nil {
		return certs.Issued{}, f.fail
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	serial := big.NewInt(int64(f.calls) + 1000)
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: domains[0]}, DNSNames: domains, NotBefore: time.Now().Add(-f.age), NotAfter: time.Now().Add(f.life - f.age)}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	keyDER, _ := x509.MarshalECPrivateKey(key)
	return certs.Issued{
		CertPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		KeyPEM:  string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})),
		Serial:  hex.EncodeToString(serial.Bytes()), Issuer: "Fake CA", NotBefore: tmpl.NotBefore, NotAfter: tmpl.NotAfter,
	}, nil
}

// recorder keeps module events.
type recorder struct {
	mu     sync.Mutex
	events []model.Event
}

func (r *recorder) RecordEvent(_ context.Context, e model.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

func (r *recorder) list() []model.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]model.Event{}, r.events...)
}

type noDNS struct{}

func (noDNS) Zone(context.Context, string) (certs.Zone, error)               { return certs.Zone{}, nil }
func (noDNS) AddTXT(context.Context, string, string, string) (string, error) { return "", nil }
func (noDNS) DeleteRecord(context.Context, string, string) error             { return nil }

// 证书：签发后写到节点目录（私钥 0600）并执行重载命令；签发失败保留状态并退避；续期换新证书
// 再下发；节点没开证书能力时目标报错；总览卡片提示；下载私钥写审计日志。
func TestCertsOverChannel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remoteMod, certsMod := &remote.Module{}, &certs.Module{}
	mods := []module.Module{remoteMod, certsMod}
	store, err := storage.OpenWith(filepath.Join(t.TempDir(), "tza-probe.db"), module.Migrations(mods))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	node, token, err := store.SaveNode(ctx, model.Node{Name: "web", Billing: model.Billing{ResetAt: "2026-11-01T00:00:00Z", TrafficMode: "sum"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	other, _, _ := store.SaveNode(ctx, model.Node{Name: "offline", Billing: model.Billing{ResetAt: "2026-11-01T00:00:00Z", TrafficMode: "sum"}}, true)
	hub := &channel.Hub{}
	issuer := &fakeIssuer{life: 90 * 24 * time.Hour}
	remoteMod.Hub, remoteMod.Nodes = hub, store
	certsMod.Store, certsMod.Hub, certsMod.Nodes = &certs.Store{DB: store.DB()}, hub, store
	certsMod.NewIssuer = func(string) certs.Issuer { return issuer }
	certsMod.NewDNS = func(string) certs.DNSProvider { return noDNS{} }
	events := &recorder{}
	certsMod.Events = events
	certsMod.Init()
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
			if b, ok := out.(*[]byte); ok {
				*b, _ = io.ReadAll(res.Body)
			} else {
				json.NewDecoder(res.Body).Decode(out)
			}
		}
		return res.StatusCode
	}
	transport, err := agent.NewTransport(server.URL, token, true)
	if err != nil {
		t.Fatal(err)
	}
	go (&agent.Channel{Transport: transport, Handlers: agent.CertHandlers(), Capabilities: []string{channel.CapabilityCerts}}).Run(ctx)
	for deadline := time.Now().Add(10 * time.Second); !hub.Status(node).Connected; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("channel never connected")
		}
	}

	var dns certs.DNSView
	if code := call("POST", "/admin/certs/dns", map[string]string{"name": "CF", "token": "secret-token-1234"}, &dns); code != 201 || dns.Token != "" || dns.TokenHint != "…1234" {
		t.Fatalf("DNS 账号不回令牌：%d %+v", code, dns)
	}
	dir := filepath.Join(t.TempDir(), "certs", "example.com")
	var c certs.CertView
	if code := call("POST", "/admin/certs", map[string]any{"domains": []string{"example.com", "*.example.com"}, "dns_id": dns.ID, "targets": []map[string]string{
		{"node_id": node, "dir": dir, "reload": "echo reloaded >> reload.log"},
		{"node_id": other},
	}}, &c); code != 201 || c.Status != certs.StatusPending || c.Targets[1].Dir != "/etc/tza-certs/example.com" {
		t.Fatalf("%d %+v", code, c)
	}
	if code := call("POST", "/admin/certs", map[string]any{"domains": []string{"a.example"}, "dns_id": "nope"}, nil); code != 400 {
		t.Fatal("DNS 账号要存在", code)
	}
	if code := call("DELETE", "/admin/certs/dns/"+dns.ID, nil, nil); code != 409 {
		t.Fatal("还在用的 DNS 账号不能删", code)
	}

	if err := certsMod.Process(ctx); err != nil {
		t.Fatal(err)
	}
	var list struct{ Certs []certs.CertView }
	call("GET", "/admin/certs", nil, &list)
	got := list.Certs[0]
	if got.Status != certs.StatusValid || got.Serial == "" || got.CertPEM != "" || got.KeyPEM != "" || got.Issuer != "Fake CA" {
		t.Fatalf("签发后：%+v", got)
	}
	if got.Targets[0].Deployed != got.Serial || got.Targets[0].Error != "" || !strings.Contains(got.Targets[1].Error, "--manage") {
		t.Fatalf("下发：%+v", got.Targets)
	}
	key, err := os.Stat(filepath.Join(dir, "privkey.pem"))
	if err != nil || key.Mode().Perm() != 0600 {
		t.Fatal("私钥要 0600", err)
	}
	if chain, _ := os.ReadFile(filepath.Join(dir, "fullchain.pem")); !strings.Contains(string(chain), "BEGIN CERTIFICATE") {
		t.Fatal("证书没写进去")
	}
	if logged, _ := os.ReadFile(filepath.Join(dir, "reload.log")); string(logged) != "reloaded\n" {
		t.Fatalf("重载命令要在目录里执行一次：%q", logged)
	}
	// 没到期、没变化：不重新签发，也不重复下发。
	certsMod.Process(ctx)
	if issuer.calls != 1 {
		t.Fatal("没到期不能重签", issuer.calls)
	}
	if logged, _ := os.ReadFile(filepath.Join(dir, "reload.log")); string(logged) != "reloaded\n" {
		t.Fatal("没变化不能重复重载")
	}

	// 续期失败：状态变 error，旧证书保留，下次尝试往后推。
	issuer.fail = errors.New("too many certificates")
	call("POST", "/admin/certs/"+got.ID+"/renew", nil, nil)
	certsMod.Process(ctx)
	call("GET", "/admin/certs", nil, &list)
	if f := list.Certs[0]; f.Status != certs.StatusError || f.Serial != got.Serial || f.NextAttempt < time.Now().Add(50*time.Minute).Unix() || !strings.Contains(f.Error, "too many") {
		t.Fatalf("失败后：%+v", f)
	}
	certsMod.Process(ctx)
	if issuer.calls != 2 {
		t.Fatal("退避期间不能再试", issuer.calls)
	}
	// 告警：变坏时发一条触发通知（其中一个下发目标本来就失败了），状态不变不重复发。
	if ev := events.list(); len(ev) != 1 || !ev[0].Active || ev[0].Kind != model.EventCert || !strings.HasPrefix(ev[0].NodeID, "cert:") {
		t.Fatalf("告警事件：%+v", ev)
	}
	var cards []module.Card
	cards, _ = certsMod.Overview(ctx)
	if cards[0].Tone != "warn" || len(cards[0].Items) != 1 || !strings.Contains(cards[0].Items[0].Note, "签发失败") {
		t.Fatalf("总览：%+v", cards)
	}

	// 续期成功：换了新证书，再下发一次。
	issuer.fail = nil
	call("POST", "/admin/certs/"+got.ID+"/renew", nil, nil)
	certsMod.Process(ctx)
	call("GET", "/admin/certs", nil, &list)
	renewed := list.Certs[0]
	if renewed.Status != certs.StatusValid || renewed.Serial == got.Serial || renewed.Targets[0].Deployed != renewed.Serial {
		t.Fatalf("续期后：%+v", renewed)
	}
	if logged, _ := os.ReadFile(filepath.Join(dir, "reload.log")); string(logged) != "reloaded\nreloaded\n" {
		t.Fatalf("新证书要再下发并重载：%q", logged)
	}
	// 下发不到的目标去掉后恢复：发一条解除通知。
	call("PUT", "/admin/certs/"+got.ID, map[string]any{"domains": []string{"example.com", "*.example.com"}, "dns_id": dns.ID, "targets": []map[string]string{{"node_id": node, "dir": dir, "reload": "echo reloaded >> reload.log"}}}, nil)
	certsMod.Process(ctx)
	if ev := events.list(); len(ev) != 2 || ev[1].Active || !strings.Contains(ev[1].Message, "恢复") {
		t.Fatalf("恢复事件：%+v", ev)
	}

	// 下载私钥要写审计日志。
	var pemKey []byte
	if code := call("GET", "/admin/certs/"+got.ID+"/download?part=key", nil, &pemKey); code != 200 || !strings.Contains(string(pemKey), "PRIVATE KEY") {
		t.Fatal(code)
	}
	var audit []model.AuditEntry
	call("GET", "/admin/audit?limit=20", nil, &audit)
	found := false
	for _, e := range audit {
		found = found || e.Action == "certs.download"
	}
	if !found {
		t.Fatal("下载要写审计日志")
	}

	// 有效期只剩不到三分之一（90 天的证书剩 10 天）时自动续期；总有效期很短（6 天）的
	// 证书不会每轮都重签。
	issuer.life, issuer.age = 90*24*time.Hour, 80*24*time.Hour
	call("POST", "/admin/certs/"+got.ID+"/renew", nil, nil)
	certsMod.Process(ctx)
	calls := issuer.calls
	certsMod.Process(ctx)
	if issuer.calls != calls+1 {
		t.Fatal("剩不到三分之一要自动续期", issuer.calls, calls)
	}
	issuer.life, issuer.age = 6*24*time.Hour, 0
	certsMod.Process(ctx)
	calls = issuer.calls
	certsMod.Process(ctx)
	certsMod.Process(ctx)
	if issuer.calls != calls {
		t.Fatal("新签的短期证书不能反复重签", issuer.calls, calls)
	}
}

func TestRenewAt(t *testing.T) {
	day := int64(86400)
	now := time.Now().Unix()
	for _, c := range []struct {
		life, left int64
		due        bool
	}{{90, 31, false}, {90, 29, true}, {45, 16, false}, {45, 14, true}, {6, 3, false}, {6, 1, true}} {
		cert := certs.Cert{CertPEM: "x", NotBefore: now - (c.life-c.left)*day, NotAfter: now + c.left*day}
		if cert.Due(time.Now()) != c.due {
			t.Errorf("有效期 %d 天、剩 %d 天：due=%v", c.life, c.left, !c.due)
		}
	}
}
