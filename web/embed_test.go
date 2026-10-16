package web

import (
	"compress/gzip"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// 静态资源回源校验要能拿到 304，否则 no-cache 等于每次刷新都整份重下。
func TestAssetsRevalidateAndCompress(t *testing.T) {
	h := Handler()
	get := func(path string, header map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		for k, v := range header {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	first := get("/app.js", nil)
	etag := first.Header().Get("ETag")
	if first.Code != 200 || etag == "" || first.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("first fetch: %d etag=%q cache=%q", first.Code, etag, first.Header().Get("Cache-Control"))
	}
	if again := get("/app.js", map[string]string{"If-None-Match": "W/" + etag}); again.Code != 304 || again.Body.Len() != 0 {
		t.Fatalf("revalidation: %d with %d bytes", again.Code, again.Body.Len())
	}
	zipped := get("/app.js", map[string]string{"Accept-Encoding": "br, gzip"})
	if zipped.Header().Get("Content-Encoding") != "gzip" || zipped.Body.Len() >= first.Body.Len() {
		t.Fatalf("gzip: encoding=%q %d vs %d bytes", zipped.Header().Get("Content-Encoding"), zipped.Body.Len(), first.Body.Len())
	}
	zr, err := gzip.NewReader(zipped.Body)
	if err != nil {
		t.Fatal(err)
	}
	if plain, _ := io.ReadAll(zr); string(plain) != first.Body.String() {
		t.Fatal("gzip body differs from plain body")
	}
	// 官方主题包（仓库的 themes/）的样式表与图片只在包里，不编进二进制：包里的每个文件都不该在这里。
	packaged := 0
	filepath.WalkDir(filepath.Join("..", "themes"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Base(p) == "theme.json" {
			return err
		}
		packaged++
		rel, _ := filepath.Rel(filepath.Join("..", "themes"), p)
		parts := strings.SplitN(filepath.ToSlash(rel), "/", 2)
		if w := get("/"+parts[len(parts)-1], nil); w.Code != http.StatusNotFound {
			t.Errorf("主题包里的 %s 不该编进二进制，得到 %d", rel, w.Code)
		}
		return nil
	})
	if packaged == 0 {
		t.Fatal("没找到仓库里的官方主题包（../themes）")
	}
	for _, path := range []string{"/", "/admin", "/admin/tasks"} {
		if w := get(path, nil); w.Code != 200 || w.Header().Get("Content-Type") != "text/html; charset=utf-8" {
			t.Fatalf("%s: %d %q", path, w.Code, w.Header().Get("Content-Type"))
		}
	}
	if w := get("/missing.js", nil); w.Code != http.StatusNotFound {
		t.Fatalf("missing asset: %d", w.Code)
	}
}
