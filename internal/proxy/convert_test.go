package proxy

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

const vmessJSON = `{"v":"2","ps":"香港 01","add":"hk.example.com","port":"443","id":"11111111-2222-3333-4444-555555555555","aid":"0","scy":"auto","net":"ws","host":"cdn.example.com","path":"/ray","tls":"tls","sni":"cdn.example.com"}`

var sampleLinks = []string{
	"vmess://" + base64.StdEncoding.EncodeToString([]byte(vmessJSON)),
	"vless://3124fca9-8531-4472-8248-ac88e3949159@jp.example.com:443?encryption=none&flow=xtls-rprx-vision&security=reality&sni=www.example.org&fp=chrome&pbk=PUBKEY&sid=abcd&type=tcp#%E6%97%A5%E6%9C%AC",
	"trojan://p%40ss@us.example.com:443?sni=us.example.com&type=ws&path=%2Fws&host=us.example.com#US",
	"ss://" + base64.RawURLEncoding.EncodeToString([]byte("2022-blake3-aes-128-gcm:MTIzNDU2Nzg5MDEyMzQ1Ng==:YWJjZGVmZ2hpamtsbW5vcA==")) + "@1.2.3.4:8388#SS",
	"ss://YWVzLTI1Ni1nY206cGFzcw@[2001:db8::1]:8388#v6",
	"hysteria2://secret@hy.example.com:8443?sni=hy.example.com&obfs=salamander&obfs-password=o&insecure=1#HY",
	"tuic://x@y:1#unsupported",
}

func TestParseLinks(t *testing.T) {
	body := base64.StdEncoding.EncodeToString([]byte(strings.Join(sampleLinks, "\n")))
	nodes, skipped, err := ParseSubscription([]byte(body))
	if err != nil || len(nodes) != 6 || skipped != 1 {
		t.Fatalf("%d nodes, %d skipped, %v", len(nodes), skipped, err)
	}
	byName := map[string]ExtNode{}
	for _, n := range nodes {
		byName[n.Name] = n
		if n.SingBox == nil || n.Link == "" {
			t.Errorf("%s 缺 sing-box 或链接", n.Name)
		}
	}
	vm := byName["香港 01"]
	if vm.Clash["network"] != "ws" || vm.SingBox["transport"].(map[string]any)["path"] != "/ray" || vm.SingBox["tls"].(map[string]any)["server_name"] != "cdn.example.com" {
		t.Fatalf("vmess：%+v", vm)
	}
	vl := byName["日本"]
	reality := vl.SingBox["tls"].(map[string]any)["reality"].(map[string]any)
	if reality["public_key"] != "PUBKEY" || vl.SingBox["flow"] != "xtls-rprx-vision" || vl.Clash["reality-opts"] == nil {
		t.Fatalf("vless：%+v", vl)
	}
	if tr := byName["US"]; tr.Clash["password"] != "p@ss" || tr.SingBox["transport"].(map[string]any)["type"] != "ws" {
		t.Fatalf("trojan：%+v", tr)
	}
	if ss := byName["SS"]; ss.Clash["cipher"] != "2022-blake3-aes-128-gcm" || ss.Clash["password"] != "MTIzNDU2Nzg5MDEyMzQ1Ng==:YWJjZGVmZ2hpamtsbW5vcA==" {
		t.Fatalf("ss：%+v", ss)
	}
	if v6 := byName["v6"]; v6.Clash["server"] != "2001:db8::1" || v6.Clash["cipher"] != "aes-256-gcm" {
		t.Fatalf("ss v6：%+v", v6)
	}
	if hy := byName["HY"]; hy.SingBox["obfs"].(map[string]any)["password"] != "o" || hy.SingBox["tls"].(map[string]any)["insecure"] != true {
		t.Fatalf("hysteria2：%+v", hy)
	}
}

func TestParseClashYAML(t *testing.T) {
	body := `
proxies:
  - {name: "台湾 A", type: trojan, server: tw.example.com, port: 443, password: pw, sni: tw.example.com}
  - name: 新加坡
    type: vless
    server: sg.example.com
    port: 443
    uuid: 3124fca9-8531-4472-8248-ac88e3949159
    tls: true
    network: grpc
    grpc-opts: {grpc-service-name: svc}
  - {name: ws-ss, type: ss, server: a.example, port: 1, cipher: aes-128-gcm, password: x, plugin: v2ray-plugin}
  - {name: tuic, type: tuic, server: a.example, port: 1}
proxy-groups: []
`
	nodes, skipped, err := ParseSubscription([]byte(body))
	if err != nil || len(nodes) != 3 || skipped != 1 {
		t.Fatalf("%d %d %v", len(nodes), skipped, err)
	}
	sg := nodes[1]
	if sg.SingBox["transport"].(map[string]any)["service_name"] != "svc" || !strings.HasPrefix(sg.Link, "vless://") || !strings.Contains(sg.Link, "serviceName=svc") {
		t.Fatalf("%+v", sg)
	}
	if nodes[2].SingBox != nil || nodes[2].Link != "" {
		t.Fatal("带插件的 SS 不能转换，只保留 Clash 形式")
	}
	// 生成的链接再解析一遍得到同样的节点。
	again, ok := linkToClash(nodes[0].Link)
	if !ok || again["password"] != "pw" || again["sni"] != "tw.example.com" || again["name"] != "台湾 A" {
		t.Fatalf("%v %+v", ok, again)
	}
}

func TestExtraAndSubscription(t *testing.T) {
	nodes, _, _ := ParseSubscription([]byte(strings.Join(sampleLinks[:3], "\n")))
	sources := []Source{
		{Name: "A", Enabled: true, Prefix: "机场A", Nodes: nodes},
		{Name: "B", Enabled: true, UserIDs: []string{"other"}, Nodes: nodes},
		{Name: "C", Enabled: false, Nodes: nodes},
	}
	extra := Extra(sources, "u1")
	if len(extra) != 3 || extra[0].Name != "机场A 香港 01" || extra[0].SingBox["tag"] != "机场A 香港 01" || extra[0].Clash["name"] != "机场A 香港 01" {
		t.Fatalf("%+v", extra)
	}
	entries, m := subscriptionFixture()
	clash, _, _ := Subscription(FormatClash, Options{Extra: extra}, entries, m)
	if !strings.Contains(string(clash), `"name":"机场A 日本"`) || !strings.Contains(string(clash), `"机场A US"`) {
		t.Fatalf("%s", clash)
	}
	sb, _, _ := Subscription(FormatSingBox, Options{Extra: extra}, entries, m)
	var doc struct{ Outbounds []map[string]any }
	json.Unmarshal(sb, &doc)
	tags := []string{}
	for _, o := range doc.Outbounds {
		tags = append(tags, o["tag"].(string))
	}
	if !strings.Contains(strings.Join(tags, ","), "机场A 香港 01") {
		t.Fatal(tags)
	}
	b64, _, _ := Subscription(FormatBase64, Options{Extra: extra}, entries, m)
	raw, _ := base64.StdEncoding.DecodeString(string(b64))
	if !strings.Contains(string(raw), "#%E6%9C%BA%E5%9C%BAA%20US") {
		t.Fatalf("%s", raw)
	}
	// 和自己的入站重名时自动加编号。
	clash, _, _ = Subscription(FormatClash, Options{Extra: []ExtNode{rename(nodes[1], "日本")}}, entries, m)
	if !strings.Contains(string(clash), `"name":"日本 3"`) {
		t.Fatalf("%s", clash)
	}
}
