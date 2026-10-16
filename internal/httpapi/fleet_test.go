package httpapi

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/agentdist"
	"github.com/leeska/TZA-Probe/internal/agentsig"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/storage"
	"github.com/leeska/TZA-Probe/pkg/tzapkg"
	"github.com/leeska/TZA-Probe/web"
)

type fleetEnv struct {
	t      *testing.T
	store  *storage.Store
	server *httptest.Server
	admin  string
}

// steppingRepo moves the report clock ten seconds per report, so a test can
// report back to back without tripping the 3-second report throttle.
type steppingRepo struct {
	*storage.Store
	mu    sync.Mutex
	clock time.Time
}

func (r *steppingRepo) Report(ctx context.Context, id string, m model.Metrics, _ time.Time) error {
	r.mu.Lock()
	r.clock = r.clock.Add(10 * time.Second)
	now := r.clock
	r.mu.Unlock()
	return r.Store.Report(ctx, id, m, now)
}

func newFleetEnv(t *testing.T, agents *agentdist.Dist) *fleetEnv {
	t.Helper()
	s, err := storage.Open(filepath.Join(t.TempDir(), "fleet.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	admin := model.Secret()
	repo := &steppingRepo{Store: s, clock: time.Now().Add(-time.Hour)}
	server := httptest.NewServer((&API{Store: repo, AdminToken: admin, Assets: web.Handler(), Agents: agents}).Handler())
	t.Cleanup(server.Close)
	return &fleetEnv{t: t, store: s, server: server, admin: admin}
}

func (e *fleetEnv) request(method, path, token string, body any) (int, []byte) {
	e.t.Helper()
	var data []byte
	if body != nil {
		data, _ = json.Marshal(body)
	}
	req, _ := http.NewRequest(method, e.server.URL+path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := e.server.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// node creates a node through the admin API and returns its id and Agent key.
func (e *fleetEnv) node(name, group string) (string, string) {
	e.t.Helper()
	code, data := e.request("POST", "/api/v1/admin/nodes", e.admin, model.Node{Name: name, Group: group, Billing: model.Billing{ResetAt: "2026-09-01T00:00:00Z", TrafficMode: "sum"}})
	if code != 200 {
		e.t.Fatalf("create %s: %d %s", name, code, data)
	}
	var v struct{ ID, Token string }
	json.Unmarshal(data, &v)
	return v.ID, v.Token
}

func (e *fleetEnv) nodes() map[string]model.NodeView {
	e.t.Helper()
	code, data := e.request("GET", "/api/v1/admin/nodes", e.admin, nil)
	if code != 200 {
		e.t.Fatalf("list: %d %s", code, data)
	}
	var list []model.NodeView
	json.Unmarshal(data, &list)
	out := map[string]model.NodeView{}
	for _, n := range list {
		out[n.ID] = n
	}
	return out
}

func TestBatchPatchNodesIsAllOrNothing(t *testing.T) {
	e := newFleetEnv(t, nil)
	a, _ := e.node("甲", "")
	b, _ := e.node("乙", "旧组")
	c, _ := e.node("丙", "")
	code, data := e.request("POST", "/api/v1/admin/nodes/batch", e.admin, map[string]any{"ids": []string{a, b}, "patch": map[string]any{"group": "日本", "region": "东京", "public": false}})
	if code != 200 || string(bytes.TrimSpace(data)) != `{"updated":2}` {
		t.Fatalf("batch: %d %s", code, data)
	}
	nodes := e.nodes()
	for _, id := range []string{a, b} {
		if n := nodes[id]; n.Group != "日本" || n.Region != "东京" || n.Public {
			t.Fatalf("batch did not land on %s: %+v", n.Name, n.Node)
		}
	}
	if nodes[a].Name != "甲" || nodes[b].Name != "乙" || nodes[c].Group != "" {
		t.Fatal("batch touched more than the submitted fields or nodes")
	}
	// 一台不合法（卡片地区不在任何目录里）就一台都不改。
	if code, data = e.request("POST", "/api/v1/admin/nodes/batch", e.admin, map[string]any{"ids": []string{a, c}, "patch": map[string]any{"group": "美国", "display": map[string]any{"route_region": "火星"}}}); code != 400 {
		t.Fatalf("invalid batch: %d %s", code, data)
	}
	if e.nodes()[c].Group != "" {
		t.Fatal("a refused batch must change nothing")
	}
	for _, patch := range []map[string]any{{"name": "x"}, {"order": 3}, {}} {
		if code, _ = e.request("POST", "/api/v1/admin/nodes/batch", e.admin, map[string]any{"ids": []string{a}, "patch": patch}); code != 400 {
			t.Fatalf("patch %v must be refused: %d", patch, code)
		}
	}
	if code, _ = e.request("POST", "/api/v1/admin/nodes/batch", e.admin, map[string]any{"ids": []string{a, "missing"}, "patch": map[string]any{"group": "x"}}); code != 404 {
		t.Fatalf("unknown node: %d", code)
	}
	if code, _ = e.request("POST", "/api/v1/admin/nodes/batch", "", map[string]any{"ids": []string{a}, "patch": map[string]any{"group": "x"}}); code != 401 {
		t.Fatalf("anonymous batch: %d", code)
	}
}

func TestRenameGroupCarriesAlertRules(t *testing.T) {
	e := newFleetEnv(t, nil)
	ctx := context.Background()
	hk1, _ := e.node("hk1", "HK")
	hk2, _ := e.node("hk2", "HK")
	jp, _ := e.node("jp", "JP")
	rule, err := e.store.SaveAlertRule(ctx, model.AlertRule{Kind: model.AlertCPU, Threshold: 80, HoldSeconds: 300, Scope: model.ScopeGroup, Groups: []string{"HK"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	code, data := e.request("POST", "/api/v1/admin/groups/rename", e.admin, map[string]string{"from": "HK", "to": "香港"})
	if code != 200 || string(bytes.TrimSpace(data)) != `{"moved":2}` {
		t.Fatalf("rename: %d %s", code, data)
	}
	nodes := e.nodes()
	if nodes[hk1].Group != "香港" || nodes[hk2].Group != "香港" || nodes[jp].Group != "JP" {
		t.Fatal("rename moved the wrong nodes")
	}
	rules, _ := e.store.AlertRules(ctx)
	if len(rules) != 1 || rules[0].ID != rule || len(rules[0].Groups) != 1 || rules[0].Groups[0] != "香港" {
		t.Fatalf("rule must follow the group: %+v", rules)
	}
	if _, ok := nodes[hk1].Alerts.Rule(model.AlertCPU); !ok {
		t.Fatal("renamed group lost its rule")
	}
	// 合并进一个已有同类规则的分组会让两条规则争同一批节点：拒绝，什么都不改。
	if _, err = e.store.SaveAlertRule(ctx, model.AlertRule{Kind: model.AlertCPU, Threshold: 90, HoldSeconds: 300, Scope: model.ScopeGroup, Groups: []string{"JP"}}, true); err != nil {
		t.Fatal(err)
	}
	if code, data = e.request("POST", "/api/v1/admin/groups/rename", e.admin, map[string]string{"from": "香港", "to": "JP"}); code != 400 {
		t.Fatalf("conflicting merge: %d %s", code, data)
	}
	if e.nodes()[hk1].Group != "香港" {
		t.Fatal("a refused merge must not move nodes")
	}
	// 解散：回到未分组。
	if code, data = e.request("POST", "/api/v1/admin/groups/rename", e.admin, map[string]string{"from": "JP", "to": ""}); code != 200 {
		t.Fatalf("dissolve: %d %s", code, data)
	}
	if e.nodes()[jp].Group != "" {
		t.Fatal("dissolved group must leave its nodes ungrouped")
	}
}

func TestAgentUpgradeQueue(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, agentdist.Name("amd64")), []byte("new-agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	e := newFleetEnv(t, &agentdist.Dist{Dir: dir})
	modern, modernKey := e.node("modern", "")
	legacy, legacyKey := e.node("legacy", "")
	fresh, _ := e.node("fresh", "")
	metrics := func(version string, self bool) model.Report {
		m := model.Metrics{CPU: 1, MemoryTotal: 100, MemoryUsed: 10, Arch: "amd64", Version: version}
		if self {
			m.Capabilities = []string{model.CapabilitySelfUpdate}
		}
		return model.Report{Protocol: model.Protocol, Metrics: m}
	}
	report := func(key string, r model.Report) (int, model.ReportReply) {
		t.Helper()
		code, data := e.request("POST", "/api/v1/agent/report", key, r)
		var reply model.ReportReply
		if code == 200 {
			json.Unmarshal(data, &reply)
		}
		return code, reply
	}
	if code, _ := report(modernKey, metrics("0.9.0", true)); code != 204 {
		t.Fatalf("nothing queued must stay 204: %d", code)
	}
	report(legacyKey, metrics("0.9.0", false))

	code, data := e.request("POST", "/api/v1/admin/agents/upgrade", e.admin, map[string]any{"ids": []string{modern, legacy, fresh}})
	if code != 200 {
		t.Fatalf("queue: %d %s", code, data)
	}
	var queued struct{ Results map[string]string }
	json.Unmarshal(data, &queued)
	// Server 手里只有没签名的安装包：不下发，标「未签名」。
	if queued.Results[modern] != upgradeUnsigned || queued.Results[legacy] != upgradeManual || queued.Results[fresh] != upgradeNever {
		t.Fatalf("results: %v", queued.Results)
	}
	if code, _ := e.request("GET", "/dl/"+agentdist.KeyFile, "", nil); code != 404 {
		t.Fatalf("no key configured: %d", code)
	}

	// 运维者在自己电脑上签好，把 .sig 与公钥放进 agents 目录。
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	sum := sha256.Sum256([]byte("new-agent"))
	writeSig := func(version string, binarySum string) agentsig.Signature {
		sig := agentsig.Sign(key, "ops", "amd64", version, binarySum)
		raw, _ := json.Marshal(sig)
		os.WriteFile(filepath.Join(dir, agentdist.Name("amd64")+".sig"), raw, 0o644)
		return sig
	}
	os.WriteFile(filepath.Join(dir, agentdist.KeyFile), []byte(tzapkg.FormatPublicKey("ops", pub)+"\n"), 0o644)
	if code, data := e.request("GET", "/dl/"+agentdist.KeyFile, "", nil); code != 200 || !bytes.HasPrefix(data, []byte("ops:")) {
		t.Fatalf("upgrade key: %d %s", code, data)
	}
	// 签的是别的二进制（换了安装包忘了重签）：仍然不下发。
	writeSig("1.1.0", hex.EncodeToString(make([]byte, 32)))
	if _, err := (&agentdist.Dist{Dir: dir}).Signature("amd64"); !errors.Is(err, agentdist.ErrUnsigned) {
		t.Fatalf("stale signature: %v", err)
	}
	// 签名版本不比节点新：算已是最新。
	writeSig("0.8.0", hex.EncodeToString(sum[:]))
	code, data = e.request("POST", "/api/v1/admin/agents/upgrade", e.admin, map[string]any{"ids": []string{modern}})
	json.Unmarshal(data, &queued)
	if code != 200 || queued.Results[modern] != upgradeCurrent {
		t.Fatalf("older signed release: %d %v", code, queued.Results)
	}
	sig := writeSig("1.1.0", hex.EncodeToString(sum[:]))
	code, data = e.request("POST", "/api/v1/admin/agents/upgrade", e.admin, map[string]any{"ids": []string{modern}})
	json.Unmarshal(data, &queued)
	if code != 200 || queued.Results[modern] != upgradeQueued {
		t.Fatalf("signed release: %d %v", code, queued.Results)
	}
	code, reply := report(modernKey, metrics("0.9.0", true))
	if code != 200 || reply.Upgrade == nil || reply.Upgrade.Path != "/dl/tza-agent-linux-amd64" || reply.Upgrade.SHA256 != sig.SHA256 ||
		reply.Upgrade.Version != "1.1.0" || reply.Upgrade.Arch != "amd64" || reply.Upgrade.KeyID != "ops" || reply.Upgrade.Sig != sig.Sig {
		t.Fatalf("queued node must get the signed upgrade: %d %+v", code, reply.Upgrade)
	}
	// 下发一次就够：之后的上报不再重复下发，直到它以新版本回来。
	if code, _ = report(modernKey, metrics("0.9.0", true)); code != 204 {
		t.Fatalf("upgrade must be sent once: %d", code)
	}
	state := func() map[string]upgradeItem {
		_, data := e.request("GET", "/api/v1/admin/agents/upgrade", e.admin, nil)
		var v struct{ Items map[string]upgradeItem }
		json.Unmarshal(data, &v)
		return v.Items
	}
	if state()[modern].State != upgradeSent {
		t.Fatalf("state: %+v", state())
	}
	if code, _ = report(modernKey, metrics("1.1.0", true)); code != 204 {
		t.Fatalf("upgraded report: %d", code)
	}
	if _, ok := state()[modern]; ok {
		t.Fatal("a report at the offered version must clear the entry")
	}
	if code, _ = e.request("POST", "/api/v1/admin/agents/upgrade", e.admin, map[string]any{"ids": []string{modern}}); code != 200 {
		t.Fatal(code)
	}
	if code, _ = e.request("POST", "/api/v1/admin/agents/upgrade", "", map[string]any{"ids": []string{modern}}); code != 401 {
		t.Fatalf("anonymous upgrade: %d", code)
	}
}
