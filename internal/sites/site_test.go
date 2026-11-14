package sites

import (
	"strings"
	"testing"

	"github.com/leeska/TZA-ControlCenter/internal/certs"
)

func TestValidate(t *testing.T) {
	ok := []Site{
		{NodeID: "n", Domains: []string{"App.Example.com"}, Kind: KindProxy, Upstream: "http://127.0.0.1:3000"},
		{NodeID: "n", Domains: []string{"www.example.com"}, Kind: KindStatic, Root: "/var/www/site"},
		{NodeID: "n", Domains: []string{"old.example.com"}, Kind: KindRedirect, RedirectTo: "https://new.example.com", KeepPath: true},
		{NodeID: "n", Domains: []string{"a.example.com"}, Kind: KindProxy, Upstream: "https://10.0.0.2:8443/app", TLS: TLSFiles, CertPath: "/etc/ssl/a.crt", KeyPath: "/etc/ssl/a.key"},
	}
	for i := range ok {
		if err := ok[i].Validate(); err != nil {
			t.Errorf("%+v: %v", ok[i], err)
		}
	}
	if ok[0].Domains[0] != "app.example.com" || ok[0].Name != "app.example.com" || ok[2].RedirectCode != 301 {
		t.Fatalf("%+v %+v", ok[0], ok[2])
	}
	bad := []Site{
		{NodeID: "n", Domains: []string{"a.example.com"}, Kind: KindProxy, Upstream: "127.0.0.1:3000"},
		{NodeID: "n", Domains: []string{"a.example.com"}, Kind: KindProxy, Upstream: "http://x; return 200"},
		{NodeID: "n", Domains: []string{"a.example.com"}, Kind: KindStatic, Root: "relative"},
		{NodeID: "n", Domains: []string{"a.example.com"}, Kind: KindStatic, Root: "/var/www/a b"},
		{NodeID: "n", Domains: []string{"a.example.com"}, Kind: KindRedirect, RedirectTo: "https://x", RedirectCode: 200},
		{NodeID: "n", Domains: []string{"a.example.com"}, Kind: KindProxy, Upstream: "http://x", TLS: TLSCert},
		{NodeID: "n", Domains: []string{"a.example.com"}, Kind: KindProxy, Upstream: "http://x", TLS: TLSFiles, CertPath: "/x/../y", KeyPath: "/k"},
		{NodeID: "n", Domains: []string{"bad domain"}, Kind: KindProxy, Upstream: "http://x"},
		{NodeID: "", Domains: []string{"a.example.com"}, Kind: KindProxy, Upstream: "http://x"},
	}
	for _, s := range bad {
		if err := s.Validate(); err == nil {
			t.Errorf("%+v 要拒绝", s)
		}
	}
}

func TestConfig(t *testing.T) {
	s := Site{ID: "ab12", NodeID: "n", Domains: []string{"app.example.com", "www.app.example.com"}, Kind: KindProxy, Upstream: "http://127.0.0.1:3000", WebSocket: true, TLS: TLSFiles, CertPath: "/c.pem", KeyPath: "/k.pem", ForceHTTPS: true, CloudflareRealIP: true, MaxBodyMB: 100}
	s.Validate()
	conf := s.Config("/c.pem", "/k.pem", "1.26.3")
	for _, want := range []string{"server_name app.example.com www.app.example.com;", "return 301 https://$host$request_uri;", "http2 on;", "ssl_certificate /c.pem;", "proxy_pass http://127.0.0.1:3000;", "proxy_set_header Upgrade $http_upgrade;", "real_ip_header CF-Connecting-IP;", "client_max_body_size 100m;"} {
		if !strings.Contains(conf, want) {
			t.Errorf("缺 %q:\n%s", want, conf)
		}
	}
	if strings.Count(conf, "proxy_pass") != 1 {
		t.Error("强制 HTTPS 时 80 端口只跳转")
	}
	if old := s.Config("/c.pem", "/k.pem", "1.22.1"); !strings.Contains(old, "listen 443 ssl http2;") || strings.Contains(old, "http2 on;") {
		t.Errorf("旧版 nginx 用 listen … http2:\n%s", old)
	}
	r := Site{ID: "r1", NodeID: "n", Domains: []string{"old.example.com"}, Kind: KindRedirect, RedirectTo: "https://new.example.com/", KeepPath: true}
	r.Validate()
	if conf := r.Config("", "", "1.26.3"); !strings.Contains(conf, "return 301 https://new.example.com$request_uri;") || strings.Contains(conf, "443") {
		t.Fatalf("%s", conf)
	}
	c := certs.Cert{Domains: []string{"*.example.com", "example.com"}}
	st := Site{TLS: TLSCert}
	if crt, key := st.Paths(&c); crt != "/etc/tza-certs/_.example.com/fullchain.pem" || key != "/etc/tza-certs/_.example.com/privkey.pem" {
		t.Fatal(crt, key)
	}
	if !certCovers(c.Domains, []string{"app.example.com", "example.com"}) || certCovers(c.Domains, []string{"a.b.example.com"}) {
		t.Fatal("通配符只覆盖一层")
	}
}
