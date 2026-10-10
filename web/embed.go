// Package web embeds the independent, dependency-free browser application.
package web

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
)

//go:embed *.html *.css *.js *.svg anime/*
var files embed.FS

// asset is one embedded file prepared once at startup: its validator and, for
// text, a gzip copy.
type asset struct {
	data, gz    []byte
	etag, ctype string
}

func Handler() http.Handler {
	assets := map[string]*asset{}
	fs.WalkDir(files, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := files.ReadFile(name)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		a := &asset{data: data, etag: `"` + hex.EncodeToString(sum[:8]) + `"`, ctype: mime.TypeByExtension(path.Ext(name))}
		if strings.HasSuffix(name, ".html") {
			a.ctype = "text/html; charset=utf-8"
		}
		// webp 本身已压缩，再 gzip 只是白费 CPU；文本类（JS / CSS / SVG / HTML）能缩到三成左右。
		if !strings.HasPrefix(a.ctype, "image/") || strings.HasPrefix(a.ctype, "image/svg") {
			var buf bytes.Buffer
			zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
			zw.Write(data)
			zw.Close()
			if buf.Len() < len(data) {
				a.gz = buf.Bytes()
			}
		}
		assets["/"+name] = a
		return nil
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			w.WriteHeader(405)
			return
		}
		name := r.URL.Path
		if name == "/" || name == "/admin" || strings.HasPrefix(name, "/admin/") {
			name = "/index.html"
		}
		a, ok := assets[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		// 静态资源（app.js / anime.css / 各皮肤 css / 立绘）也要每次回源校验：
		// 之前只有 index.html 带 no-cache，浏览器会按启发式缓存把 app.js 留很久，
		// 于是服务端已经换成新版、用户开着的标签页刷新后还是旧界面（"线上没实现"就是这么来的）。
		// 回源校验靠 ETag（内容哈希）：没变就回 304 空体，不再每次刷新把一兆多的立绘和脚本整份重下。
		h := w.Header()
		h.Set("Cache-Control", "no-cache")
		h.Set("ETag", a.etag)
		h.Set("Content-Type", a.ctype)
		if a.gz != nil {
			h.Set("Vary", "Accept-Encoding")
		}
		if match := r.Header.Get("If-None-Match"); match != "" && etagMatch(match, a.etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		body := a.data
		if a.gz != nil && acceptsGzip(r) {
			body = a.gz
			h.Set("Content-Encoding", "gzip")
		}
		h.Set("Content-Length", strconv.Itoa(len(body)))
		if r.Method == "GET" {
			w.Write(body)
		}
	})
}

// etagMatch 认 If-None-Match 里的列表与 *；反代压缩后可能把强校验改成 W/ 前缀，也算命中。
func etagMatch(header, etag string) bool {
	for _, v := range strings.Split(header, ",") {
		v = strings.TrimPrefix(strings.TrimSpace(v), "W/")
		if v == "*" || v == etag {
			return true
		}
	}
	return false
}

func acceptsGzip(r *http.Request) bool {
	for _, v := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		coding, params, _ := strings.Cut(strings.TrimSpace(v), ";")
		if strings.EqualFold(strings.TrimSpace(coding), "gzip") {
			return strings.ReplaceAll(strings.TrimSpace(params), " ", "") != "q=0"
		}
	}
	return false
}
