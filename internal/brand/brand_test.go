package brand

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var (
	pngLogo = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)
	svgLogo = []byte(`<?xml version="1.0"?>
<!-- 一枚合规的 logo：渐变、内部引用、内联样式都可以 -->
<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" viewBox="0 0 64 64">
  <defs><linearGradient id="g"><stop offset="0" stop-color="#e8590c"/></linearGradient><circle id="c" r="10"/></defs>
  <style>circle { fill: #fff }</style>
  <rect width="64" height="64" rx="14" fill="url(#g)"/><use xlink:href="#c" x="32" y="32"/>
</svg>`)
)

// 类型只看内容：常见的图标格式都收，SVG 里能跑脚本、能往外取东西的写法一律拒。
func TestDetect(t *testing.T) {
	for name, tc := range map[string]struct {
		data  []byte
		ctype string
	}{
		"png":  {pngLogo, "image/png"},
		"jpeg": {[]byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0}, "image/jpeg"},
		"gif":  {[]byte("GIF89a......"), "image/gif"},
		"webp": {[]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), "image/webp"},
		"ico":  {[]byte{0, 0, 1, 0, 1, 0}, "image/x-icon"},
		"svg":  {svgLogo, "image/svg+xml"},
	} {
		if _, ctype, err := Detect(tc.data); err != nil || ctype != tc.ctype {
			t.Errorf("%s: 得到 %q %v，期望 %q", name, ctype, err, tc.ctype)
		}
	}
	for name, data := range map[string]string{
		"空":              "",
		"纯文本":            "hello",
		"HTML":           `<html><body><svg></svg></body></html>`,
		"script":         `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`,
		"大写 SCRIPT":      `<svg xmlns="http://www.w3.org/2000/svg"><SCRIPT>alert(1)</SCRIPT></svg>`,
		"onload":         `<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`,
		"foreignObject":  `<svg xmlns="http://www.w3.org/2000/svg"><foreignObject><div/></foreignObject></svg>`,
		"外链 href":        `<svg xmlns="http://www.w3.org/2000/svg"><image href="https://evil.example/x.png"/></svg>`,
		"外链 xlink:href":  `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"><use xlink:href="other.svg#a"/></svg>`,
		"javascript: 链接": `<svg xmlns="http://www.w3.org/2000/svg"><a href="#x"><text>JavaScript:alert(1)</text></a></svg>`,
		"DOCTYPE 实体":     `<!DOCTYPE svg [<!ENTITY x "y">]><svg xmlns="http://www.w3.org/2000/svg">&x;</svg>`,
		"坏 XML":          `<svg xmlns="http://www.w3.org/2000/svg"><g></svg>`,
		"超大":             string(append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, MaxLogo)...)),
	} {
		if _, _, err := Detect([]byte(data)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s 该被拒，得到 %v", name, err)
		}
	}
}

func TestStoreLifecycle(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "branding")
	s := &Store{Dir: dir}
	get := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("GET", Path, nil))
		return w
	}
	if s.URL() != "" || get().Code != http.StatusNotFound {
		t.Fatal("没上传时不该有 logo")
	}
	if err := s.Save([]byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="x()"/>`)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("坏 SVG 该被拒: %v", err)
	}
	if s.URL() != "" {
		t.Fatal("被拒的上传留下了 logo")
	}

	if err := s.Save(svgLogo); err != nil {
		t.Fatal(err)
	}
	svgURL := s.URL()
	if !strings.HasPrefix(svgURL, Path+"?v=") {
		t.Fatalf("地址: %q", svgURL)
	}
	w := get()
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/svg+xml" || !bytes.Equal(w.Body.Bytes(), svgLogo) {
		t.Fatalf("SVG: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	// 直接打开 SVG 时也什么都跑不了。
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "sandbox") {
		t.Fatalf("SVG 缺少自己的 CSP: %q", csp)
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("缺 nosniff")
	}

	// 换成 PNG：地址版本跟着变，旧格式的文件不留。
	if err := s.Save(pngLogo); err != nil {
		t.Fatal(err)
	}
	if s.URL() == svgURL || get().Header().Get("Content-Type") != "image/png" {
		t.Fatalf("换图后没跟上: %q", s.URL())
	}
	if _, err := os.Stat(filepath.Join(dir, "logo.svg")); !os.IsNotExist(err) {
		t.Fatal("旧的 logo.svg 还在")
	}
	// 另一个 Store 读同一目录（重启后）也认得。
	if (&Store{Dir: dir}).URL() != s.URL() {
		t.Fatal("重启后 logo 地址变了")
	}

	if err := s.Remove(); err != nil {
		t.Fatal(err)
	}
	if s.URL() != "" || get().Code != http.StatusNotFound {
		t.Fatal("移除后还有 logo")
	}
	if err := s.Remove(); err != nil {
		t.Fatalf("重复移除: %v", err)
	}

	var none *Store
	if none.URL() != "" || none.Remove() != nil || none.Save(pngLogo) == nil {
		t.Fatal("nil Store 该没有 logo、也存不进去")
	}
}
