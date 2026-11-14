package proxy

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"

	"github.com/leeska/TZA-Probe/internal/users"
)

// Subscription formats.
const (
	FormatClash   = "clash"   // Clash Meta / mihomo YAML
	FormatSingBox = "singbox" // sing-box 1.12+ client configuration
	FormatBase64  = "base64"  // share links, one per line, base64 (v2rayN and most others)
)

// Subscription modes: which traffic the generated rules send through the
// proxy.
const (
	ModeRule   = "rule"   // private and mainland China direct, the rest proxied
	ModeGlobal = "global" // everything proxied
)

// GroupSelect and GroupAuto are the policy groups the configurations make.
const (
	GroupSelect = "节点选择"
	GroupAuto   = "自动选择"
)

// FormatFor picks a format from ?format= or, without one, the client's
// User-Agent; unknown clients get base64.
func FormatFor(query, userAgent string) string {
	switch strings.ToLower(query) {
	case "clash", "mihomo", "meta":
		return FormatClash
	case "singbox", "sing-box":
		return FormatSingBox
	case "base64", "v2ray", "links":
		return FormatBase64
	}
	ua := strings.ToLower(userAgent)
	for _, name := range []string{"clash", "mihomo", "stash", "nyanpasu", "verge", "flclash"} {
		if strings.Contains(ua, name) {
			return FormatClash
		}
	}
	for _, name := range []string{"sing-box", "sfa/", "sfi/", "sfm/", "sft/"} {
		if strings.Contains(ua, name) {
			return FormatSingBox
		}
	}
	return FormatBase64
}

// Entry is one proxy in a subscription: an inbound with the name the client
// shows (unique within the subscription).
type Entry struct {
	Name    string
	Inbound Inbound
}

// MemberOf is the user as the inbound configs know them.
func MemberOf(u users.User) Member {
	return Member{ID: u.ID, Name: u.Name, UUID: u.ProxyUUID, Key: u.ProxyKey}
}

// Entries are the inbounds m may use and a client can reach (enabled, with a
// connection address), in order, named uniquely.
func Entries(inbounds []Inbound, m Member) []Entry {
	out := []Entry{}
	seen := map[string]int{}
	for _, in := range inbounds {
		if !in.Enabled || in.Address == "" || len(in.Members([]Member{m})) == 0 {
			continue
		}
		name := strings.TrimSpace(in.Name)
		if name == "" {
			name = in.Address + ":" + strconv.Itoa(in.Port)
		}
		seen[name]++
		if seen[name] > 1 {
			name = fmt.Sprintf("%s %d", name, seen[name])
		}
		out = append(out, Entry{Name: name, Inbound: in})
	}
	return out
}

// SingBoxCompatible drops what sing-box clients cannot use: xray 26.9.8+
// only accepts REALITY clients that offer an X25519MLKEM768 key share, which
// sing-box's uTLS fingerprints do not (SagerNet/sing-box#4520; relaxing
// minClientVer does not help). Shadowsocks and sing-box-hosted REALITY work.
func SingBoxCompatible(entries []Entry) []Entry {
	out := []Entry{}
	for _, e := range entries {
		if e.Inbound.Kernel == KernelXray && e.Inbound.Protocol == ProtocolVLESSReality {
			continue
		}
		out = append(out, e)
	}
	return out
}

// Options are what a subscription is made of besides the user's own
// inbounds: the rules (nil is the built-in 国内直连) and nodes from external
// subscriptions.
type Options struct {
	Rules []Rule
	Extra []ExtNode
}

// uniqueExtra renames external nodes whose names clash with the user's
// inbounds or each other.
func uniqueExtra(entries []Entry, extra []ExtNode) []ExtNode {
	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.Name] = true
	}
	out := make([]ExtNode, 0, len(extra))
	for _, n := range extra {
		name := n.Name
		for i := 2; seen[name]; i++ {
			name = fmt.Sprintf("%s %d", n.Name, i)
		}
		seen[name] = true
		n = rename(n, name)
		out = append(out, n)
	}
	return out
}

// Subscription renders what a client imports. It returns the body and its
// content type.
func Subscription(format string, opt Options, entries []Entry, m Member) ([]byte, string, error) {
	rules := opt.Rules
	if rules == nil {
		rules, _ = ParseRules(BuiltInTemplates[0].Rules)
	}
	extra := uniqueExtra(entries, opt.Extra)
	switch format {
	case FormatBase64:
		lines := []string{}
		for _, e := range entries {
			lines = append(lines, e.Inbound.NamedLink(m, e.Name))
		}
		for _, n := range extra {
			if n.Link != "" {
				lines = append(lines, n.Link)
			}
		}
		return []byte(base64.StdEncoding.EncodeToString([]byte(strings.Join(lines, "\n")))), "text/plain; charset=utf-8", nil
	case FormatClash:
		return clashConfig(rules, entries, extra, m), "text/yaml; charset=utf-8", nil
	case FormatSingBox:
		raw, err := json.MarshalIndent(singBoxClient(rules, SingBoxCompatible(entries), extra, m), "", "  ")
		return raw, "application/json; charset=utf-8", err
	}
	return nil, "", fmt.Errorf("未知订阅格式 %q", format)
}

// q quotes a YAML scalar. A JSON string is a valid YAML double-quoted
// scalar, which keeps names with any character safe.
func q(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

func clashProxy(e Entry, m Member) string {
	in := e.Inbound
	head := fmt.Sprintf("  - name: %s\n    server: %s\n    port: %d\n    udp: true\n", q(e.Name), q(in.Address), in.Port)
	switch in.Protocol {
	case ProtocolVLESSReality:
		return head + fmt.Sprintf("    type: vless\n    uuid: %s\n    network: tcp\n    tls: true\n    flow: %s\n    servername: %s\n    client-fingerprint: chrome\n    reality-opts:\n      public-key: %s\n      short-id: %s\n",
			q(m.UUID), VLESSFlow, q(in.Reality.ServerName), q(in.Reality.PublicKey), q(in.Reality.ShortID))
	default:
		return head + fmt.Sprintf("    type: ss\n    cipher: %s\n    password: %s\n", SSMethod, q(in.SS.ServerKey+":"+m.Key))
	}
}

func clashConfig(rules []Rule, entries []Entry, extra []ExtNode, m Member) []byte {
	var b strings.Builder
	b.WriteString("# TZA Probe 订阅：" + strings.ReplaceAll(m.Name, "\n", " ") + "\n")
	b.WriteString("mixed-port: 7890\nallow-lan: false\nmode: rule\nlog-level: warning\nipv6: true\nunified-delay: true\n")
	b.WriteString("dns:\n  enable: true\n  ipv6: true\n  enhanced-mode: fake-ip\n  fake-ip-filter:\n    - \"+.lan\"\n    - \"+.local\"\n  default-nameserver:\n    - 223.5.5.5\n    - 119.29.29.29\n  nameserver:\n    - https://223.5.5.5/dns-query\n    - https://doh.pub/dns-query\n")
	if usesChina(rules) {
		b.WriteString("  nameserver-policy:\n    \"geosite:geolocation-!cn\":\n      - https://1.1.1.1/dns-query\n      - https://dns.google/dns-query\n")
	}
	var proxies strings.Builder
	names := []string{}
	for _, e := range entries {
		proxies.WriteString(clashProxy(e, m))
		names = append(names, q(e.Name))
	}
	for _, n := range extra {
		// A JSON object is a valid YAML flow mapping: the external proxy
		// goes in exactly as its source wrote it.
		raw, _ := json.Marshal(n.Clash)
		proxies.WriteString("  - " + string(raw) + "\n")
		names = append(names, q(n.Name))
	}
	if len(names) == 0 {
		// A group needs at least one member; with no usable node the client
		// still loads and simply goes direct.
		names = []string{"DIRECT"}
		b.WriteString("proxies: []\n")
	} else {
		b.WriteString("proxies:\n" + proxies.String())
	}
	b.WriteString("proxy-groups:\n")
	choices := append([]string{q(GroupAuto)}, names...)
	if names[0] != "DIRECT" {
		choices = append(choices, "DIRECT")
	}
	fmt.Fprintf(&b, "  - name: %s\n    type: select\n    proxies: [%s]\n", q(GroupSelect), strings.Join(choices, ", "))
	fmt.Fprintf(&b, "  - name: %s\n    type: url-test\n    url: https://www.gstatic.com/generate_204\n    interval: 300\n    tolerance: 50\n    proxies: [%s]\n", q(GroupAuto), strings.Join(names, ", "))
	b.WriteString("rules:\n")
	// The whole rule is one quoted scalar: "MATCH,节点选择", not
	// MATCH,"节点选择" (which YAML reads with the quotes in the name).
	for _, r := range clashRules(rules) {
		b.WriteString("  - " + q(r) + "\n")
	}
	return []byte(b.String())
}

func singBoxOutbound(e Entry, m Member) obj {
	in := e.Inbound
	host := in.Address
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	switch in.Protocol {
	case ProtocolVLESSReality:
		return obj{"type": "vless", "tag": e.Name, "server": host, "server_port": in.Port, "uuid": m.UUID, "flow": VLESSFlow,
			"tls": obj{"enabled": true, "server_name": in.Reality.ServerName,
				"utls":    obj{"enabled": true, "fingerprint": "chrome"},
				"reality": obj{"enabled": true, "public_key": in.Reality.PublicKey, "short_id": in.Reality.ShortID}}}
	default:
		return obj{"type": "shadowsocks", "tag": e.Name, "server": host, "server_port": in.Port, "method": SSMethod, "password": in.SS.ServerKey + ":" + m.Key}
	}
}

// ruleSet is a remote binary rule set from SagerNet's published branches,
// fetched through the proxy (GitHub is often unreachable directly).
func ruleSet(tag, repo string) obj {
	return obj{"type": "remote", "tag": tag, "format": "binary", "url": "https://raw.githubusercontent.com/SagerNet/" + repo + "/rule-set/" + tag + ".srs", "download_detour": GroupSelect}
}

func singBoxClient(rules []Rule, entries []Entry, extra []ExtNode, m Member) obj {
	names := []string{}
	outbounds := []any{}
	for _, e := range entries {
		names = append(names, e.Name)
		outbounds = append(outbounds, singBoxOutbound(e, m))
	}
	for _, n := range extra {
		if n.SingBox != nil {
			names = append(names, n.Name)
			outbounds = append(outbounds, n.SingBox)
		}
	}
	auto := append([]string{}, names...)
	if len(auto) == 0 {
		auto = []string{"direct"}
	}
	groups := []any{
		obj{"type": "selector", "tag": GroupSelect, "outbounds": append(append([]string{GroupAuto}, names...), "direct"), "default": GroupAuto},
		obj{"type": "urltest", "tag": GroupAuto, "outbounds": auto, "url": "https://www.gstatic.com/generate_204", "interval": "5m", "tolerance": 50},
	}
	outbounds = append(append(groups, outbounds...), obj{"type": "direct", "tag": "direct"})
	dns := obj{
		"servers": []any{
			obj{"type": "https", "tag": "remote", "server": "1.1.1.1", "detour": GroupSelect},
			obj{"type": "https", "tag": "local", "server": "223.5.5.5"},
		},
		"final":    "remote",
		"strategy": "prefer_ipv4",
	}
	routeRules, sets, final := singBoxRoute(rules)
	all := append([]any{obj{"action": "sniff"}, obj{"protocol": "dns", "action": "hijack-dns"}}, routeRules...)
	route := obj{"auto_detect_interface": true, "final": final, "default_domain_resolver": "local", "rules": all}
	if len(sets) > 0 {
		route["rule_set"] = sets
	}
	if usesChina(rules) {
		if !slices.ContainsFunc(sets, func(s any) bool { return s.(obj)["tag"] == "geosite-cn" }) {
			route["rule_set"] = append(sets, ruleSet("geosite-cn", "sing-geosite"))
		}
		dns["rules"] = []any{obj{"rule_set": []string{"geosite-cn"}, "server": "local"}}
	}
	return obj{
		"log": obj{"level": "warn"},
		"dns": dns,
		"inbounds": []any{
			obj{"type": "tun", "tag": "tun-in", "address": []string{"172.19.0.1/30", "fdfe:dcba:9876::1/126"}, "auto_route": true, "strict_route": true},
			obj{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": 2080},
		},
		"outbounds":    outbounds,
		"route":        route,
		"experimental": obj{"cache_file": obj{"enabled": true}},
	}
}
