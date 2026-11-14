// Package sites is the website module: nginx sites (reverse proxy, static
// directory, redirect) on managed nodes, with HTTPS from the certificates
// module or from files already on the node.
package sites

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/leeska/TZA-ControlCenter/internal/certs"
	"github.com/leeska/TZA-ControlCenter/internal/channel"
)

// Kinds of site.
const (
	KindProxy    = "proxy"
	KindStatic   = "static"
	KindRedirect = "redirect"
)

// TLS modes.
const (
	TLSNone  = "none"
	TLSCert  = "cert"  // a certificate of the certs module, deployed to the node
	TLSFiles = "files" // certificate and key files already on the node
)

// Site is one nginx site on one node.
type Site struct {
	ID       string   `json:"id"`
	NodeID   string   `json:"node_id"`
	Name     string   `json:"name"`
	Enabled  bool     `json:"enabled"`
	Domains  []string `json:"domains"`
	Kind     string   `json:"kind"`
	Upstream string   `json:"upstream,omitempty"` // proxy: http(s)://host:port[/path]
	// WebSocket passes Upgrade through and allows long-lived connections.
	WebSocket bool   `json:"websocket,omitempty"`
	Root      string `json:"root,omitempty"` // static
	// Redirect target; KeepPath appends the request path and query.
	RedirectTo   string `json:"redirect_to,omitempty"`
	RedirectCode int    `json:"redirect_code,omitempty"`
	KeepPath     bool   `json:"keep_path,omitempty"`
	TLS          string `json:"tls"`
	CertID       string `json:"cert_id,omitempty"`
	CertPath     string `json:"cert_path,omitempty"`
	KeyPath      string `json:"key_path,omitempty"`
	ForceHTTPS   bool   `json:"force_https,omitempty"`
	// CloudflareRealIP takes the visitor's address from CF-Connecting-IP when
	// the request comes from Cloudflare.
	CloudflareRealIP bool  `json:"cloudflare_real_ip,omitempty"`
	MaxBodyMB        int   `json:"max_body_mb,omitempty"`
	Created          int64 `json:"created_at"`
}

// FileName is the site's file in the node's conf.d.
func (s Site) FileName() string { return channel.SiteFilePrefix + s.ID + ".conf" }

func absPath(p string) bool {
	return strings.HasPrefix(p, "/") && filepath.Clean(p) == p && !strings.ContainsAny(p, " ;{}\"'\\$\n") && len(p) <= 255
}

// Validate checks and normalises what the administrator sent.
func (s *Site) Validate() error {
	s.Name = strings.TrimSpace(s.Name)
	if utf8.RuneCountInString(s.Name) > 40 {
		return errors.New("名称最多 40 个字")
	}
	if s.NodeID == "" {
		return errors.New("请选择节点")
	}
	domains, err := certs.NormalizeDomains(s.Domains)
	if err != nil {
		return err
	}
	s.Domains = domains
	if s.Name == "" {
		s.Name = domains[0]
	}
	switch s.Kind {
	case KindProxy:
		u, err := url.Parse(strings.TrimSpace(s.Upstream))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(s.Upstream, " ;{}\"'$\n") {
			return errors.New("上游地址写法是 http://127.0.0.1:8080（也可以带路径）")
		}
		s.Upstream = strings.TrimSpace(s.Upstream)
		s.Root, s.RedirectTo = "", ""
	case KindStatic:
		s.Root = strings.TrimSpace(s.Root)
		if !absPath(s.Root) || s.Root == "/" {
			return errors.New("网站目录须是绝对路径，如 /var/www/example")
		}
		s.Upstream, s.RedirectTo, s.WebSocket = "", "", false
	case KindRedirect:
		u, err := url.Parse(strings.TrimSpace(s.RedirectTo))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || strings.ContainsAny(s.RedirectTo, " ;{}\"'$\n") {
			return errors.New("跳转地址须是 http(s) 链接")
		}
		s.RedirectTo = strings.TrimSpace(s.RedirectTo)
		if s.RedirectCode == 0 {
			s.RedirectCode = 301
		}
		if s.RedirectCode != 301 && s.RedirectCode != 302 && s.RedirectCode != 307 && s.RedirectCode != 308 {
			return errors.New("跳转状态码只能是 301、302、307 或 308")
		}
		s.Upstream, s.Root, s.WebSocket = "", "", false
	default:
		return errors.New("请选择反向代理、静态网站或跳转")
	}
	switch s.TLS {
	case "", TLSNone:
		s.TLS, s.CertID, s.CertPath, s.KeyPath, s.ForceHTTPS = TLSNone, "", "", "", false
	case TLSCert:
		if s.CertID == "" {
			return errors.New("请选择证书")
		}
		s.CertPath, s.KeyPath = "", ""
	case TLSFiles:
		s.CertPath, s.KeyPath = strings.TrimSpace(s.CertPath), strings.TrimSpace(s.KeyPath)
		if !absPath(s.CertPath) || !absPath(s.KeyPath) {
			return errors.New("证书和私钥须是节点上的绝对路径")
		}
		s.CertID = ""
	default:
		return errors.New("HTTPS 方式无效")
	}
	if s.MaxBodyMB < 0 || s.MaxBodyMB > 10240 {
		return errors.New("上传大小上限为 0–10240 MB")
	}
	return nil
}

// cloudflareRanges are Cloudflare's published edge ranges (2026-11).
var cloudflareRanges = []string{
	"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22", "141.101.64.0/18", "108.162.192.0/18",
	"190.93.240.0/20", "188.114.96.0/20", "197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
	"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
	"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32", "2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32",
}

// newHTTP2 reports whether nginx takes the separate `http2 on;` (1.25.1+).
func newHTTP2(version string) bool {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 3 {
		return true // unknown: assume a current nginx
	}
	major, _ := strconv.Atoi(parts[0])
	minor, _ := strconv.Atoi(parts[1])
	patch, _ := strconv.Atoi(parts[2])
	return major > 1 || minor > 25 || (minor == 25 && patch >= 1)
}

// Paths are the certificate files a TLS site uses on its node.
func (s Site) Paths(cert *certs.Cert) (crt, key string) {
	if s.TLS == TLSFiles {
		return s.CertPath, s.KeyPath
	}
	if cert != nil {
		dir := certs.DefaultDir(cert.Domains[0])
		return dir + "/fullchain.pem", dir + "/privkey.pem"
	}
	return "", ""
}

func (s Site) body(b *strings.Builder) {
	if s.CloudflareRealIP {
		for _, r := range cloudflareRanges {
			fmt.Fprintf(b, "    set_real_ip_from %s;\n", r)
		}
		b.WriteString("    real_ip_header CF-Connecting-IP;\n")
	}
	if s.MaxBodyMB > 0 {
		fmt.Fprintf(b, "    client_max_body_size %dm;\n", s.MaxBodyMB)
	}
	switch s.Kind {
	case KindProxy:
		b.WriteString("    location / {\n")
		fmt.Fprintf(b, "        proxy_pass %s;\n", s.Upstream)
		b.WriteString("        proxy_http_version 1.1;\n")
		b.WriteString("        proxy_set_header Host $host;\n")
		b.WriteString("        proxy_set_header X-Real-IP $remote_addr;\n")
		b.WriteString("        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n")
		b.WriteString("        proxy_set_header X-Forwarded-Proto $scheme;\n")
		if strings.HasPrefix(s.Upstream, "https://") {
			b.WriteString("        proxy_ssl_server_name on;\n")
		}
		if s.WebSocket {
			b.WriteString("        proxy_set_header Upgrade $http_upgrade;\n")
			b.WriteString("        proxy_set_header Connection \"upgrade\";\n")
			b.WriteString("        proxy_read_timeout 3600s;\n")
			b.WriteString("        proxy_send_timeout 3600s;\n")
		}
		b.WriteString("    }\n")
	case KindStatic:
		fmt.Fprintf(b, "    root %s;\n", s.Root)
		b.WriteString("    index index.html index.htm;\n")
		b.WriteString("    location / {\n        try_files $uri $uri/ =404;\n    }\n")
	case KindRedirect:
		target := s.RedirectTo
		if s.KeepPath {
			target = strings.TrimSuffix(target, "/") + "$request_uri"
		}
		fmt.Fprintf(b, "    location / {\n        return %d %s;\n    }\n", s.RedirectCode, target)
	}
}

// Config renders the site's nginx file. crt / key are the certificate
// paths for a TLS site; version is the node's nginx version.
func (s Site) Config(crt, key, version string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# 由 TZA ControlCenter 管理（站点 %s「%s」），在面板里修改，手改会被覆盖。\n", s.ID, strings.ReplaceAll(s.Name, "\n", " "))
	names := strings.Join(s.Domains, " ")
	b.WriteString("server {\n    listen 80;\n    listen [::]:80;\n")
	fmt.Fprintf(&b, "    server_name %s;\n", names)
	if s.TLS != TLSNone && s.ForceHTTPS {
		b.WriteString("    location / {\n        return 301 https://$host$request_uri;\n    }\n")
	} else {
		s.body(&b)
	}
	b.WriteString("}\n")
	if s.TLS == TLSNone {
		return b.String()
	}
	b.WriteString("\nserver {\n")
	if newHTTP2(version) {
		b.WriteString("    listen 443 ssl;\n    listen [::]:443 ssl;\n    http2 on;\n")
	} else {
		b.WriteString("    listen 443 ssl http2;\n    listen [::]:443 ssl http2;\n")
	}
	fmt.Fprintf(&b, "    server_name %s;\n", names)
	fmt.Fprintf(&b, "    ssl_certificate %s;\n    ssl_certificate_key %s;\n", crt, key)
	b.WriteString("    ssl_protocols TLSv1.2 TLSv1.3;\n    ssl_session_cache shared:TZASSL:10m;\n")
	s.body(&b)
	b.WriteString("}\n")
	return b.String()
}
