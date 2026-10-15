package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/leeska/TZA-Probe/internal/market/markettest"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/pkg/tzapkg"
)

type memPlugins map[string]tzapkg.Plugin

func (m memPlugins) Plugin(id string) (tzapkg.Plugin, error) {
	p, ok := m[id]
	if !ok {
		return tzapkg.Plugin{}, errors.New("没有安装这个插件")
	}
	return p, nil
}

func parse(t *testing.T, id string) tzapkg.Plugin {
	t.Helper()
	p, err := tzapkg.ParsePlugin(markettest.PluginJSON(t, id))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// toServer sends every request to the test server whatever host it names:
// the plugin's host rules apply to the URL it renders, before transport.
type toServer struct{ target *url.URL }

func (r toServer) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme, req.URL.Host = r.target.Scheme, r.target.Host
	return http.DefaultTransport.RoundTrip(req)
}

func TestEventVars(t *testing.T) {
	v := EventVars(model.Event{ID: 42, Kind: model.AlertCPU, NodeName: "东京\n1", NodeID: "n1", Active: true, At: 1760533200, Message: "当前 CPU：97.0%"})
	if v["title"] != "TZA Probe · 告警触发 · CPU 使用率 · 东京 1" || v["urgent"] != "1" || v["priority"] != "4" || v["active"] != "true" || v["id"] != "42" || v["timestamp"] != "1760533200" {
		t.Fatalf("firing vars: %v", v)
	}
	if !strings.Contains(v["text"], "类型：CPU 使用率") || strings.Contains(v["text"], "通道") || v["kind_name"] != "CPU 使用率" || v["time"] != "2025-10-15 13:00:00 UTC" {
		t.Fatalf("text: %q", v["text"])
	}
	test := EventVars(model.Event{Kind: "test", Message: "测试", At: 1})
	if test["status"] != "连接测试" || test["urgent"] != "" || test["priority"] != "2" || test["title"] != "TZA Probe · 连接测试" || test["id"] != "" || strings.Contains(test["text"], "通道") {
		t.Fatalf("test vars: %v", test)
	}
	if rec := EventVars(model.Event{Kind: model.AlertOffline}); rec["status"] != "告警解除" || rec["priority"] != "3" || rec["active"] != "false" || rec["urgent"] != "" {
		t.Fatalf("recovery vars: %v", rec)
	}
	for _, name := range tzapkg.EventVars {
		if _, ok := v[name]; !ok {
			t.Fatalf("event.%s missing", name)
		}
	}
}

func TestPluginChannelDelivers(t *testing.T) {
	var got struct {
		path, auth string
		body       map[string]any
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path, got.auth = r.URL.Path, r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&got.body)
		w.Write([]byte(`{"id":"x"}`))
	}))
	defer server.Close()
	tr := Transport{Plugins: memPlugins{"ntfy": parse(t, "ntfy")}}
	c := Config{Channel: "plugin", Plugin: "ntfy", PluginSettings: map[string]string{"server": server.URL, "topic": "probe", "token": "tk_secret"}}
	if err := tr.Send(context.Background(), c, model.Event{ID: 7, Kind: model.AlertOffline, Active: true, NodeName: "东京", Message: "离线"}); err != nil {
		t.Fatal(err)
	}
	if got.auth != "Bearer tk_secret" || got.body["topic"] != "probe" || got.body["priority"] != float64(4) || got.body["title"] != "TZA Probe · 告警触发 · 节点离线 · 东京" {
		t.Fatalf("request: %+v", got)
	}
}

func TestPluginChannelFailures(t *testing.T) {
	answer := `{"code":19021,"msg":"sign match fail, secret=tk_should_not_leak"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Write([]byte(answer))
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	tr := Transport{Plugins: memPlugins{"feishu": parse(t, "feishu")}, RoundTripper: toServer{target}}
	hook := "https://open.feishu.cn/open-apis/bot/v2/hook/0123456789abcdef"
	c := Config{Channel: "plugin", Plugin: "feishu", PluginSettings: map[string]string{"webhook": hook}}
	event := model.Event{Kind: "test", Message: "测试"}

	// HTTP 200 with an error code in the JSON is a failure; the message names
	// the field, not the whole answer, and never the webhook.
	err := tr.Send(context.Background(), c, event)
	if err == nil || !strings.Contains(err.Error(), "code") || strings.Contains(err.Error(), "tk_should_not_leak") || strings.Contains(err.Error(), "hook/0123") {
		t.Fatalf("JSON rejection: %v", err)
	}
	answer = `{"code":0}`
	if err := tr.Send(context.Background(), c, event); err != nil {
		t.Fatalf("accepted answer: %v", err)
	}
	// A stored address outside the plugin's hosts never leaves the Server.
	c.PluginSettings = map[string]string{"webhook": "https://evil.example/hook"}
	if err := tr.Send(context.Background(), c, event); err == nil || !strings.Contains(err.Error(), "设置不再有效") {
		t.Fatalf("undeclared host: %v", err)
	}
	// The plugin is gone: a clear error, stored on the event by the Engine.
	c = Config{Channel: "plugin", Plugin: "dingtalk"}
	if err := tr.Send(context.Background(), c, event); err == nil || !strings.Contains(err.Error(), "dingtalk") || !strings.Contains(err.Error(), "重新选择") {
		t.Fatalf("missing plugin: %v", err)
	}
	if err := (Transport{}).Send(context.Background(), Config{Channel: "plugin", Plugin: "ntfy"}, event); err == nil {
		t.Fatal("no plugin source")
	}
	if err := (Config{Channel: "plugin"}).Validate(); err == nil {
		t.Fatal("plugin channel without an id")
	}
	// Redirects are not followed.
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://elsewhere.example/", http.StatusFound)
	}))
	defer redirect.Close()
	tr = Transport{Plugins: memPlugins{"ntfy": parse(t, "ntfy")}}
	err = tr.Send(context.Background(), Config{Channel: "plugin", Plugin: "ntfy", PluginSettings: map[string]string{"server": redirect.URL, "topic": "probe"}}, event)
	if err == nil || !strings.Contains(err.Error(), "302") {
		t.Fatalf("redirect: %v", err)
	}
}
