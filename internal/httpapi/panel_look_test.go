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

// 前台默认外观是后台可控的：存进设置、读回设置、并在样式表之前交给浏览器
// （/theme-default.js）。坏值 / 未登录一律拒掉，别把一张不存在的皮肤发给访客。
func TestPanelDefaultLookSettings(t *testing.T) {
	s, err := storage.Open(filepath.Join(t.TempDir(), "look.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	admin := model.Secret()
	server := httptest.NewServer((&API{Store: s, AdminToken: admin, Assets: web.Handler()}).Handler())
	defer server.Close()
	request := func(method, path, token string, body any) (int, []byte) {
		t.Helper()
		var data []byte
		if body != nil {
			data, _ = json.Marshal(body)
		}
		req, _ := http.NewRequest(method, server.URL+path, bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
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
	look := func() (string, string) {
		t.Helper()
		code, body := request("GET", "/api/v1/admin/panel", admin, nil)
		if code != 200 {
			t.Fatalf("settings: %d %s", code, body)
		}
		var view struct {
			DefaultTheme string `json:"default_theme"`
			DefaultSkin  string `json:"default_skin"`
		}
		if err = json.Unmarshal(body, &view); err != nil {
			t.Fatal(err)
		}
		return view.DefaultTheme, view.DefaultSkin
	}
	defaults := func() string {
		t.Helper()
		code, body := request("GET", "/theme-default.js", "", nil)
		if code != 200 {
			t.Fatalf("theme-default.js: %d %s", code, body)
		}
		return string(body)
	}

	// 没配过就是内置默认，而且首屏脚本里也写着它们。
	if theme, skin := look(); theme != model.DefaultPanelTheme || skin != model.DefaultPanelSkin {
		t.Fatalf("未配置时该是 %s / %s，得到 %s / %s", model.DefaultPanelTheme, model.DefaultPanelSkin, theme, skin)
	}
	if body := defaults(); !strings.Contains(body, `"auto"`) || !strings.Contains(body, `"shadcn"`) {
		t.Fatalf("首屏脚本没带内置默认: %s", body)
	}

	// 复古印刷已不内置：没装这个主题包时不能设成默认（装上之后的情形见 theme_api_test.go）。
	if code, body := request("PUT", "/api/v1/admin/panel", admin, map[string]any{"default_skin": "press"}); code != 400 {
		t.Fatalf("没装的主题包不该能设成默认: %d %s", code, body)
	}
	// 存下深色 + 现代极简。
	if code, body := request("PUT", "/api/v1/admin/panel", admin, map[string]any{"default_theme": "dark", "default_skin": "shadcn"}); code != 204 {
		t.Fatalf("PUT panel: %d %s", code, body)
	}
	if theme, skin := look(); theme != "dark" || skin != "shadcn" {
		t.Fatalf("设置没保存: %s / %s", theme, skin)
	}
	body := defaults()
	if !strings.Contains(body, `theme:"dark"`) || !strings.Contains(body, `skin:"shadcn"`) {
		t.Fatalf("首屏脚本没跟上设置: %s", body)
	}

	// 只改延迟窗口不该动外观。
	if code, _ := request("PUT", "/api/v1/admin/panel", admin, map[string]any{"latency_window_minutes": 360}); code != 204 {
		t.Fatal("只改窗口被拒")
	}
	if theme, skin := look(); theme != "dark" || skin != "shadcn" {
		t.Fatalf("改窗口顺手把外观冲了: %s / %s", theme, skin)
	}

	for _, bad := range []map[string]any{
		{"default_theme": "sepia"},
		{"default_skin": "neon"},
		{"default_skin": "glass"},
		{},
	} {
		if code, body := request("PUT", "/api/v1/admin/panel", admin, bad); code != 400 {
			t.Fatalf("坏值 %v 该被拒绝，得到 %d %s", bad, code, body)
		}
	}
	if code, _ := request("PUT", "/api/v1/admin/panel", "", map[string]any{"default_skin": "shadcn"}); code != 401 {
		t.Fatal("匿名改外观没被拒")
	}
}

// 计费汇总单位：后台设置、读回设置，并随 /theme-default.js 交给公开面板首屏。
// 未知币种（汇率表里没有）不能存，匿名不能改。
func TestPanelPriceUnitSetting(t *testing.T) {
	s, err := storage.Open(filepath.Join(t.TempDir(), "unit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	admin := model.Secret()
	server := httptest.NewServer((&API{Store: s, AdminToken: admin, Assets: web.Handler()}).Handler())
	defer server.Close()
	request := func(method, path, token string, body any) (int, []byte) {
		t.Helper()
		var data []byte
		if body != nil {
			data, _ = json.Marshal(body)
		}
		req, _ := http.NewRequest(method, server.URL+path, bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
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

	// 没配置就是内置默认（CNY），首屏脚本里也写着它。
	code, body := request("GET", "/api/v1/admin/panel", admin, nil)
	if code != 200 {
		t.Fatalf("settings: %d %s", code, body)
	}
	var view struct {
		PriceUnit string `json:"price_unit"`
	}
	if err = json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}
	if view.PriceUnit != model.DefaultPriceUnit {
		t.Fatalf("未配置时该是 %s，得到 %s", model.DefaultPriceUnit, view.PriceUnit)
	}
	if _, js := request("GET", "/theme-default.js", "", nil); !strings.Contains(string(js), `__tzaPriceUnit="`+model.DefaultPriceUnit+`"`) {
		t.Fatalf("首屏脚本没带默认汇总单位: %s", js)
	}

	// 存美元，读回与首屏脚本都跟上；小写顺带归一成大写。
	if code, body := request("PUT", "/api/v1/admin/panel", admin, map[string]any{"price_unit": "usd"}); code != 204 {
		t.Fatalf("PUT price_unit: %d %s", code, body)
	}
	code, body = request("GET", "/api/v1/admin/panel", admin, nil)
	if code != 200 {
		t.Fatalf("settings: %d %s", code, body)
	}
	if err = json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}
	if view.PriceUnit != "USD" {
		t.Fatalf("设置没保存: %s", view.PriceUnit)
	}
	if _, js := request("GET", "/theme-default.js", "", nil); !strings.Contains(string(js), `__tzaPriceUnit="USD"`) {
		t.Fatalf("首屏脚本没跟上汇总单位: %s", js)
	}

	// 未知币种被拒；改单位不动其他设置。
	for _, bad := range []string{"USDT", "", "CNY2"} {
		if code, body := request("PUT", "/api/v1/admin/panel", admin, map[string]any{"price_unit": bad}); code != 400 {
			t.Fatalf("坏单位 %q 该被拒绝，得到 %d %s", bad, code, body)
		}
	}
	code, body = request("GET", "/api/v1/admin/panel", admin, nil)
	if code != 200 {
		t.Fatalf("settings: %d %s", code, body)
	}
	var after struct {
		PriceUnit string `json:"price_unit"`
		SiteName  string `json:"site_name"`
	}
	if err = json.Unmarshal(body, &after); err != nil {
		t.Fatal(err)
	}
	if after.PriceUnit != "USD" {
		t.Fatalf("被拒的请求改掉了单位: %s", after.PriceUnit)
	}
	if code, _ := request("PUT", "/api/v1/admin/panel", "", map[string]any{"price_unit": "USD"}); code != 401 {
		t.Fatal("匿名改汇总单位没被拒")
	}
}
