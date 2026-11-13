package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/storage"
	"github.com/leeska/TZA-Probe/web"
)

// 前台按模块开关与「整个前台需要登录」：关掉的区块访客拿不到（404 / 字段清空），需要登录时
// 访客的每个前台接口都是 401；管理员不受影响；首屏脚本把开关交给前台。
func TestPublicSections(t *testing.T) {
	s, err := storage.Open(filepath.Join(t.TempDir(), "public.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	admin := model.Secret()
	server := httptest.NewServer((&API{Store: s, AdminToken: admin, Assets: web.Handler()}).Handler())
	defer server.Close()
	request := func(method, path, token string, body any) (int, []byte) {
		t.Helper()
		var reader io.Reader
		if body != nil {
			data, _ := json.Marshal(body)
			reader = bytes.NewReader(data)
		}
		req, _ := http.NewRequest(method, server.URL+path, reader)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
	boot := func() map[string]any {
		t.Helper()
		_, body := request("GET", "/theme-default.js", "", nil)
		for _, line := range strings.Split(string(body), "\n") {
			if v, ok := strings.CutPrefix(line, "window.__tzaPublic="); ok {
				var out map[string]any
				if err := json.Unmarshal([]byte(strings.TrimSuffix(v, ";")), &out); err != nil {
					t.Fatalf("首屏开关不是 JSON: %q", line)
				}
				return out
			}
		}
		t.Fatalf("首屏脚本没带前台开关: %s", body)
		return nil
	}

	if code, _ := request("GET", "/api/v1/latency-samples", "", nil); code != 200 {
		t.Fatalf("默认全开：延迟样本 %d", code)
	}
	if code, body := request("PUT", "/api/v1/admin/panel", admin, map[string]any{"public_hidden": []string{"nope"}}); code != 400 {
		t.Fatalf("未知区块要拒绝：%d %s", code, body)
	}
	if code, body := request("PUT", "/api/v1/admin/panel", admin, map[string]any{"public_hidden": []string{"unlock", "network"}}); code != 204 {
		t.Fatalf("保存区块开关：%d %s", code, body)
	}
	if got := boot()["hidden"]; len(got.([]any)) != 2 || got.([]any)[0] != "network" {
		t.Fatalf("首屏区块开关按固定顺序：%v", got)
	}
	for _, path := range []string{"/api/v1/latency-samples", "/api/v1/unlock-catalog", "/api/v1/nodes/x/network-history"} {
		if code, _ := request("GET", path, "", nil); code != 404 {
			t.Fatalf("关掉的区块访客要 404：%s → %d", path, code)
		}
	}
	if code, _ := request("GET", "/api/v1/latency-samples", admin, nil); code != 200 {
		t.Fatalf("管理员不受区块开关影响：%d", code)
	}
	if code, _ := request("GET", "/api/v1/nodes", "", nil); code != 200 {
		t.Fatalf("节点列表照常：%d", code)
	}

	if code, body := request("PUT", "/api/v1/admin/panel", admin, map[string]any{"public_login": true}); code != 204 {
		t.Fatalf("开启需要登录：%d %s", code, body)
	}
	if boot()["login"] != true {
		t.Fatal("首屏脚本要带 login:true")
	}
	for _, path := range []string{"/api/v1/nodes", "/api/v1/nodes/live", "/api/v1/observations", "/api/v1/nodes/x/history"} {
		code, body := request("GET", path, "", nil)
		if code != 401 || !strings.Contains(string(body), `"login":true`) {
			t.Fatalf("需要登录时访客 %s → %d %s", path, code, body)
		}
	}
	if code, _ := request("GET", "/api/v1/nodes", admin, nil); code != 200 {
		t.Fatalf("管理员照常：%d", code)
	}
	var panel model.PanelSettings
	_, body := request("GET", "/api/v1/admin/panel", admin, nil)
	if err := json.Unmarshal(body, &panel); err != nil || !panel.PublicLogin || len(panel.PublicHidden) != 2 {
		t.Fatalf("面板设置读回：%s", body)
	}
}

func TestPublicPolicyStrip(t *testing.T) {
	value := 12.5
	n := model.NodeView{RemainingValue: &value, ProbeTargets: []model.ProbeTarget{{Kind: "tcp"}}, Unlock: &model.UnlockSummary{}}
	n.Billing = model.Billing{Price: 9.9, Currency: "USD", ExpiresAt: "2027-01-01T00:00:00Z", TrafficLimit: 1e12, TrafficMode: "sum", ResetAt: "2026-01-01T00:00:00Z"}
	n.Display.RouteRegion = "上海"
	got := publicPolicy{}.strip(n)
	if got.Billing.Price != 9.9 || got.Unlock == nil || len(got.ProbeTargets) != 1 {
		t.Fatal("全开时不动")
	}
	got = publicPolicy{hidden: []string{"network", "unlock", "billing"}}.strip(n)
	if got.ProbeTargets != nil || got.Display.RouteRegion != "" || got.Unlock != nil {
		t.Fatalf("网络与解锁要清掉：%+v", got)
	}
	if got.Billing.Price != 0 || got.Billing.Currency != "" || got.Billing.ExpiresAt != "" || got.RemainingValue != nil {
		t.Fatalf("价格与到期要清掉：%+v", got.Billing)
	}
	if got.Billing.TrafficLimit != 1e12 || got.Billing.TrafficMode != "sum" || got.Billing.ResetAt == "" {
		t.Fatalf("流量额度与周期属于用量，要保留：%+v", got.Billing)
	}
	if n.Billing.Price != 9.9 || n.Unlock == nil {
		t.Fatal("strip 不能改动原值")
	}
	keep := publicPolicy{hidden: []string{"network"}}
	if keep.keepObservation(model.Observation{Task: model.Probe{Kind: "tcp"}}) || !keep.keepObservation(model.Observation{Task: model.Probe{Kind: model.UnlockKind}}) {
		t.Fatal("关掉网络只滤掉网络观测")
	}
}

// 总览：空库也给出四张核心卡片，读得到、带页面跳转。
func TestOverview(t *testing.T) {
	s, err := storage.Open(filepath.Join(t.TempDir(), "overview.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	admin := model.Secret()
	server := httptest.NewServer((&API{Store: s, AdminToken: admin, Assets: web.Handler()}).Handler())
	defer server.Close()
	req, _ := http.NewRequest("GET", server.URL+"/api/v1/admin/overview", nil)
	if resp, err := server.Client().Do(req); err != nil || resp.StatusCode != 401 {
		t.Fatalf("总览需要登录：%v", resp.StatusCode)
	}
	req.Header.Set("Authorization", "Bearer "+admin)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Cards []struct{ ID, Title, Page, Value, Tone string }
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, c := range out.Cards {
		ids = append(ids, c.ID)
		if c.Value == "读取失败" || c.Page == "" {
			t.Fatalf("卡片 %+v", c)
		}
	}
	if strings.Join(ids, ",") != "nodes,billing,tasks,alerts" {
		t.Fatalf("核心卡片：%v", ids)
	}
}
