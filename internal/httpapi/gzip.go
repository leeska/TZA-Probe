package httpapi

import (
	"compress/gzip"
	"net/http"
	"strings"
	"sync"
)

// 公开面板每秒取一次 /nodes：10 台节点约 36KB JSON（每台带 24 项解锁明细与探测目标），
// gzip 后约 1.4KB。静态资源早就预压缩了，API 却一直明文发——反代默认的 gzip_types 也不含
// application/json。BestSpeed 足够：JSON 重复度高，快档已经缩到二十几分之一，CPU 只是零头。
var gzipWriters = sync.Pool{New: func() any {
	zw, _ := gzip.NewWriterLevel(nil, gzip.BestSpeed)
	return zw
}}

// gzipAPI compresses the response when the client accepts gzip. The decision is
// made at WriteHeader, so a body-less status (204, 304) goes out untouched.
func gzipAPI(w http.ResponseWriter, r *http.Request, next http.Handler) {
	w.Header().Add("Vary", "Accept-Encoding")
	if !acceptsGzip(r) {
		next.ServeHTTP(w, r)
		return
	}
	gw := &gzipResponse{ResponseWriter: w}
	defer gw.close()
	next.ServeHTTP(gw, r)
}

type gzipResponse struct {
	http.ResponseWriter
	zw      *gzip.Writer
	decided bool
}

func (g *gzipResponse) WriteHeader(code int) {
	if !g.decided {
		g.decided = true
		h := g.Header()
		if code != http.StatusNoContent && code != http.StatusNotModified && h.Get("Content-Encoding") == "" {
			h.Del("Content-Length")
			h.Set("Content-Encoding", "gzip")
			g.zw = gzipWriters.Get().(*gzip.Writer)
			g.zw.Reset(g.ResponseWriter)
		}
	}
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzipResponse) Write(b []byte) (int, error) {
	if !g.decided {
		g.WriteHeader(http.StatusOK)
	}
	if g.zw != nil {
		return g.zw.Write(b)
	}
	return g.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the connection (deadlines).
func (g *gzipResponse) Unwrap() http.ResponseWriter { return g.ResponseWriter }

func (g *gzipResponse) close() {
	if g.zw != nil {
		g.zw.Close()
		gzipWriters.Put(g.zw)
	}
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
