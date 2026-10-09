// Package web embeds the independent, dependency-free browser application.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed *.html *.css *.js *.svg anime/*
var files embed.FS

func Handler() http.Handler {
	root, _ := fs.Sub(files, ".")
	fileServer := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			w.WriteHeader(405)
			return
		}
		if r.URL.Path == "/" || r.URL.Path == "/admin" || strings.HasPrefix(r.URL.Path, "/admin/") {
			data, _ := files.ReadFile("index.html")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			if r.Method == "GET" {
				w.Write(data)
			}
			return
		}
		// 静态资源（app.js / anime.css / 各皮肤 css / 立绘）也要每次回源校验：
		// 之前只有 index.html 带 no-cache，浏览器会按启发式缓存把 app.js 留很久，
		// 于是服务端已经换成新版、用户开着的标签页刷新后还是旧界面（"线上没实现"就是这么来的）。
		w.Header().Set("Cache-Control", "no-cache")
		fileServer.ServeHTTP(w, r)
	})
}
