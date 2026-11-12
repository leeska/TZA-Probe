package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/leeska/TZA-Probe/internal/market"
	"github.com/leeska/TZA-Probe/internal/model"
)

// installDeadline outlasts the server's 30-second WriteTimeout: an install
// waits for CC twice and then downloads up to 20 MB.
const installDeadline = 3 * time.Minute

// marketRoutes is the panel's view of TZA-CC. The browser only talks to these
// routes; the Server fetches the catalog, preview images and packages itself,
// so the page CSP stays 'self' and CC never sees the visitor.
func (a *API) marketRoutes(route func(string, http.HandlerFunc)) {
	route("GET /api/v1/admin/market/status", func(w http.ResponseWriter, r *http.Request) {
		write(w, 200, a.Market.Status())
	})
	route("GET /api/v1/admin/market/catalog", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		page := 0
		if raw := q.Get("page"); raw != "" {
			var err error
			if page, err = strconv.Atoi(raw); err != nil {
				write(w, 400, map[string]string{"error": "page 须为整数"})
				return
			}
		}
		out, err := a.Market.Catalog(r.Context(), market.Query{Type: q.Get("type"), Kind: q.Get("kind"), For: q.Get("for"), Q: q.Get("q"), Page: page})
		if err != nil {
			marketFail(w, err)
			return
		}
		write(w, 200, out)
	})
	route("GET /api/v1/admin/market/packages/{id}", func(w http.ResponseWriter, r *http.Request) {
		out, err := a.Market.Package(r.Context(), r.PathValue("id"))
		if err != nil {
			marketFail(w, err)
			return
		}
		write(w, 200, out)
	})
	// 安装与更新是同一个动作：version 省略即装当前 Probe 能用的最新版，同 id 的已装包（市场或本地）被替换。
	route("POST /api/v1/admin/market/install", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			ID      string `json:"id"`
			Version string `json:"version"`
		}
		if !decode(w, r, &v) {
			return
		}
		http.NewResponseController(w).SetWriteDeadline(time.Now().Add(installDeadline))
		m, err := a.Market.Install(r.Context(), v.ID, v.Version)
		if err != nil {
			marketFail(w, err)
			return
		}
		write(w, 200, m)
	})
	route("GET /api/v1/admin/market/updates", func(w http.ResponseWriter, r *http.Request) {
		out, err := a.Market.Updates(r.Context())
		if err != nil {
			marketFail(w, err)
			return
		}
		write(w, 200, map[string]any{"updates": out})
	})
	// 绑定实例（设备码流程）：开始 → 浏览器按 interval 轮询 → 成功后凭据存在数据目录的 cc-instance。
	route("POST /api/v1/admin/market/connect", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Name string `json:"name"`
		}
		if r.ContentLength != 0 && !decode(w, r, &v) {
			return
		}
		out, err := a.Market.Connect(r.Context(), v.Name)
		if err != nil {
			marketFail(w, err)
			return
		}
		write(w, 200, out)
	})
	route("POST /api/v1/admin/market/connect/poll", func(w http.ResponseWriter, r *http.Request) {
		out, err := a.Market.Poll(r.Context())
		if err != nil {
			marketFail(w, err)
			return
		}
		write(w, 200, out)
	})
	route("DELETE /api/v1/admin/market/connect", func(w http.ResponseWriter, r *http.Request) {
		warning, err := a.Market.Disconnect(r.Context())
		if err != nil {
			marketFail(w, err)
			return
		}
		if warning != "" {
			write(w, 200, map[string]string{"warning": warning})
			return
		}
		w.WriteHeader(204)
	})
	// 预览图代理：只取 TZA-CC 同源地址上的图片（类型白名单、5 MB 上限），浏览器不直连 CC。
	route("GET /api/v1/admin/market/media", func(w http.ResponseWriter, r *http.Request) {
		m, err := a.Market.Media(r.Context(), r.URL.Query().Get("url"))
		if err != nil {
			marketFail(w, err)
			return
		}
		w.Header().Set("Content-Type", m.Type)
		w.Header().Set("Cache-Control", "private, max-age=3600")
		w.Header().Set("Content-Length", strconv.Itoa(len(m.Body)))
		w.WriteHeader(200)
		w.Write(m.Body)
	})
}

// marketFail maps market errors: not configured / no trusted key is 503, a
// CC answer or a package that fails verification is 502 (a CC 404 / 409
// keeps its status), bad input and missing packages go through fail.
func marketFail(w http.ResponseWriter, err error) {
	var remote *market.RemoteError
	switch {
	case errors.Is(err, market.ErrNotConfigured), errors.Is(err, market.ErrNoTrustedKeys):
		write(w, 503, map[string]string{"error": err.Error()})
	case errors.Is(err, model.ErrInvalid), errors.Is(err, model.ErrNotFound), errors.Is(err, model.ErrConflict):
		fail(w, err)
	case errors.As(err, &remote) && (remote.Status == http.StatusNotFound || remote.Status == http.StatusConflict):
		// 404 no such package / version; 409 e.g. the release needs a newer Probe,
		// or the account reached its instance limit while binding.
		write(w, remote.Status, map[string]string{"error": err.Error()})
	case errors.As(err, &remote), errors.Is(err, market.ErrUnreachable), errors.Is(err, market.ErrRejected):
		write(w, 502, map[string]string{"error": err.Error()})
	default:
		fail(w, err)
	}
}
