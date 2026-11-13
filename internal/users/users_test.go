package users_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/httpapi"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/module"
	"github.com/leeska/TZA-Probe/internal/storage"
	"github.com/leeska/TZA-Probe/internal/users"
)

func TestStatusAndPeriods(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	u := users.User{Enabled: true, TrafficLimit: 100, TrafficUsed: 99, ExpiresAt: "2026-12-01T00:00:00Z"}
	if u.Status(now) != users.StatusActive {
		t.Fatal(u.Status(now))
	}
	u.TrafficUsed = 100
	if u.Status(now) != users.StatusExceeded {
		t.Fatal("用完额度")
	}
	if u.Status(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)) != users.StatusExpired {
		t.Fatal("到期时刻起就算过期")
	}
	u.Enabled = false
	if u.Status(now) != users.StatusDisabled {
		t.Fatal("停用优先")
	}
	p := users.User{ResetDay: 5, PeriodStart: time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC).Unix()}
	if got := time.Unix(p.NextReset(), 0).UTC(); !got.Equal(time.Date(2026, 12, 5, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("下一个重置日：%v", got)
	}
	p.PeriodStart = time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC).Unix()
	if got := time.Unix(p.NextReset(), 0).UTC(); !got.Equal(time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("当月还没到：%v", got)
	}
	if (users.User{}).NextReset() != 0 {
		t.Fatal("不重置")
	}
	for _, bad := range []users.User{{Name: ""}, {Name: "a", ResetDay: 29}, {Name: "a", TrafficLimit: -1}, {Name: "a", ExpiresAt: "明天"}} {
		if bad.Validate() == nil {
			t.Fatalf("应拒绝 %+v", bad)
		}
	}
}

func openStore(t *testing.T) (*storage.Store, *users.Module) {
	t.Helper()
	mod := &users.Module{}
	st, err := storage.OpenWith(filepath.Join(t.TempDir(), "users.db"), module.Migrations([]module.Module{mod}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	mod.Store = &users.Store{DB: st.DB()}
	return st, mod
}

func TestRollPeriods(t *testing.T) {
	_, mod := openStore(t)
	ctx := context.Background()
	start := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	u, err := mod.Store.Create(ctx, users.User{Name: "朋友", Enabled: true, ResetDay: 1, TrafficLimit: 10}, start)
	if err != nil {
		t.Fatal(err)
	}
	never, _ := mod.Store.Create(ctx, users.User{Name: "不重置", Enabled: true}, start)
	// 用量由流量统计写入；这里直接写库模拟。
	set := func(id string, used int64) {
		x, _ := mod.Store.Get(ctx, id)
		x.TrafficUsed = used
		raw, _ := json.Marshal(x)
		if _, err := mod.Store.DB.Exec("UPDATE users SET value=? WHERE id=?", string(raw), id); err != nil {
			t.Fatal(err)
		}
	}
	set(u.ID, 10)
	set(never.ID, 10)
	if n, _ := mod.Store.RollPeriods(ctx, start.AddDate(0, 0, 5)); n != 0 {
		t.Fatal("还没到重置日")
	}
	// 服务停了几个月：一次补到当前周期。
	n, err := mod.Store.RollPeriods(ctx, time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC))
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	got, _ := mod.Store.Get(ctx, u.ID)
	if got.TrafficUsed != 0 || !time.Unix(got.PeriodStart, 0).UTC().Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("新周期：%+v", got)
	}
	if x, _ := mod.Store.Get(ctx, never.ID); x.TrafficUsed != 10 {
		t.Fatal("不重置的不动")
	}
}

// 后台增删改、换 token、清零用量；使用者凭 token 看自己的用量，不需要登录、看不到 id 和备注，
// 换 token 后旧链接立即失效。
func TestRoutes(t *testing.T) {
	st, mod := openStore(t)
	admin := model.Secret()
	handler := (&httpapi.API{Store: st, AdminToken: admin, Modules: []module.Module{mod}, Assets: http.NotFoundHandler()}).Handler()
	call := func(method, path, key string, body any) (int, map[string]any) {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "http://localhost/api/v1"+path, bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		out := map[string]any{}
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	if code, _ := call("GET", "/admin/users", "", nil); code != 401 {
		t.Fatal("后台接口要登录", code)
	}
	code, u := call("POST", "/admin/users", admin, map[string]any{"name": " 小明 ", "note": "同事", "enabled": true, "traffic_limit": 100 << 30, "reset_day": 1, "expires_at": "2099-01-01T00:00:00Z"})
	if code != 201 || u["name"] != "小明" || u["status"] != "active" || len(u["token"].(string)) != 32 {
		t.Fatal(code, u)
	}
	if code, _ := call("POST", "/admin/users", admin, map[string]any{"name": "x", "reset_day": 31}); code != 400 {
		t.Fatal("坏值", code)
	}
	id, token := u["id"].(string), u["token"].(string)
	code, page := call("GET", "/u/"+token, "", nil)
	if code != 200 || page["name"] != "小明" || page["status"] != "active" || page["id"] != nil || page["note"] != nil {
		t.Fatal(code, page)
	}
	if code, _ := call("PUT", "/admin/users/"+id, admin, map[string]any{"name": "小明", "enabled": false}); code != 200 {
		t.Fatal(code)
	}
	if _, page := call("GET", "/u/"+token, "", nil); page["status"] != "disabled" {
		t.Fatal(page)
	}
	_, rotated := call("POST", "/admin/users/"+id+"/token", admin, nil)
	if rotated["token"] == token {
		t.Fatal("token 没换")
	}
	if code, _ := call("GET", "/u/"+token, "", nil); code != 404 {
		t.Fatal("旧 token 要失效", code)
	}
	if code, _ := call("GET", "/u/"+rotated["token"].(string), "", nil); code != 200 {
		t.Fatal("新 token", code)
	}
	if code, _ := call("POST", "/admin/users/"+id+"/reset", admin, nil); code != 200 {
		t.Fatal(code)
	}
	if code, _ := call("DELETE", "/admin/users/"+id, admin, nil); code != 204 {
		t.Fatal(code)
	}
	if code, _ := call("DELETE", "/admin/users/"+id, admin, nil); code != 404 {
		t.Fatal(code)
	}
	cards, _ := mod.Overview(context.Background())
	if len(cards) != 1 || cards[0].Page != "users" {
		t.Fatal(cards)
	}
}

func TestAddUsage(t *testing.T) {
	_, mod := openStore(t)
	s := mod.Store
	ctx := context.Background()
	now := time.Now()
	a, _ := s.Create(ctx, users.User{Name: "a", Enabled: true, TrafficLimit: 100}, now)
	b, _ := s.Create(ctx, users.User{Name: "b", Enabled: true}, now)
	gone, err := s.AddUsage(ctx, map[string]users.Usage{a.ID: {Up: 40, Down: 30}, b.ID: {Down: 500}, "missing": {Up: 1}}, now)
	if err != nil || len(gone) != 0 {
		t.Fatal(gone, err)
	}
	gone, _ = s.AddUsage(ctx, map[string]users.Usage{a.ID: {Down: 30}}, now)
	if len(gone) != 1 || gone[0] != a.ID {
		t.Fatal("用完额度的要报出来", gone)
	}
	got, _ := s.Get(ctx, a.ID)
	if got.TrafficUsed != 100 || got.TrafficUp != 40 || got.TrafficDown != 60 {
		t.Fatalf("%+v", got)
	}
	if gone, _ = s.AddUsage(ctx, map[string]users.Usage{a.ID: {Down: 1}}, now); len(gone) != 0 {
		t.Fatal("已经超额的不重复报", gone)
	}
	got, _ = s.ResetUsage(ctx, a.ID, now)
	if got.TrafficUsed != 0 || got.TrafficUp != 0 || got.TrafficDown != 0 {
		t.Fatalf("%+v", got)
	}
}
