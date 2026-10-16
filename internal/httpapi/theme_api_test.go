package httpapi

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/storage"
	"github.com/leeska/TZA-Probe/internal/theme"
	"github.com/leeska/TZA-Probe/web"
)

// 主题包走完整条路：后台上传 → 首屏脚本列出它（带样式表地址）→ 静态文件可取 →
// 能设成前台默认 → 删除后默认退回内置。示例主题包 examples/themes/matcha 就是这里上传的那份。
func TestThemePackageLifecycle(t *testing.T) {
	dir := t.TempDir()
	s, err := storage.Open(filepath.Join(dir, "themes.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	admin := model.Secret()
	server := httptest.NewServer((&API{Store: s, AdminToken: admin, Assets: web.Handler(), Themes: &theme.Store{Dir: filepath.Join(dir, "themes")}}).Handler())
	defer server.Close()
	request := func(method, path, token, ctype string, body []byte) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, server.URL+path, bytes.NewReader(body))
		req.Header.Set("Content-Type", ctype)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// 把仓库里的示例主题包打成 zip（与用户用 zip -r 打的一样：带一层顶层目录）。
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range []string{"theme.json", "theme.css"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "examples", "themes", "matcha", name))
		if err != nil {
			t.Fatal(err)
		}
		w, _ := zw.Create("matcha/" + name)
		w.Write(data)
	}
	zw.Close()

	if code, _ := request("POST", "/api/v1/admin/themes", "", "application/zip", buf.Bytes()); code != 401 {
		t.Fatalf("匿名上传主题包该被拒，得到 %d", code)
	}
	code, body := request("POST", "/api/v1/admin/themes", admin, "application/zip", buf.Bytes())
	if code != 200 || !strings.Contains(body, `"id":"matcha"`) {
		t.Fatalf("上传示例主题包: %d %s", code, body)
	}

	// 首屏脚本：清单里有它，样式表地址带版本号。
	_, boot := request("GET", "/theme-default.js", "", "", nil)
	if !strings.Contains(boot, "window.__tzaThemes=") || !strings.Contains(boot, `"id":"matcha"`) || !strings.Contains(boot, `/themes/matcha/theme.css?v=`) {
		t.Fatalf("首屏脚本没列出主题包: %s", boot)
	}
	if !strings.Contains(boot, `"id":"glass"`) || !strings.Contains(boot, `"builtin":true`) {
		t.Fatalf("首屏脚本要同时列出内置皮肤: %s", boot)
	}
	if code, css := request("GET", "/themes/matcha/theme.css", "", "", nil); code != 200 || !strings.Contains(css, `[data-skin="matcha"]`) {
		t.Fatalf("主题包样式表: %d", code)
	}

	// 能设成前台默认；不存在的 id 照旧被拒。
	look, _ := json.Marshal(map[string]any{"default_skin": "matcha"})
	if code, body := request("PUT", "/api/v1/admin/panel", admin, "application/json", look); code != 204 {
		t.Fatalf("把主题包设成默认: %d %s", code, body)
	}
	if _, boot := request("GET", "/theme-default.js", "", "", nil); !strings.Contains(boot, `skin:"matcha"`) {
		t.Fatalf("默认皮肤没写进首屏脚本: %s", boot)
	}
	bad, _ := json.Marshal(map[string]any{"default_skin": "neon"})
	if code, _ := request("PUT", "/api/v1/admin/panel", admin, "application/json", bad); code != 400 {
		t.Fatal("不存在的皮肤该被拒")
	}

	// 坏包整包拒收，错误信息说得清是哪里不对。
	var evil bytes.Buffer
	ez := zip.NewWriter(&evil)
	w, _ := ez.Create("theme.json")
	w.Write([]byte(`{"id":"evil","name":"x","chrome":{"light":"#ffffff","dark":"#000000"},"swatch":["#000000","#000000","#000000"],"styles":["a.css"]}`))
	w, _ = ez.Create("a.css")
	w.Write([]byte("a{}"))
	w, _ = ez.Create("run.js")
	w.Write([]byte("alert(1)"))
	ez.Close()
	if code, body := request("POST", "/api/v1/admin/themes", admin, "application/zip", evil.Bytes()); code != 400 || !strings.Contains(body, "不收脚本") {
		t.Fatalf("带脚本的主题包该被拒: %d %s", code, body)
	}

	// 删除：默认皮肤退回内置，静态文件 404，内置的删不掉。
	if code, _ := request("DELETE", "/api/v1/admin/themes/glass", admin, "", nil); code != 400 {
		t.Fatal("内置皮肤不能删")
	}
	if code, body := request("DELETE", "/api/v1/admin/themes/matcha", admin, "", nil); code != 204 {
		t.Fatalf("删除主题包: %d %s", code, body)
	}
	if _, boot := request("GET", "/theme-default.js", "", "", nil); !strings.Contains(boot, `skin:"shadcn"`) || strings.Contains(boot, "matcha") {
		t.Fatalf("删掉后默认该退回内置、清单里不该再有它: %s", boot)
	}
	if code, _ := request("GET", "/themes/matcha/theme.css", "", "", nil); code != 404 {
		t.Fatal("删掉后静态文件还在")
	}
	if code, _ := request("DELETE", "/api/v1/admin/themes/matcha", admin, "", nil); code != 404 {
		t.Fatal("删两次该是 404")
	}
}
