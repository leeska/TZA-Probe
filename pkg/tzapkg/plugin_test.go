package tzapkg

import (
	"archive/zip"
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

const feishu = `{
  "id": "feishu-test", "type": "plugin", "kind": "notify", "name": "飞书", "version": "1.0.0",
  "notify": {
    "hosts": ["open.feishu.cn", "*.larksuite.com"],
    "settings": [
      {"key": "webhook", "label": "Webhook", "type": "url", "required": true, "secret": true},
      {"key": "secret", "label": "签名密钥", "type": "secret"},
      {"key": "level", "label": "级别", "type": "select", "default": "4", "options": [{"value": "3", "label": "普通"}, {"value": "4", "label": "高"}]}
    ],
    "computed": [
      {"name": "ts", "value": "{{now.unix}}"},
      {"name": "sign", "hmac_sha256": {"key": "{{computed.ts}}\n{{settings.secret}}", "message": ""}, "when": "settings.secret"}
    ],
    "request": {
      "method": "POST", "url": "{{settings.webhook}}",
      "headers": {"X-Level": "{{settings.level}}", "X-Sign?": "{{computed.sign}}"},
      "body": {"msg_type": "text", "content": {"text": "{{event.title}}\n{{event.message|truncate:10}}"},
               "timestamp?": "{{computed.sign|trim}}{{computed.ts}}", "sign?": "{{computed.sign}}", "prio": "{{settings.level|number}}", "fixed": 7}
    },
    "success": {"json": {"path": "code", "equals": [0, "0"]}}
  }
}`

func TestNotifyRender(t *testing.T) {
	p, err := ParsePlugin([]byte(feishu))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.CheckSettings(map[string]string{"webhook": "https://evil.example/hook"}); err == nil {
		t.Fatal("undeclared host accepted")
	}
	if _, err := p.CheckSettings(map[string]string{"webhook": "http://open.feishu.cn/x"}); err == nil {
		t.Fatal("http accepted for a fixed host list")
	}
	if _, err := p.CheckSettings(map[string]string{}); err == nil {
		t.Fatal("missing required setting accepted")
	}
	settings, err := p.CheckSettings(map[string]string{"webhook": " https://open.feishu.cn/open-apis/bot/v2/hook/abc ", "secret": "s3cret", "junk": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if settings["level"] != "4" || settings["junk"] != "" {
		t.Fatalf("settings normalisation: %v", settings)
	}
	now := time.Unix(1700000000, 0)
	req, err := p.RenderNotify(settings, map[string]string{"title": "TZA Probe · 告警触发", "message": "CPU 使用率超过阈值很久了"}, now)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte("1700000000\ns3cret"))
	wantSign := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	var body map[string]any
	json.Unmarshal(req.Body, &body)
	if req.URL != "https://open.feishu.cn/open-apis/bot/v2/hook/abc" || req.Method != "POST" {
		t.Fatalf("url %s", req.URL)
	}
	if body["sign"] != wantSign || req.Headers.Get("X-Sign") != wantSign || body["prio"] != float64(4) || body["fixed"] != float64(7) {
		t.Fatalf("body %s", req.Body)
	}
	if text := body["content"].(map[string]any)["text"]; text != "TZA Probe · 告警触发\nCPU 使用率超过…" {
		t.Fatalf("text %q", text)
	}

	// Without the optional secret the signed members disappear.
	delete(settings, "secret")
	req, _ = p.RenderNotify(settings, map[string]string{}, now)
	body = map[string]any{}
	json.Unmarshal(req.Body, &body)
	if _, ok := body["sign"]; ok || req.Headers.Get("X-Sign") != "" {
		t.Fatalf("optional members kept: %s", req.Body)
	}
	if body["timestamp"] != "1700000000" {
		t.Fatalf("timestamp %v", body["timestamp"])
	}

	if err := p.CheckResponse(200, []byte(`{"code":0}`)); err != nil {
		t.Fatal(err)
	}
	if err := p.CheckResponse(200, []byte(`{"code":"0"}`)); err != nil {
		t.Fatal(err)
	}
	if err := p.CheckResponse(200, []byte(`{"code":19021,"msg":"sign match fail"}`)); err == nil {
		t.Fatal("rejected answer counted as success")
	}
	if err := p.CheckResponse(500, []byte(`{"code":0}`)); err == nil {
		t.Fatal("HTTP 500 counted as success")
	}
}

func TestNotifyWildcardAndForm(t *testing.T) {
	p, err := ParsePlugin([]byte(`{"id":"gotify-test","type":"plugin","kind":"notify","name":"Gotify",
		"notify":{"hosts":["*"],"settings":[{"key":"server","label":"服务器","type":"url","required":true},{"key":"token","label":"Token","type":"secret","required":true}],
		"request":{"url":"{{settings.server}}/message?token={{settings.token|urlencode}}","body_type":"form","body":{"title":"{{event.title}}","extras?":"{{event.node}}"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	s, err := p.CheckSettings(map[string]string{"server": "http://10.0.0.5:8080", "token": "a&b"})
	if err != nil {
		t.Fatal(err)
	}
	req, err := p.RenderNotify(s, map[string]string{"title": "标题"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if req.URL != "http://10.0.0.5:8080/message?token=a%26b" || string(req.Body) != "title="+url.QueryEscape("标题") {
		t.Fatalf("%s %s", req.URL, req.Body)
	}
}

func TestPluginRejects(t *testing.T) {
	base := func(notify string) string {
		return `{"id":"bad-test","type":"plugin","kind":"notify","name":"x","notify":` + notify + `}`
	}
	cases := map[string]string{
		"unknown variable":    base(`{"hosts":["a.com"],"request":{"url":"https://a.com/{{event.password}}"}}`),
		"undeclared setting":  base(`{"hosts":["a.com"],"request":{"url":"https://a.com/{{settings.nope}}"}}`),
		"computed before def": base(`{"hosts":["a.com"],"computed":[{"name":"a","value":"{{computed.b}}"}],"request":{"url":"https://a.com"}}`),
		"unknown filter":      base(`{"hosts":["a.com"],"request":{"url":"https://a.com/{{now.unix|exec}}"}}`),
		"forbidden header":    base(`{"hosts":["a.com"],"request":{"url":"https://a.com","headers":{"Host":"b.com"}}}`),
		"wildcard no url":     base(`{"hosts":["*"],"request":{"url":"https://a.com"}}`),
		"bad host":            base(`{"hosts":["https://a.com"],"request":{"url":"https://a.com"}}`),
		"unclosed":            base(`{"hosts":["a.com"],"request":{"url":"https://a.com/{{now.unix"}}`),
		"unknown field":       base(`{"hosts":["a.com"],"script":"x","request":{"url":"https://a.com"}}`),
		"reserved id":         `{"id":"telegram","type":"plugin","kind":"notify","name":"x","notify":{"hosts":["a.com"],"request":{"url":"https://a.com"}}}`,
		"bad kind":            `{"id":"bad-test","type":"plugin","kind":"script","name":"x"}`,
		"targets bad family":  `{"id":"bad-test","type":"plugin","kind":"targets","name":"x","targets":[{"id":"a","family":"ipv4","region":"东京","carrier":"其他","host":"2001:db8::1","port":80}]}`,
		"targets bad carrier": `{"id":"bad-test","type":"plugin","kind":"targets","name":"x","targets":[{"id":"a","family":"ipv4","region":"东京","carrier":"NTT","host":"1.1.1.1","port":80}]}`,
	}
	for name, raw := range cases {
		if _, err := ParsePlugin([]byte(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestRenderedHostEnforced(t *testing.T) {
	// A url setting is checked, but the template may still build a host from
	// other text; the rendered URL is checked again.
	p, err := ParsePlugin([]byte(`{"id":"sneaky-test","type":"plugin","kind":"notify","name":"x",
		"notify":{"hosts":["a.com"],"settings":[{"key":"sub","label":"子域","type":"text"}],"request":{"url":"https://{{settings.sub}}/x"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.RenderNotify(map[string]string{"sub": "evil.com"}, nil, time.Now()); err == nil {
		t.Fatal("rendered URL outside hosts accepted")
	}
	if _, err := p.RenderNotify(map[string]string{"sub": "a.com"}, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestInspect(t *testing.T) {
	zipOf := func(files map[string]string) []byte {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for name, body := range files {
			w, _ := zw.Create(name)
			w.Write([]byte(body))
		}
		zw.Close()
		return buf.Bytes()
	}
	info, err := Inspect(zipOf(map[string]string{"feishu/plugin.json": feishu}))
	if err != nil || info.Type != TypePlugin || info.Kind != KindNotify || len(info.Permissions) == 0 || !strings.Contains(info.Permissions[0], "open.feishu.cn") {
		t.Fatalf("plugin inspect: %+v %v", info, err)
	}
	if _, err := Inspect(zipOf(map[string]string{"plugin.json": feishu, "x.css": "a{}"})); err == nil {
		t.Fatal("plugin with extra files accepted")
	}
	theme := `{"id":"t-one","name":"T","version":"1.0.0","chrome":{"light":"#ffffff","dark":"#000000"},"swatch":["#ffffff","#ffffff","#000000"],"styles":["a.css"]}`
	if info, err := Inspect(zipOf(map[string]string{"theme.json": theme, "a.css": "a{}"})); err != nil || info.Type != TypeSkin {
		t.Fatalf("theme inspect: %+v %v", info, err)
	}
	targets := `{"id":"jp-targets","type":"plugin","kind":"targets","name":"日本目标","targets":[
		{"id":"tyo-v4","family":"ipv4","region":"东京","carrier":"其他","host":"1.1.1.1","port":443},
		{"id":"tyo-dns","family":"ipv6","region":"东京","carrier":"其他","host":"example.jp","port":443}]}`
	if info, err := Inspect(zipOf(map[string]string{"plugin.json": targets})); err != nil || info.Kind != KindTargets || !strings.Contains(info.Permissions[0], "2 个") {
		t.Fatalf("targets inspect: %+v %v", info, err)
	}
}

func TestSkinSettings(t *testing.T) {
	base := `{"id":"%s","name":"x","chrome":{"light":"#eef0e2","dark":"#141811"},"swatch":["#eef0e2","#fbfaf3","#5f8f3a"],"styles":["theme.css"],"settings":%s}`
	m, err := Parse([]byte(fmt.Sprintf(base, "matcha", `[{"id":"density","type":"choice","label":"密度","options":[{"value":"cozy","label":"舒适"},{"value":"compact","label":"紧凑"}]},{"id":"paper","type":"toggle","label":"纸纹"}]`)))
	if err != nil {
		t.Fatal(err)
	}
	if m.Settings[0].Default != "cozy" || m.Settings[1].Default != "off" {
		t.Errorf("defaults not filled: %+v", m.Settings)
	}
	if _, err := Parse([]byte(fmt.Sprintf(base, "matcha", `[{"id":"addons","type":"addons","label":"附加包"}]`))); err != nil {
		t.Errorf("any skin may list its add-ons: %v", err)
	}
	for name, raw := range map[string]string{
		"two addon menus":     `[{"id":"a","type":"addons","label":"x"},{"id":"b","type":"addons","label":"y"}]`,
		"addons with options": `[{"id":"a","type":"addons","label":"x","options":[{"value":"v","label":"v"},{"value":"w","label":"w"}]}]`,
		"unknown type":        `[{"id":"x","type":"slider","label":"x"}]`,
		"bad id":              `[{"id":"Bad!","type":"toggle","label":"x"}]`,
		"dup id":              `[{"id":"a","type":"toggle","label":"x"},{"id":"a","type":"toggle","label":"y"}]`,
		"one option":          `[{"id":"a","type":"choice","label":"x","options":[{"value":"v","label":"v"}]}]`,
		"bad default":         `[{"id":"a","type":"choice","label":"x","default":"z","options":[{"value":"v","label":"v"},{"value":"w","label":"w"}]}]`,
		"toggle default":      `[{"id":"a","type":"toggle","label":"x","default":"yes"}]`,
		"no label":            `[{"id":"a","type":"toggle"}]`,
	} {
		if _, err := Parse([]byte(fmt.Sprintf(base, "matcha", raw))); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := Parse([]byte(`{"id":"eu","type":"addon","for":"matcha","name":"欧洲","settings":[{"id":"a","type":"toggle","label":"x"}],"decorations":[{"id":"de","name":"德国"}]}`)); err == nil {
		t.Error("an add-on must not declare settings")
	}
}

func TestAddonsAndDecorations(t *testing.T) {
	skin := `{"id":"matcha","name":"x","chrome":{"light":"#eef0e2","dark":"#141811"},"swatch":["#eef0e2","#fbfaf3","#5f8f3a"],"styles":["theme.css"],"decorations":%s}`
	m, err := Parse([]byte(fmt.Sprintf(skin, `[{"id":"jp","name":"日本","regions":[" 日本 "],"images":{"art":"img/jp.webp"},"vars":{"plate-top":"0.3","board":"#ffffff","blink":"5.4s","rgb":"255, 79, 154","cell":"rgb(255 255 255 / 0.62)"}},{"id":"fallback","name":"兜底","default":true}]`)))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Decorations) != 2 || m.Decorations[0].Regions[0] != "日本" || m.Decorations[0].Vars["board"] != "#ffffff" {
		t.Fatalf("decorations: %+v", m.Decorations)
	}
	addon := `{"id":"europe","type":"addon","for":"matcha","name":"欧洲","decorations":[{"id":"de","name":"德国","regions":["德国"],"images":{"art":"de.webp"}}]}`
	if a, err := Parse([]byte(addon)); err != nil || a.Type != TypeAddon || a.For != "matcha" {
		t.Fatalf("addon: %+v %v", a, err)
	}
	for name, raw := range map[string]string{
		"addon without for":   strings.Replace(addon, `"for":"matcha",`, "", 1),
		"addon for itself":    strings.Replace(addon, `"for":"matcha"`, `"for":"europe"`, 1),
		"addon with styles":   strings.Replace(addon, `"name":"欧洲"`, `"name":"欧洲","styles":["a.css"]`, 1),
		"empty addon":         `{"id":"europe","type":"addon","for":"matcha","name":"欧洲"}`,
		"for on a skin":       fmt.Sprintf(strings.Replace(skin, `"styles"`, `"for":"glass","styles"`, 1), `[]`),
		"bad deco id":         fmt.Sprintf(skin, `[{"id":"Bad!","name":"x"}]`),
		"dup deco id":         fmt.Sprintf(skin, `[{"id":"a","name":"x"},{"id":"a","name":"y"}]`),
		"no deco name":        fmt.Sprintf(skin, `[{"id":"a"}]`),
		"svg image":           fmt.Sprintf(skin, `[{"id":"a","name":"x","images":{"art":"a.svg"}}]`),
		"image outside":       fmt.Sprintf(skin, `[{"id":"a","name":"x","images":{"art":"../a.webp"}}]`),
		"bad image name":      fmt.Sprintf(skin, `[{"id":"a","name":"x","images":{"Art":"a.webp"}}]`),
		"css injection":       fmt.Sprintf(skin, `[{"id":"a","name":"x","vars":{"board":"red;}body{display:none"}}]`),
		"quoted value":        fmt.Sprintf(skin, `[{"id":"a","name":"x","vars":{"board":"url(\"x\")"}}]`),
		"image and var clash": fmt.Sprintf(skin, `[{"id":"a","name":"x","images":{"art":"a.webp"},"vars":{"art":"1"}}]`),
		"unknown type":        `{"id":"europe","type":"figures","name":"欧洲"}`,
	} {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestFrontendArchiveContract(t *testing.T) {
	manifest := `{"id":"viewer","name":"Viewer","chrome":{"light":"#ffffff","dark":"#111111"},"swatch":["#ffffff","#eeeeee","#123456"],"frontend":{"entry":"index.html","api_version":1}}`
	files := map[string]string{"theme.json": manifest, "index.html": `<script type="module" src="app.js"></script>`, "app.js": `import './chunk.mjs'`, "chunk.mjs": "export const x=1", "data.json": "{}", "logo.svg": "<svg/>", "app.wasm": "wasm"}
	if info, err := Inspect(frontendZip(t, files)); err != nil || len(info.Permissions) != 2 {
		t.Fatalf("frontend archive/review: %+v %v", info, err)
	}
	for _, bad := range []string{
		strings.Replace(manifest, `"api_version":1`, `"api_version":2`, 1),
		strings.Replace(manifest, `index.html`, `../index.html`, 1),
		strings.Replace(manifest, `index.html`, `app.js`, 1),
		strings.Replace(manifest, `index.html`, `missing.html`, 1),
		strings.Replace(manifest, `"frontend":`, `"base":"plain","frontend":`, 1),
	} {
		files["theme.json"] = bad
		if _, _, err := Open(frontendZip(t, files)); err == nil {
			t.Fatalf("accepted invalid frontend: %s", bad)
		}
	}
	files["theme.json"] = strings.Replace(manifest, `"frontend":{"entry":"index.html","api_version":1}`, `"styles":["theme.css"]`, 1)
	files["theme.css"] = "body{}"
	if _, _, err := Open(frontendZip(t, files)); err == nil {
		t.Fatal("CSS skin must not acquire executable files")
	}
	files["theme.json"] = `{"id":"addon","type":"addon","for":"viewer","name":"Add-on","decorations":[{"id":"a","name":"A"}],"frontend":{"entry":"index.html","api_version":1}}`
	if _, _, err := Open(frontendZip(t, files)); err == nil {
		t.Fatal("add-on must not acquire a frontend entry")
	}
}

func frontendZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for name, data := range files {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestSettingsUIArchiveContract(t *testing.T) {
	manifest := `{"id":"dialog","name":"Dialog","chrome":{"light":"#ffffff","dark":"#111111"},"swatch":["#ffffff","#eeeeee","#123456"],"styles":["theme.css"],"settings_ui":{"entry":"settings/index.html","api_version":1}}`
	files := map[string]string{"theme.json": manifest, "theme.css": "body{}", "settings/index.html": `<script src="/theme-settings-sdk.js"></script><script src="main.js"></script>`, "settings/main.js": "TZASettings.ready.then(console.log)"}
	if info, err := Inspect(frontendZip(t, files)); err != nil || len(info.Permissions) != 1 {
		t.Fatalf("settings archive/review: %+v %v", info, err)
	}
	for _, bad := range []string{
		strings.Replace(manifest, `"api_version":1`, `"api_version":2`, 1),
		strings.Replace(manifest, `settings/index.html`, `../index.html`, 1),
		strings.Replace(manifest, `settings/index.html`, `settings/main.js`, 1),
		strings.Replace(manifest, `settings/index.html`, `missing.html`, 1),
		`{"id":"addon","type":"addon","for":"dialog","name":"Addon","decorations":[{"id":"a","name":"A"}],"settings_ui":{"entry":"settings/index.html","api_version":1}}`,
	} {
		files["theme.json"] = bad
		if _, _, err := Open(frontendZip(t, files)); err == nil {
			t.Fatalf("accepted invalid settings UI: %s", bad)
		}
	}
}
