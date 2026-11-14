package proxy

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ExtNode is a node from an external subscription, kept in every form the
// subscriptions need: the Clash proxy (the canonical form), the sing-box
// outbound and the share link. SingBox or Link is empty when that form
// cannot express the node.
type ExtNode struct {
	Name    string         `json:"name"`
	Type    string         `json:"type"`
	Clash   map[string]any `json:"clash"`
	SingBox map[string]any `json:"sing_box,omitempty"`
	Link    string         `json:"link,omitempty"`
}

// supportedTypes are the Clash proxy types converted to the other forms.
var supportedTypes = map[string]bool{"ss": true, "vmess": true, "vless": true, "trojan": true, "hysteria2": true}

// ParseSubscription reads a subscription body: Clash YAML (a proxies list)
// or share links, plain or base64. It returns the nodes it understands and
// how many entries it skipped.
func ParseSubscription(body []byte) ([]ExtNode, int, error) {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return nil, 0, fmt.Errorf("订阅内容是空的")
	}
	if strings.Contains(text, "proxies:") {
		var doc struct {
			Proxies []map[string]any `yaml:"proxies"`
		}
		if err := yaml.Unmarshal([]byte(text), &doc); err == nil && len(doc.Proxies) > 0 {
			out, skipped := []ExtNode{}, 0
			for _, p := range doc.Proxies {
				n, ok := fromClash(p, "")
				if !ok {
					skipped++
					continue
				}
				out = append(out, n)
			}
			return out, skipped, nil
		}
	}
	if !strings.Contains(text, "://") {
		decoded, err := decodeBase64(text)
		if err != nil {
			return nil, 0, fmt.Errorf("看不懂订阅内容：既不是 Clash YAML，也不是分享链接")
		}
		text = string(decoded)
	}
	out, skipped := []ExtNode{}, 0
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		p, ok := linkToClash(line)
		if !ok {
			skipped++
			continue
		}
		n, ok := fromClash(p, line)
		if !ok {
			skipped++
			continue
		}
		out = append(out, n)
	}
	if len(out) == 0 && skipped == 0 {
		return nil, 0, fmt.Errorf("订阅里没有节点")
	}
	return out, skipped, nil
}

func decodeBase64(s string) ([]byte, error) {
	s = strings.Join(strings.Fields(s), "")
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, fmt.Errorf("不是 base64")
}

// fromClash completes a Clash proxy into an ExtNode (false: unusable).
func fromClash(p map[string]any, link string) (ExtNode, bool) {
	name, typ := strings.TrimSpace(str(p["name"])), str(p["type"])
	if str(p["server"]) == "" || num(p["port"]) <= 0 || num(p["port"]) > 65535 || !supportedTypes[typ] {
		return ExtNode{}, false
	}
	if name == "" {
		name = hostPort(p)
	}
	p["name"] = name
	sb := clashToSingBox(p)
	if link == "" {
		link = clashToLink(p)
	}
	return ExtNode{Name: name, Type: typ, Clash: p, SingBox: sb, Link: link}, true
}

func str(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case int, int64, float64:
		return fmt.Sprint(t)
	}
	return ""
}

func num(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case string:
		n, _ := strconv.Atoi(t)
		return n
	}
	return 0
}

func boolean(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true" || t == "1"
	case int:
		return t != 0
	}
	return false
}

func sub(p map[string]any, key string) map[string]any {
	if m, ok := p[key].(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

// ---------------------------------------------------------------- links → Clash

func linkToClash(link string) (map[string]any, bool) {
	scheme, rest, ok := strings.Cut(link, "://")
	if !ok {
		return nil, false
	}
	switch strings.ToLower(scheme) {
	case "vmess":
		return vmessToClash(rest)
	case "ss":
		return ssToClash(rest)
	case "vless", "trojan", "hysteria2", "hy2":
		return urlToClash(strings.ToLower(scheme), link)
	}
	return nil, false
}

func vmessToClash(payload string) (map[string]any, bool) {
	raw, err := decodeBase64(strings.SplitN(payload, "#", 2)[0])
	if err != nil {
		return nil, false
	}
	var v map[string]any
	if json.Unmarshal(raw, &v) != nil {
		return nil, false
	}
	p := map[string]any{"name": str(v["ps"]), "type": "vmess", "server": str(v["add"]), "port": num(v["port"]), "uuid": str(v["id"]), "alterId": num(v["aid"]), "cipher": "auto", "udp": true}
	if c := str(v["scy"]); c != "" {
		p["cipher"] = c
	}
	if str(v["tls"]) == "tls" {
		p["tls"] = true
		if sni := str(v["sni"]); sni != "" {
			p["servername"] = sni
		}
		if fp := str(v["fp"]); fp != "" {
			p["client-fingerprint"] = fp
		}
	}
	return withTransport(p, str(v["net"]), str(v["path"]), str(v["host"]), str(v["path"]))
}

// withTransport sets network options; false for transports not converted.
func withTransport(p map[string]any, network, path, host, service string) (map[string]any, bool) {
	switch network {
	case "", "tcp":
	case "ws":
		p["network"] = "ws"
		opts := map[string]any{}
		if path != "" {
			opts["path"] = path
		}
		if host != "" {
			opts["headers"] = map[string]any{"Host": host}
		}
		p["ws-opts"] = opts
	case "grpc":
		p["network"] = "grpc"
		p["grpc-opts"] = map[string]any{"grpc-service-name": service}
	default:
		return nil, false
	}
	return p, true
}

func ssToClash(rest string) (map[string]any, bool) {
	body, name, _ := strings.Cut(rest, "#")
	name, _ = url.PathUnescape(name)
	if q := strings.Index(body, "?"); q >= 0 {
		if strings.Contains(body[q:], "plugin=") {
			return nil, false
		}
		body = body[:q]
	}
	body = strings.TrimSuffix(body, "/")
	var userinfo, hostport string
	if at := strings.LastIndex(body, "@"); at >= 0 {
		userinfo, hostport = body[:at], body[at+1:]
		if dec, err := decodeBase64(userinfo); err == nil && strings.Contains(string(dec), ":") {
			userinfo = string(dec)
		} else if u, err := url.PathUnescape(userinfo); err == nil {
			userinfo = u
		}
	} else { // legacy: base64(method:password@host:port)
		dec, err := decodeBase64(body)
		if err != nil {
			return nil, false
		}
		at := strings.LastIndex(string(dec), "@")
		if at < 0 {
			return nil, false
		}
		userinfo, hostport = string(dec[:at]), string(dec[at+1:])
	}
	method, password, ok := strings.Cut(userinfo, ":")
	host, port, err := net.SplitHostPort(hostport)
	if !ok || err != nil {
		return nil, false
	}
	return map[string]any{"name": name, "type": "ss", "server": host, "port": num(port), "cipher": method, "password": password, "udp": true}, true
}

func urlToClash(scheme, link string) (map[string]any, bool) {
	u, err := url.Parse(link)
	if err != nil || u.Hostname() == "" {
		return nil, false
	}
	q := u.Query()
	name := u.Fragment
	secret := u.User.Username()
	if pass, ok := u.User.Password(); ok && scheme != "vless" {
		secret += ":" + pass
	}
	p := map[string]any{"name": name, "server": u.Hostname(), "port": num(u.Port()), "udp": true}
	insecure := q.Get("allowInsecure") == "1" || q.Get("insecure") == "1"
	switch scheme {
	case "vless":
		p["type"], p["uuid"] = "vless", secret
		if f := q.Get("flow"); f != "" {
			p["flow"] = f
		}
		switch q.Get("security") {
		case "tls":
			p["tls"] = true
		case "reality":
			p["tls"] = true
			p["reality-opts"] = map[string]any{"public-key": q.Get("pbk"), "short-id": q.Get("sid")}
		}
		if sni := q.Get("sni"); sni != "" {
			p["servername"] = sni
		}
		if fp := q.Get("fp"); fp != "" {
			p["client-fingerprint"] = fp
		}
	case "trojan":
		p["type"], p["password"] = "trojan", secret
		if sni := q.Get("sni"); sni != "" {
			p["sni"] = sni
		}
	default: // hysteria2 / hy2
		p["type"], p["password"] = "hysteria2", secret
		if sni := q.Get("sni"); sni != "" {
			p["sni"] = sni
		}
		if o := q.Get("obfs"); o != "" {
			p["obfs"], p["obfs-password"] = o, q.Get("obfs-password")
		}
		if insecure {
			p["skip-cert-verify"] = true
		}
		return p, true
	}
	if insecure {
		p["skip-cert-verify"] = true
	}
	return withTransport(p, q.Get("type"), q.Get("path"), q.Get("host"), q.Get("serviceName"))
}

// ---------------------------------------------------------------- Clash → sing-box

func clashTLS(p map[string]any, sniKey string) map[string]any {
	tls := map[string]any{"enabled": true}
	if sni := str(p[sniKey]); sni != "" {
		tls["server_name"] = sni
	}
	if boolean(p["skip-cert-verify"]) {
		tls["insecure"] = true
	}
	if fp := str(p["client-fingerprint"]); fp != "" {
		tls["utls"] = map[string]any{"enabled": true, "fingerprint": fp}
	}
	if r := sub(p, "reality-opts"); len(r) > 0 {
		tls["reality"] = map[string]any{"enabled": true, "public_key": str(r["public-key"]), "short_id": str(r["short-id"])}
		if _, ok := tls["utls"]; !ok {
			tls["utls"] = map[string]any{"enabled": true, "fingerprint": "chrome"}
		}
	}
	return tls
}

// clashTransport is the sing-box transport (nil for plain TCP); ok is false
// for networks not converted.
func clashTransport(p map[string]any) (map[string]any, bool) {
	switch str(p["network"]) {
	case "", "tcp":
		return nil, true
	case "ws":
		ws := sub(p, "ws-opts")
		t := map[string]any{"type": "ws"}
		if path := str(ws["path"]); path != "" {
			t["path"] = path
		}
		if host := str(sub(ws, "headers")["Host"]); host != "" {
			t["headers"] = map[string]any{"Host": host}
		}
		return t, true
	case "grpc":
		return map[string]any{"type": "grpc", "service_name": str(sub(p, "grpc-opts")["grpc-service-name"])}, true
	}
	return nil, false
}

func clashToSingBox(p map[string]any) map[string]any {
	out := map[string]any{"tag": str(p["name"]), "server": str(p["server"]), "server_port": num(p["port"])}
	switch str(p["type"]) {
	case "ss":
		if str(p["plugin"]) != "" {
			return nil
		}
		out["type"], out["method"], out["password"] = "shadowsocks", str(p["cipher"]), str(p["password"])
		return out
	case "vmess":
		out["type"], out["uuid"], out["alter_id"], out["security"] = "vmess", str(p["uuid"]), num(p["alterId"]), "auto"
		if c := str(p["cipher"]); c != "" {
			out["security"] = c
		}
		if boolean(p["tls"]) {
			out["tls"] = clashTLS(p, "servername")
		}
	case "vless":
		out["type"], out["uuid"] = "vless", str(p["uuid"])
		if f := str(p["flow"]); f != "" {
			out["flow"] = f
		}
		if boolean(p["tls"]) {
			out["tls"] = clashTLS(p, "servername")
		}
	case "trojan":
		out["type"], out["password"] = "trojan", str(p["password"])
		out["tls"] = clashTLS(p, "sni")
	case "hysteria2":
		out["type"], out["password"] = "hysteria2", str(p["password"])
		out["tls"] = clashTLS(p, "sni")
		if o := str(p["obfs"]); o != "" {
			out["obfs"] = map[string]any{"type": o, "password": str(p["obfs-password"])}
		}
		return out
	default:
		return nil
	}
	t, ok := clashTransport(p)
	if !ok {
		return nil
	}
	if t != nil {
		out["transport"] = t
	}
	return out
}

// ---------------------------------------------------------------- Clash → link

func hostPort(p map[string]any) string {
	return net.JoinHostPort(str(p["server"]), strconv.Itoa(num(p["port"])))
}

func transportQuery(p map[string]any, q url.Values) {
	switch str(p["network"]) {
	case "ws":
		ws := sub(p, "ws-opts")
		q.Set("type", "ws")
		if path := str(ws["path"]); path != "" {
			q.Set("path", path)
		}
		if host := str(sub(ws, "headers")["Host"]); host != "" {
			q.Set("host", host)
		}
	case "grpc":
		q.Set("type", "grpc")
		q.Set("serviceName", str(sub(p, "grpc-opts")["grpc-service-name"]))
	default:
		q.Set("type", "tcp")
	}
}

func clashToLink(p map[string]any) string {
	name := url.PathEscape(str(p["name"]))
	switch str(p["type"]) {
	case "ss":
		if str(p["plugin"]) != "" {
			return ""
		}
		user := base64.RawURLEncoding.EncodeToString([]byte(str(p["cipher"]) + ":" + str(p["password"])))
		return "ss://" + user + "@" + hostPort(p) + "#" + name
	case "vmess":
		v := map[string]any{"v": "2", "ps": str(p["name"]), "add": str(p["server"]), "port": strconv.Itoa(num(p["port"])), "id": str(p["uuid"]), "aid": strconv.Itoa(num(p["alterId"])), "scy": str(p["cipher"]), "net": "tcp"}
		if n := str(p["network"]); n != "" {
			v["net"] = n
		}
		if ws := sub(p, "ws-opts"); len(ws) > 0 {
			v["path"], v["host"] = str(ws["path"]), str(sub(ws, "headers")["Host"])
		}
		if g := sub(p, "grpc-opts"); len(g) > 0 {
			v["path"] = str(g["grpc-service-name"])
		}
		if boolean(p["tls"]) {
			v["tls"], v["sni"] = "tls", str(p["servername"])
		}
		raw, _ := json.Marshal(v)
		return "vmess://" + base64.StdEncoding.EncodeToString(raw)
	case "vless":
		q := url.Values{"encryption": {"none"}}
		if f := str(p["flow"]); f != "" {
			q.Set("flow", f)
		}
		if r := sub(p, "reality-opts"); len(r) > 0 {
			q.Set("security", "reality")
			q.Set("pbk", str(r["public-key"]))
			q.Set("sid", str(r["short-id"]))
		} else if boolean(p["tls"]) {
			q.Set("security", "tls")
		}
		if sni := str(p["servername"]); sni != "" {
			q.Set("sni", sni)
		}
		if fp := str(p["client-fingerprint"]); fp != "" {
			q.Set("fp", fp)
		}
		transportQuery(p, q)
		return "vless://" + url.PathEscape(str(p["uuid"])) + "@" + hostPort(p) + "?" + q.Encode() + "#" + name
	case "trojan":
		q := url.Values{}
		if sni := str(p["sni"]); sni != "" {
			q.Set("sni", sni)
		}
		if boolean(p["skip-cert-verify"]) {
			q.Set("allowInsecure", "1")
		}
		transportQuery(p, q)
		return "trojan://" + url.PathEscape(str(p["password"])) + "@" + hostPort(p) + "?" + q.Encode() + "#" + name
	case "hysteria2":
		q := url.Values{}
		if sni := str(p["sni"]); sni != "" {
			q.Set("sni", sni)
		}
		if o := str(p["obfs"]); o != "" {
			q.Set("obfs", o)
			q.Set("obfs-password", str(p["obfs-password"]))
		}
		if boolean(p["skip-cert-verify"]) {
			q.Set("insecure", "1")
		}
		return "hysteria2://" + url.PathEscape(str(p["password"])) + "@" + hostPort(p) + "?" + q.Encode() + "#" + name
	}
	return ""
}
