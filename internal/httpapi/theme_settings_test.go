package httpapi

import (
	"archive/zip"
	"bytes"
	"io"
	"io/fs"
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

// 主题设置：二次元在 theme.json 里声明的菜单（角色包、眨眼与呼吸）由后台保存，随首屏清单发给访客；
// 没声明过的键、不合规的值、没有设置菜单的主题都拒收。
func TestThemeSettings(t *testing.T) {
	dir := t.TempDir()
	s, err := storage.Open(filepath.Join(dir, "settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	admin := model.Secret()
	server := httptest.NewServer((&API{Store: s, AdminToken: admin, Assets: web.Handler(), Themes: &theme.Store{Dir: filepath.Join(dir, "themes")}}).Handler())
	defer server.Close()
	request := func(method, path, ctype string, body []byte) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, server.URL+path, bytes.NewReader(body))
		req.Header.Set("Content-Type", ctype)
		req.Header.Set("Authorization", "Bearer "+admin)
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// 装仓库里的官方二次元主题包。
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	root := filepath.Join("..", "..", "themes", "anime")
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		data, _ := os.ReadFile(p)
		w, _ := zw.Create(filepath.ToSlash(rel))
		w.Write(data)
		return nil
	})
	zw.Close()
	if code, body := request("POST", "/api/v1/admin/themes", "application/zip", buf.Bytes()); code != 200 {
		t.Fatalf("install anime: %d %s", code, body)
	}

	put := func(id, body string) (int, string) {
		return request("PUT", "/api/v1/admin/themes/"+id+"/settings", "application/json", []byte(body))
	}
	if code, body := put("anime", `{"motion":"off"}`); code != 200 || !strings.Contains(body, `"motion":"off"`) {
		t.Fatalf("set motion: %d %s", code, body)
	}
	if code, body := put("anime", `{"characters":["builtin","europe","europe"]}`); code != 200 || !strings.Contains(body, `"characters":["builtin","europe"]`) || !strings.Contains(body, `"motion":"off"`) {
		t.Fatalf("set characters (merged with motion, deduplicated): %d %s", code, body)
	}
	for name, c := range map[string]struct {
		id, body string
		want     int
	}{
		"unknown key":   {"anime", `{"nope":"on"}`, 400},
		"bad toggle":    {"anime", `{"motion":"maybe"}`, 400},
		"bad pack list": {"anime", `{"characters":"europe"}`, 400},
		"empty patch":   {"anime", `{}`, 400},
		"no settings":   {"glass", `{"motion":"off"}`, 400},
		"no such theme": {"matcha", `{"motion":"off"}`, 404},
	} {
		if code, body := put(c.id, c.body); code != c.want {
			t.Errorf("%s: %d %s, want %d", name, code, body, c.want)
		}
	}

	_, list := request("GET", "/api/v1/admin/themes", "", nil)
	if !strings.Contains(list, `"type":"characters","label":"角色包"`) || !strings.Contains(list, `"options":{"characters":["builtin","europe"],"motion":"off"}`) {
		t.Fatalf("admin list lacks the settings menu or its values: %s", list)
	}
	_, boot := request("GET", "/theme-default.js", "", nil)
	if !strings.Contains(boot, `"options":{"characters":["builtin","europe"],"motion":"off"}`) {
		t.Fatalf("the boot script must carry the values to visitors: %s", boot)
	}
}
