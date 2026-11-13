package proxy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleInbounds(t *testing.T) ([]Inbound, []Member) {
	t.Helper()
	vless := Inbound{ID: "v1", NodeID: "n", Name: "直连", Kernel: KernelXray, Protocol: ProtocolVLESSReality, Port: 443, Enabled: true, Address: "203.0.113.5", Reality: &Reality{Dest: "www.example.org:443"}}
	ss := Inbound{ID: "s1", NodeID: "n", Name: "中转", Kernel: KernelXray, Protocol: ProtocolSS2022, Port: 8388, Enabled: true, Address: "example.com", UserIDs: []string{"u2"}}
	for _, in := range []*Inbound{&vless, &ss} {
		if err := in.Prepare(nil); err != nil {
			t.Fatal(err)
		}
	}
	members := []Member{{ID: "u1", Name: "甲", UUID: "11111111-2222-4333-8444-555555555555", Key: "AAAAAAAAAAAAAAAAAAAAAA=="}, {ID: "u2", Name: "乙", UUID: "66666666-7777-4888-9999-000000000000", Key: "BBBBBBBBBBBBBBBBBBBBBB=="}}
	return []Inbound{vless, ss}, members
}

func TestPrepare(t *testing.T) {
	if (&Inbound{Name: "x", NodeID: "n", Kernel: KernelSingBox, Protocol: ProtocolVLESSReality, Port: 443}).Prepare(nil) == nil {
		t.Fatal("Reality 目标必填")
	}
	in := Inbound{Name: "x", NodeID: "n", Kernel: KernelSingBox, Protocol: ProtocolVLESSReality, Port: 443, Reality: &Reality{Dest: "www.example.org:443"}}
	if err := in.Prepare(nil); err != nil {
		t.Fatal(err)
	}
	if in.Reality.Dest != "www.example.org:443" || in.Reality.ServerName != "www.example.org" || len(in.Reality.ShortID) != 8 || in.Reality.PrivateKey == "" || in.SS != nil {
		t.Fatalf("%+v", in.Reality)
	}
	next := in
	next.Reality = &Reality{Dest: "www.apple.com:443"}
	if err := next.Prepare(&in); err != nil || next.Reality.PrivateKey != in.Reality.PrivateKey || next.Reality.ServerName != "www.apple.com" {
		t.Fatalf("编辑时保留密钥：%+v %v", next.Reality, err)
	}
	for _, bad := range []Inbound{
		{Name: "", NodeID: "n", Kernel: KernelXray, Protocol: ProtocolSS2022, Port: 1},
		{Name: "x", NodeID: "n", Kernel: "v2ray", Protocol: ProtocolSS2022, Port: 1},
		{Name: "x", NodeID: "n", Kernel: KernelXray, Protocol: "trojan", Port: 1},
		{Name: "x", NodeID: "n", Kernel: KernelXray, Protocol: ProtocolSS2022, Port: 70000},
		{Name: "x", NodeID: "n", Kernel: KernelXray, Protocol: ProtocolSS2022, Port: 1, Address: "bad host"},
		{Name: "x", NodeID: "n", Kernel: KernelXray, Protocol: ProtocolVLESSReality, Port: 1, Reality: &Reality{Dest: "nohost"}},
	} {
		if bad.Prepare(nil) == nil {
			t.Fatalf("应拒绝 %+v", bad)
		}
	}
}

func TestConfigAndLinks(t *testing.T) {
	inbounds, members := sampleInbounds(t)
	for _, kernel := range []string{KernelXray, KernelSingBox} {
		raw, err := Config(kernel, inbounds, members)
		if err != nil {
			t.Fatal(err)
		}
		again, _ := Config(kernel, inbounds, members)
		if Hash(raw) != Hash(again) {
			t.Fatal("同样的输入要得到同样的配置")
		}
		var parsed map[string]any
		if err := json.Unmarshal(raw, &parsed); err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		if !strings.Contains(text, members[0].UUID) || strings.Count(text, members[1].Key) != 1 || strings.Contains(text, members[0].Key) {
			t.Fatalf("%s：VLESS 给所有人，SS 只给 u2\n%s", kernel, text)
		}
		if dir := os.Getenv("PROXY_CONFIG_DUMP"); dir != "" {
			os.WriteFile(filepath.Join(dir, kernel+".json"), raw, 0644)
		}
	}
	vless := inbounds[0].ShareLink(members[0])
	if !strings.HasPrefix(vless, "vless://"+members[0].UUID+"@203.0.113.5:443?") || !strings.Contains(vless, "pbk="+inbounds[0].Reality.PublicKey) || !strings.Contains(vless, "flow=xtls-rprx-vision") || !strings.Contains(vless, "security=reality") {
		t.Fatal(vless)
	}
	ss := inbounds[1].ShareLink(members[1])
	if !strings.HasPrefix(ss, "ss://2022-blake3-aes-128-gcm:") || !strings.Contains(ss, "@example.com:8388#") {
		t.Fatal(ss)
	}
	v6 := inbounds[1]
	v6.Address = "2001:db8::1"
	if !strings.Contains(v6.ShareLink(members[1]), "@[2001:db8::1]:8388") {
		t.Fatal("IPv6 地址要加方括号")
	}
}
