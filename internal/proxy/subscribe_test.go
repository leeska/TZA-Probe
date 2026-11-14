package proxy

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestFormatFor(t *testing.T) {
	for _, c := range []struct{ query, ua, want string }{
		{"", "clash.meta/1.19", FormatClash},
		{"", "ClashVerge/2.0", FormatClash},
		{"", "mihomo/1.19.0", FormatClash},
		{"", "SFA/1.12.0 (Android)", FormatSingBox},
		{"", "sing-box 1.14", FormatSingBox},
		{"", "v2rayN/7.0", FormatBase64},
		{"", "", FormatBase64},
		{"singbox", "clash", FormatSingBox},
		{"base64", "clash", FormatBase64},
	} {
		if got := FormatFor(c.query, c.ua); got != c.want {
			t.Errorf("FormatFor(%q, %q) = %s, want %s", c.query, c.ua, got, c.want)
		}
	}
}

func subscriptionFixture() ([]Entry, Member) {
	m := Member{ID: "u1", Name: "甲", UUID: "3124fca9-8531-4472-8248-ac88e3949159", Key: "YWJjZGVmZ2hpamtsbW5vcA=="}
	inbounds := []Inbound{
		{ID: "1", Name: "日本", Protocol: ProtocolVLESSReality, Port: 443, Enabled: true, Address: "jp.example.com", Reality: &Reality{Dest: "www.example.org:443", ServerName: "www.example.org", PublicKey: "ssRIQMO58LaXlRuo4Zl8BbboF3PGFf27BOGluKpyEl8", ShortID: "067ac0e6"}},
		{ID: "2", Name: "日本", Protocol: ProtocolSS2022, Port: 8443, Enabled: true, Address: "2001:db8::1", SS: &SS{ServerKey: "MTIzNDU2Nzg5MDEyMzQ1Ng=="}},
		{ID: "3", Name: "没地址", Protocol: ProtocolSS2022, Port: 9000, Enabled: true, SS: &SS{ServerKey: "x"}},
		{ID: "4", Name: "停用", Protocol: ProtocolSS2022, Port: 9001, Address: "a.example", SS: &SS{ServerKey: "x"}},
		{ID: "5", Name: "别人的", Protocol: ProtocolSS2022, Port: 9002, Enabled: true, Address: "a.example", UserIDs: []string{"u2"}, SS: &SS{ServerKey: "x"}},
	}
	return Entries(inbounds, m), m
}

func TestEntries(t *testing.T) {
	entries, _ := subscriptionFixture()
	if len(entries) != 2 || entries[0].Name != "日本" || entries[1].Name != "日本 2" {
		t.Fatalf("只列出能用、能连的入站，名字不重复：%+v", entries)
	}
}

func TestSubscriptionFormats(t *testing.T) {
	entries, m := subscriptionFixture()
	body, _, err := Subscription(FormatBase64, Options{}, entries, m)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(string(body))
	lines := strings.Split(string(raw), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "vless://"+m.UUID+"@jp.example.com:443?") || !strings.HasPrefix(lines[1], "ss://"+SSMethod+":") || !strings.Contains(lines[1], "@[2001:db8::1]:8443#") {
		t.Fatalf("%q", lines)
	}
	body, _, _ = Subscription(FormatClash, Options{}, entries, m)
	for _, want := range []string{`name: "日本 2"`, "type: ss", "cipher: " + SSMethod, `password: "MTIzNDU2Nzg5MDEyMzQ1Ng==:` + m.Key + `"`, "reality-opts:", "GEOIP,CN,DIRECT", `- "MATCH,节点选择"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("Clash 缺 %q:\n%s", want, body)
		}
	}
	body, _, _ = Subscription(FormatClash, Options{Rules: mustRules(t, BuiltInTemplates[1].Rules)}, entries, m)
	if strings.Contains(string(body), "GEOIP,CN") {
		t.Error("全局模式不分流")
	}
	body, _, _ = Subscription(FormatSingBox, Options{}, entries, m)
	var doc struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(body, &doc); err != nil || len(doc.Outbounds) != 5 || doc.Outbounds[2]["uuid"] != m.UUID || doc.Outbounds[3]["server"] != "2001:db8::1" {
		t.Fatalf("sing-box: %v %s", err, body)
	}
	// xray 的 REALITY 入站 sing-box 客户端连不上，不放进 sing-box 订阅。
	entries[0].Inbound.Kernel = KernelXray
	body, _, _ = Subscription(FormatSingBox, Options{}, entries, m)
	if strings.Contains(string(body), m.UUID) || !strings.Contains(string(body), `"日本 2"`) {
		t.Fatalf("%s", body)
	}
	// 没有能用的入站时也要是能加载的配置。
	if body, _, _ = Subscription(FormatClash, Options{}, nil, m); !strings.Contains(string(body), "proxies: [DIRECT]") {
		t.Fatalf("%s", body)
	}
}

func mustRules(t *testing.T, text string) []Rule {
	t.Helper()
	r, err := ParseRules(text)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
