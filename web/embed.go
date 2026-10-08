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
		fileServer.ServeHTTP(w, r)
	})
}
