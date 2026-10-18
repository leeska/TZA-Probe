package market

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/market/markettest"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/plugin"
	"github.com/leeska/TZA-Probe/internal/theme"
	"github.com/leeska/TZA-Probe/pkg/tzapkg"
)

type env struct {
	cc      *markettest.CC
	client  *Client
	themes  *theme.Store
	plugins *plugin.Store
	dir     string
	clock   time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	cc := markettest.New(t)
	cc.Add(&markettest.Package{ID: "matcha", Type: "skin", Name: "抹茶", Releases: []markettest.Release{
		{Version: "1.2.0", Probe: ">=9.0.0", Archive: markettest.Skin(t, "matcha", "1.2.0")},
		{Version: "1.1.0", Probe: ">=1.0.0", Archive: markettest.Skin(t, "matcha", "1.1.0")},
		{Version: "1.0.0", Archive: markettest.Skin(t, "matcha", "1.0.0")},
	}})
	dir := t.TempDir()
	e := &env{cc: cc, dir: dir, themes: &theme.Store{Dir: filepath.Join(dir, "themes")}, plugins: &plugin.Store{Dir: filepath.Join(dir, "plugins")}, clock: time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)}
	c, err := New(Config{URL: cc.URL() + "/", Keys: cc.Keys(), Dir: dir, Probe: "v1.1.0", Themes: e.themes, Plugins: e.plugins})
	if err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return e.clock }
	e.client = c
	return e
}

func TestNilClientIsNotConfigured(t *testing.T) {
	c, err := New(Config{})
	if c != nil || err != nil {
		t.Fatalf("empty URL: %v %v", c, err)
	}
	if _, err := c.Catalog(context.Background(), Query{}); !errors.Is(err, ErrNotConfigured) {
		t.Fatal(err)
	}
	if _, err := c.Install(context.Background(), "matcha", ""); !errors.Is(err, ErrNotConfigured) {
		t.Fatal(err)
	}
	if s := c.Status(); s.Configured {
		t.Fatalf("status: %+v", s)
	}
	for _, bad := range []string{"ftp://cc", "https://", "https://u:p@cc", "https://cc/?x=1", "cc.example.com"} {
		if _, err := New(Config{URL: bad}); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestCatalogAndDetail(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cat, err := e.client.Catalog(ctx, Query{})
	if err != nil {
		t.Fatal(err)
	}
	// The plugin entry and the malformed id are dropped.
	if len(cat.Items) != 1 || cat.Items[0].ID != "matcha" || cat.Items[0].Installed != nil {
		t.Fatalf("catalog: %+v", cat)
	}
	if !strings.HasPrefix(cat.Items[0].Cover, e.cc.URL()+"/media/") {
		t.Fatalf("cover: %q", cat.Items[0].Cover)
	}
	seen := e.cc.Requests()
	if q := seen[len(seen)-1].Query; !strings.Contains(q, "probe=1.1.0") || !strings.Contains(q, "page=1") {
		t.Fatalf("catalog query: %q", q)
	}
	if _, err := e.client.Catalog(ctx, Query{Type: "script"}); !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("bad type: %v", err)
	}

	p, err := e.client.Package(ctx, "matcha")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Versions) != 3 || p.Versions[0].Compatible || !p.Versions[1].Compatible || !p.Versions[2].Compatible {
		t.Fatalf("versions: %+v", p.Versions)
	}
	// Screenshots off the CC origin are dropped: the proxy would refuse them.
	if len(p.Screenshots) != 1 || !strings.HasPrefix(p.Screenshots[0], e.cc.URL()) {
		t.Fatalf("screenshots: %v", p.Screenshots)
	}
	if _, err := e.client.Package(ctx, "nope"); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("missing package: %v", err)
	}
	if _, err := e.client.Package(ctx, "../etc"); !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("bad id: %v", err)
	}
}

func TestInstallHappyPathAndUpdates(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	// "latest" is the newest release this Probe can run: 1.2.0 needs Probe >= 9.
	m, err := e.client.Install(ctx, "matcha", "")
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "matcha" || m.Version != "1.1.0" || m.Source != tzapkg.SourceMarket {
		t.Fatalf("installed: %+v", m)
	}
	o, ok := e.themes.Origin("matcha")
	if !ok || o.Version != "1.1.0" || len(o.SHA256) != 64 || o.KeyID != e.cc.KeyID {
		t.Fatalf("origin: %+v %v", o, ok)
	}
	if _, err := e.client.Install(ctx, "matcha", "1.2.0"); !errors.Is(err, model.ErrInvalid) || !strings.Contains(err.Error(), ">=9.0.0") {
		t.Fatalf("incompatible version: %v", err)
	}
	if _, err := e.client.Install(ctx, "matcha", "3.0.0"); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("unknown version: %v", err)
	}

	cat, _ := e.client.Catalog(ctx, Query{Type: "skin"})
	if it := cat.Items[0]; it.Installed == nil || it.Installed.Source != "market" || it.Installed.Version != "1.1.0" {
		t.Fatalf("catalog marks: %+v", it)
	}

	// Updates sends only market installs, and only what it sent comes back.
	e.cc.Lock()
	e.cc.Updates = []map[string]string{{"id": "matcha", "version": "1.1.0", "latest": "1.1.5"}, {"id": "other", "latest": "2.0.0"}}
	e.cc.Unlock()
	up, err := e.client.Updates(ctx)
	if err != nil || len(up) != 1 || up[0].Latest != "1.1.5" || up[0].Version != "1.1.0" {
		t.Fatalf("updates: %+v %v", up, err)
	}
	seen := e.cc.Requests()
	if body := seen[len(seen)-1].Body; !strings.Contains(body, `"id":"matcha"`) || !strings.Contains(body, `"probe":"1.1.0"`) {
		t.Fatalf("updates body: %s", body)
	}

	// A local upload over the market package forgets the market origin.
	if _, err := e.themes.Install(markettest.Skin(t, "matcha", "9.9.9")); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.themes.Origin("matcha"); ok {
		t.Fatal("local upload kept the market origin")
	}
	before := len(e.cc.Requests())
	if up, err := e.client.Updates(ctx); err != nil || len(up) != 0 || len(e.cc.Requests()) != before {
		t.Fatalf("no market installs must not contact CC: %v %v", up, err)
	}
}

func TestInstallRejects(t *testing.T) {
	ctx := context.Background()
	_, otherKey, _ := ed25519.GenerateKey(rand.Reader)
	cases := []struct {
		name  string
		setup func(e *env)
		want  string
	}{
		{"tampered archive", func(e *env) { e.cc.Tamper = true }, "SHA-256"},
		{"untrusted key id", func(e *env) { e.cc.SigKeyID = "rogue" }, "不在信任列表"},
		{"wrong key", func(e *env) { e.cc.SignKey = otherKey }, "签名无效"},
		{"oversized body", func(e *env) { e.cc.ExtraBytes = 10 }, "不符"},
		{"reported size over the cap", func(e *env) { e.cc.Size = tzapkg.MaxUpload + 1 }, "超出范围"},
		{"id mismatch", func(e *env) {
			e.cc.Packages["matcha"].Releases[1].Archive = markettest.Skin(t, "hojicha", "1.1.0")
		}, "id"},
		{"version mismatch", func(e *env) {
			e.cc.Packages["matcha"].Releases[1].Archive = markettest.Skin(t, "matcha", "1.0.9")
		}, "version"},
		{"signed for another package", func(e *env) { e.cc.Packages["matcha"].Releases[1].SignAs = "hojicha" }, "签名里的包"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.cc.Lock()
			tc.setup(e)
			e.cc.Unlock()
			_, err := e.client.Install(ctx, "matcha", "1.1.0")
			if !errors.Is(err, ErrRejected) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want rejection mentioning %q, got %v", tc.want, err)
			}
			if entries, _ := os.ReadDir(e.themes.Dir); len(entries) != 0 {
				t.Fatalf("a rejected package left files: %v", entries)
			}
		})
	}
	t.Run("no trusted keys", func(t *testing.T) {
		e := newEnv(t)
		e.client.keys = nil
		if _, err := e.client.Install(ctx, "matcha", ""); !errors.Is(err, ErrNoTrustedKeys) {
			t.Fatal(err)
		}
	})
}

func TestInstallReceiptsOnlyAfterSuccess(t *testing.T) {
	for _, scenario := range []string{"success", "tampered", "disk error", "confirmation unavailable", "older CC"} {
		t.Run(scenario, func(t *testing.T) {
			e := newEnv(t)
			e.cc.Receipt = "test-install-receipt"
			switch scenario {
			case "tampered":
				e.cc.Tamper = true
			case "disk error":
				file := filepath.Join(e.dir, "not-a-directory")
				if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
					t.Fatal(err)
				}
				e.themes.Dir = file
			case "confirmation unavailable":
				e.cc.ConfirmationStatus = 503
			case "older CC":
				e.cc.Receipt = ""
			}
			_, err := e.client.Install(context.Background(), "matcha", "1.0.0")
			fails := scenario == "tampered" || scenario == "disk error"
			if (err != nil) != fails {
				t.Fatalf("install: %v", err)
			}
			confirmations := 0
			for _, request := range e.cc.Requests() {
				if request.Path == "/api/v1/installations" {
					confirmations++
					if !strings.Contains(request.Body, "test-install-receipt") {
						t.Fatal(request.Body)
					}
				}
			}
			want := 1
			if fails || scenario == "older CC" {
				want = 0
			}
			if confirmations != want {
				t.Fatalf("confirmations: %d, want %d", confirmations, want)
			}
		})
	}
}

func TestBoundEmptyInventoryIsReported(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.client.Connect(ctx, "empty"); err != nil {
		t.Fatal(err)
	}
	e.cc.Lock()
	e.cc.Device = "approved"
	e.cc.Unlock()
	e.clock = e.clock.Add(6 * time.Second)
	if result, err := e.client.Poll(ctx); err != nil || result.Status != PollBound {
		t.Fatalf("bind: %+v %v", result, err)
	}
	if _, err := e.client.Updates(ctx); err != nil {
		t.Fatal(err)
	}
	requests := e.cc.Requests()
	last := requests[len(requests)-1]
	if last.Path != "/api/v1/updates" || !strings.Contains(last.Body, `"installed":[]`) || last.Auth == "" {
		t.Fatalf("empty inventory: %+v", last)
	}
}

func TestBindingAndAuthHeader(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	authOf := func(path string) string {
		seen := e.cc.Requests()
		for i := len(seen) - 1; i >= 0; i-- {
			if seen[i].Path == path {
				return seen[i].Auth
			}
		}
		t.Fatalf("no request to %s", path)
		return ""
	}
	e.client.Catalog(ctx, Query{})
	if auth := authOf("/api/v1/catalog"); auth != "" {
		t.Fatalf("unbound request carried %q", auth)
	}

	p, err := e.client.Connect(ctx, "")
	if err != nil || p.UserCode != "ABCD-EFGH" || p.Interval != 5 || !strings.HasPrefix(p.VerifyURL, e.cc.URL()) {
		t.Fatalf("connect: %+v %v", p, err)
	}
	if body := e.cc.Requests()[len(e.cc.Requests())-1].Body; !strings.Contains(body, `"name":"TZA Probe"`) || !strings.Contains(body, `"probe":"1.1.0"`) {
		t.Fatalf("device code body: %s", body)
	}
	if s := e.client.Status(); s.Pending == nil || s.Bound {
		t.Fatalf("status while pending: %+v", s)
	}
	// Polling faster than the interval does not hit CC.
	before := len(e.cc.Requests())
	if r, _ := e.client.Poll(ctx); r.Status != PollPending || len(e.cc.Requests()) != before {
		t.Fatalf("early poll: %+v", r)
	}
	e.clock = e.clock.Add(6 * time.Second)
	if r, err := e.client.Poll(ctx); err != nil || r.Status != PollPending || len(e.cc.Requests()) != before+1 {
		t.Fatalf("pending poll: %+v %v", r, err)
	}
	if auth := authOf("/api/v1/device/token"); auth != "" {
		t.Fatalf("device flow must not send a token: %q", auth)
	}
	e.cc.Lock()
	e.cc.Device = "approved"
	e.cc.Unlock()
	e.clock = e.clock.Add(6 * time.Second)
	r, err := e.client.Poll(ctx)
	if err != nil || r.Status != PollBound || r.Account != "alice" {
		t.Fatalf("approved poll: %+v %v", r, err)
	}
	info, err := os.Stat(filepath.Join(e.dir, InstanceFile))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("credentials file: %v %v", info, err)
	}
	if s := e.client.Status(); !s.Bound || s.Account != "alice" || s.InstanceID != "ins_1" || s.Pending != nil {
		t.Fatalf("bound status: %+v", s)
	}
	e.client.Catalog(ctx, Query{})
	if auth := authOf("/api/v1/catalog"); auth != "Bearer "+markettest.Token {
		t.Fatalf("bound request auth: %q", auth)
	}
	// Media and downloads go without the token (they may live on object storage).
	e.client.Media(ctx, e.cc.URL()+"/media/x.png")
	if auth := authOf("/media/x.png"); auth != "" {
		t.Fatalf("media request carried the token")
	}

	// The same credentials are not sent to a different CC.
	other, _ := New(Config{URL: "http://127.0.0.1:1", Dir: e.dir})
	if other.token() != "" {
		t.Fatal("token offered to another CC")
	}
	if s := other.Status(); s.Bound || s.OtherURL != e.cc.URL() {
		t.Fatalf("other CC status: %+v", s)
	}

	warning, err := e.client.Disconnect(ctx)
	if err != nil || warning != "" {
		t.Fatalf("disconnect: %q %v", warning, err)
	}
	if auth := authOf("/api/v1/instance"); auth != "Bearer "+markettest.Token {
		t.Fatalf("unbind auth: %q", auth)
	}
	if _, err := os.Stat(filepath.Join(e.dir, InstanceFile)); !os.IsNotExist(err) {
		t.Fatal("credentials survived disconnect")
	}
}

func TestDeviceFlowExpires(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if r, _ := e.client.Poll(ctx); r.Status != PollNone {
		t.Fatalf("poll without a flow: %+v", r)
	}
	// CC says expired (410).
	e.client.Connect(ctx, "家里的探针")
	e.cc.Lock()
	e.cc.Device = "expired"
	e.cc.Unlock()
	e.clock = e.clock.Add(10 * time.Second)
	if r, err := e.client.Poll(ctx); err != nil || r.Status != PollExpired {
		t.Fatalf("410: %+v %v", r, err)
	}
	if s := e.client.Status(); s.Pending != nil {
		t.Fatal("expired flow still pending")
	}
	// The local deadline passes without CC being asked.
	e.client.Connect(ctx, "")
	e.clock = e.clock.Add(11 * time.Minute)
	before := len(e.cc.Requests())
	if r, _ := e.client.Poll(ctx); r.Status != PollExpired || len(e.cc.Requests()) != before {
		t.Fatalf("local expiry: %+v", r)
	}
	if _, err := e.client.Connect(ctx, strings.Repeat("长", 41)); !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("long name: %v", err)
	}
}

func TestMediaProxyRules(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m, err := e.client.Media(ctx, e.cc.URL()+"/media/cover.png")
	if err != nil || m.Type != "image/png" || string(m.Body) != string(markettest.PNG) {
		t.Fatalf("media: %+v %v", m, err)
	}
	for _, u := range []string{"https://elsewhere.example/x.png", "http://127.0.0.1:1/x.png", "javascript:alert(1)", ""} {
		if _, err := e.client.Media(ctx, u); !errors.Is(err, model.ErrInvalid) {
			t.Fatalf("%q: %v", u, err)
		}
	}
	if _, err := e.client.Media(ctx, e.cc.URL()+"/media/evil.svg"); !errors.Is(err, ErrRejected) {
		t.Fatalf("svg: %v", err)
	}
	if _, err := e.client.Media(ctx, e.cc.URL()+"/media/big.png"); !errors.Is(err, ErrRejected) {
		t.Fatalf("big: %v", err)
	}
	if _, err := e.client.Media(ctx, e.cc.URL()+"/media/away.png"); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("redirect off http(s): %v", err)
	}
}

func TestCompatibleAndVersions(t *testing.T) {
	for _, tc := range []struct {
		expr, probe string
		want        bool
	}{
		{"", "1.0.0", true},
		{">=1.1.0", "1.1.0", true},
		{">=1.1.0", "1.0.9", false},
		{">=1.1.0 <2.0.0", "1.5.0", true},
		{">=1.1.0, <2.0.0", "2.0.0", false},
		{">= 1.1.0", "1.2.0", true},
		{"1.2.0", "1.2.0", true},
		{"~1.2", "1.2.0", false},
		{">=1.1.0-beta", "1.2.0", false},
		{">=1.1.0", "dev", true},
	} {
		if got := Compatible(tc.expr, tc.probe); got != tc.want {
			t.Errorf("Compatible(%q, %q) = %v", tc.expr, tc.probe, got)
		}
	}
	if ProbeVersion("v1.2.3") != "1.2.3" || ProbeVersion("1.0.0-dev+abc") != "1.0.0" || ProbeVersion("dev") != "dev" {
		t.Fatal("ProbeVersion")
	}
	if !Newer("1.10.0", "1.9.0") || Newer("1.0.0", "1.0.0") {
		t.Fatal("Newer")
	}
}

// CC 回 401 说明绑定已失效（被撤销或在用户中心解绑）：本机凭据删掉，请求不带 token 再发一次。
func TestRevokedTokenDropsBinding(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.cc.Lock()
	e.cc.Device = "approved"
	e.cc.Unlock()
	e.client.Connect(ctx, "")
	e.clock = e.clock.Add(time.Minute)
	if r, err := e.client.Poll(ctx); err != nil || r.Status != PollBound {
		t.Fatalf("bind: %+v %v", r, err)
	}
	e.cc.Lock()
	e.cc.Revoked = true
	e.cc.Unlock()
	if _, err := e.client.Catalog(ctx, Query{}); err != nil {
		t.Fatalf("catalog after revocation must retry anonymously: %v", err)
	}
	seen := e.cc.Requests()
	if last := seen[len(seen)-1]; last.Auth != "" || seen[len(seen)-2].Auth == "" {
		t.Fatalf("retry: %+v", seen[len(seen)-2:])
	}
	if s := e.client.Status(); s.Bound {
		t.Fatalf("still bound: %+v", s)
	}
	if _, err := os.Stat(filepath.Join(e.dir, InstanceFile)); !os.IsNotExist(err) {
		t.Fatal("credentials kept after 401")
	}
}

func TestCCVerdictAndDownloadBody(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.cc.Lock()
	e.cc.Packages["matcha"].Releases[1].CCIncompatible = true
	e.cc.Unlock()
	p, err := e.client.Package(ctx, "matcha")
	if err != nil || p.Versions[1].Compatible || !p.Versions[2].Compatible {
		t.Fatalf("CC verdict ignored: %+v %v", p.Versions, err)
	}
	seen := e.cc.Requests()
	if q := seen[len(seen)-1].Query; q != "probe=1.1.0" {
		t.Fatalf("detail query: %q", q)
	}
	m, err := e.client.Install(ctx, "matcha", "")
	if err != nil || m.Version != "1.0.0" {
		t.Fatalf("latest compatible: %+v %v", m, err)
	}
	for _, s := range e.cc.Requests() {
		if strings.HasSuffix(s.Path, "/download") && s.Body != `{"probe":"1.1.0","version":"1.0.0"}` {
			t.Fatalf("download body: %s", s.Body)
		}
	}
}

func TestDeviceFlowSlowDownAndLimit(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.client.Connect(ctx, "")
	e.cc.Lock()
	e.cc.Device = "slow"
	e.cc.Unlock()
	e.clock = e.clock.Add(6 * time.Second)
	if r, err := e.client.Poll(ctx); err != nil || r.Status != PollPending || r.Interval != 10 {
		t.Fatalf("slow_down: %+v %v", r, err)
	}
	e.cc.Lock()
	e.cc.Device = "limit"
	e.cc.Unlock()
	e.clock = e.clock.Add(11 * time.Second)
	var remote *RemoteError
	if _, err := e.client.Poll(ctx); !errors.As(err, &remote) || remote.Status != 409 || !strings.Contains(err.Error(), "5 台") {
		t.Fatalf("instance limit: %v", err)
	}
	if s := e.client.Status(); s.Pending != nil {
		t.Fatal("a refused flow stays pending")
	}
}

// 插件走同一条验签路，按包里是 plugin.json 落到插件目录；目录与详情带 kind 和 permissions。
func TestInstallPlugin(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.cc.Add(&markettest.Package{ID: "ntfy", Type: "plugin", Kind: "notify", Name: "ntfy", Releases: []markettest.Release{
		{Version: "1.1.0", Archive: markettest.PluginZip(t, "ntfy", "1.1.0")},
		{Version: "1.0.0", Archive: markettest.PluginZip(t, "ntfy", "1.0.0")},
	}})
	e.cc.Add(&markettest.Package{ID: "public-dns", Type: "plugin", Kind: "targets", Name: "公共 DNS", Releases: []markettest.Release{
		{Version: "1.0.0", Archive: markettest.PluginZip(t, "public-dns", "")},
	}})
	cat, err := e.client.Catalog(ctx, Query{Type: "plugin"})
	if err != nil || len(cat.Items) != 2 {
		t.Fatalf("plugin catalog: %+v %v", cat, err)
	}
	for _, it := range cat.Items {
		if it.Kind == "" || len(it.Permissions) == 0 || len(it.Swatch) != 0 || it.Cover != "" {
			t.Fatalf("plugin item: %+v", it)
		}
	}
	r, err := e.client.Install(ctx, "ntfy", "")
	if err != nil || r.Type != "plugin" || r.Kind != "notify" || r.Version != "1.1.0" || r.Source != "market" || len(r.Permissions) == 0 {
		t.Fatalf("install: %+v %v", r, err)
	}
	if p, err := e.plugins.Plugin("ntfy"); err != nil || p.Source != "market" {
		t.Fatalf("stored: %+v %v", p, err)
	}
	if entries, _ := os.ReadDir(e.themes.Dir); len(entries) != 0 {
		t.Fatal("a plugin landed in the theme store")
	}
	if cat, _ := e.client.Catalog(ctx, Query{Type: "plugin"}); cat.Items[0].Installed == nil && cat.Items[1].Installed == nil {
		t.Fatal("installed plugin not marked")
	}
	e.cc.Lock()
	e.cc.Updates = []map[string]string{{"id": "ntfy", "version": "1.1.0", "latest": "1.2.0"}}
	e.cc.Unlock()
	up, err := e.client.Updates(ctx)
	if err != nil || len(up) != 1 || up[0].ID != "ntfy" {
		t.Fatalf("plugin updates: %+v %v", up, err)
	}
	if body := e.cc.Requests()[len(e.cc.Requests())-1].Body; !strings.Contains(body, `"id":"ntfy"`) {
		t.Fatalf("updates body: %s", body)
	}
	// A plugin archive whose plugin.json names another id is refused like a theme.
	e.cc.Lock()
	e.cc.Packages["public-dns"].Releases[0].Archive = markettest.PluginZip(t, "gotify", "")
	e.cc.Unlock()
	if _, err := e.client.Install(ctx, "public-dns", ""); !errors.Is(err, ErrRejected) || !strings.Contains(err.Error(), "id") {
		t.Fatalf("id mismatch: %v", err)
	}
	if _, err := e.plugins.Plugin("gotify"); err == nil {
		t.Fatal("rejected plugin was installed")
	}
}

func TestPluginReleasePermissions(t *testing.T) {
	e := newEnv(t)
	old := markettest.PluginJSON(t, "feishu")
	newer := strings.Replace(string(old), `"version": "1.0.0"`, `"version": "1.1.0"`, 1)
	newer = strings.Replace(newer, `"open.feishu.cn", "open.larksuite.com"`, `"open.feishu.cn", "open.larksuite.com", "hooks.example.com"`, 1)
	e.cc.Add(&markettest.Package{ID: "feishu", Type: "plugin", Kind: "notify", Name: "飞书", Releases: []markettest.Release{
		{Version: "1.1.0", Archive: markettest.Zip(t, map[string]string{"plugin.json": newer})},
		{Version: "1.0.0", Archive: markettest.Zip(t, map[string]string{"plugin.json": string(old)})},
	}})
	detail, err := e.client.Package(context.Background(), "feishu")
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Versions) != 2 || !strings.Contains(strings.Join(detail.Versions[0].Permissions, " "), "hooks.example.com") || strings.Contains(strings.Join(detail.Versions[1].Permissions, " "), "hooks.example.com") {
		t.Fatalf("per-version permissions: %+v", detail.Versions)
	}
}
