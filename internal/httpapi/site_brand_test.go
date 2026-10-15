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

	"github.com/leeska/TZA-Probe/internal/brand"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/storage"
	"github.com/leeska/TZA-Probe/web"
)

// 站点名称与 logo：后台存进去、读回来、在首屏脚本里交给浏览器（/theme-default.js 的
// window.__tzaBrand），logo 由 /site-logo 提供；清掉就回到内置品牌。坏值与匿名请求一律拒。
func TestSiteBrand(t *testing.T) {
	s, err := storage.Open(filepath.Join(t.TempDir(), "brand.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	admin := model.Secret()
	logos := &brand.Store{Dir: filepath.Join(t.TempDir(), "branding")}
	server := httptest.NewServer((&API{Store: s, AdminToken: admin, Assets: web.Handler(), Logo: logos}).Handler())
	defer server.Close()
	request := func(method, path, token, ctype string, body []byte) (int, http.Header, []byte) {
		t.Helper()
		req, _ := http.NewRequest(method, server.URL+path, bytes.NewReader(body))
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
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
		return resp.StatusCode, resp.Header, b
	}
	panel := func() model.PanelSettings {
		t.Helper()
		code, _, body := request("GET", "/api/v1/admin/panel", admin, "", nil)
		if code != 200 {
			t.Fatalf("panel: %d %s", code, body)
		}
		var v model.PanelSettings
		if err := json.Unmarshal(body, &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	boot := func() siteBrand {
		t.Helper()
		_, _, body := request("GET", "/theme-default.js", "", "", nil)
		for _, line := range strings.Split(string(body), "\n") {
			if v, ok := strings.CutPrefix(line, "window.__tzaBrand="); ok {
				var b siteBrand
				if err := json.Unmarshal([]byte(strings.TrimSuffix(v, ";")), &b); err != nil {
					t.Fatalf("首屏品牌不是 JSON: %q", line)
				}
				return b
			}
		}
		t.Fatalf("首屏脚本没带品牌: %s", body)
		return siteBrand{}
	}
	putName := func(name string) int {
		t.Helper()
		data, _ := json.Marshal(map[string]string{"site_name": name})
		code, _, _ := request("PUT", "/api/v1/admin/panel", admin, "application/json", data)
		return code
	}

	// 没配过：两项都空，首屏也是空（前端显示内置的 TZA Probe）。
	if p := panel(); p.SiteName != "" || p.SiteLogo != "" {
		t.Fatalf("未配置: %+v", p)
	}
	if b := boot(); b != (siteBrand{}) {
		t.Fatalf("未配置的首屏品牌: %+v", b)
	}
	if code, _, _ := request("GET", brand.Path, "", "", nil); code != 404 {
		t.Fatalf("没有 logo 时 /site-logo 该 404，得到 %d", code)
	}

	// 名称：去掉首尾空白存下；名称里的引号、尖括号原样留着，首屏脚本里转义成安全的 JSON。
	if code := putName(`  橙子 "</script>" `); code != 204 {
		t.Fatalf("PUT site_name: %d", code)
	}
	if p := panel(); p.SiteName != `橙子 "</script>"` {
		t.Fatalf("名称: %q", p.SiteName)
	}
	_, _, js := request("GET", "/theme-default.js", "", "", nil)
	if strings.Contains(string(js), "</script>") {
		t.Fatalf("首屏脚本里的名称没转义: %s", js)
	}
	if b := boot(); b.Name != `橙子 "</script>"` {
		t.Fatalf("首屏名称: %+v", b)
	}
	for _, bad := range []string{strings.Repeat("名", model.MaxSiteNameLen+1), "a\nb"} {
		if code := putName(bad); code != 400 {
			t.Fatalf("坏名称 %q 该被拒，得到 %d", bad, code)
		}
	}

	// logo：请求体就是图片，不看 Content-Type；坏图拒掉，好图存下并在三处一致。
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 8"><rect width="8" height="8" fill="#e8590c"/></svg>`)
	if code, _, body := request("PUT", "/api/v1/admin/panel/logo", admin, "image/png", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)); code != 400 || !strings.Contains(string(body), "script") {
		t.Fatalf("带脚本的 SVG 该被拒: %d %s", code, body)
	}
	if code, _, body := request("PUT", "/api/v1/admin/panel/logo", admin, "image/png", bytes.Repeat([]byte{0}, brand.MaxLogo+1)); code != 413 {
		t.Fatalf("超大 logo 该 413: %d %s", code, body)
	}
	code, _, body := request("PUT", "/api/v1/admin/panel/logo", admin, "application/octet-stream", svg)
	var saved struct {
		SiteLogo string `json:"site_logo"`
	}
	if code != 200 || json.Unmarshal(body, &saved) != nil || !strings.HasPrefix(saved.SiteLogo, brand.Path+"?v=") {
		t.Fatalf("上传 logo: %d %s", code, body)
	}
	if p := panel(); p.SiteLogo != saved.SiteLogo {
		t.Fatalf("panel 里的 logo: %q", p.SiteLogo)
	}
	if b := boot(); b.Logo != saved.SiteLogo {
		t.Fatalf("首屏 logo: %+v", b)
	}
	code, header, served := request("GET", saved.SiteLogo, "", "", nil)
	if code != 200 || header.Get("Content-Type") != "image/svg+xml" || !bytes.Equal(served, svg) {
		t.Fatalf("/site-logo: %d %q", code, header.Get("Content-Type"))
	}
	// 页面的 CSP 不适用于直接打开的图片：它有自己更严的那份。
	if csp := header.Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") || strings.Contains(csp, "script-src 'self'") {
		t.Fatalf("/site-logo 的 CSP: %q", csp)
	}

	// 匿名与跨站一律拒。
	if code, _, _ := request("PUT", "/api/v1/admin/panel/logo", "", "image/svg+xml", svg); code != 401 {
		t.Fatalf("匿名上传 logo 没被拒: %d", code)
	}
	if code, _, _ := request("DELETE", "/api/v1/admin/panel/logo", "", "", nil); code != 401 {
		t.Fatalf("匿名删除 logo 没被拒: %d", code)
	}

	// 清除：两项都回到空，首屏与 /site-logo 一起回到默认。
	if code, _, _ := request("DELETE", "/api/v1/admin/panel/logo", admin, "", nil); code != 204 {
		t.Fatalf("DELETE logo: %d", code)
	}
	if code := putName(""); code != 204 {
		t.Fatalf("清空名称: %d", code)
	}
	if p := panel(); p.SiteName != "" || p.SiteLogo != "" {
		t.Fatalf("清除后: %+v", p)
	}
	if b := boot(); b != (siteBrand{}) {
		t.Fatalf("清除后的首屏品牌: %+v", b)
	}
	if code, _, _ := request("GET", brand.Path, "", "", nil); code != 404 {
		t.Fatalf("清除后 /site-logo 该 404，得到 %d", code)
	}
}
