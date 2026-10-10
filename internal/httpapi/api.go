// Package httpapi adapts the v1 HTTP contract to persistence ports. The browser
// and Agent use this contract; neither imports the storage implementation.
package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/leeska/TZA-Probe/internal/agentdist"
	"github.com/leeska/TZA-Probe/internal/catalog"
	"github.com/leeska/TZA-Probe/internal/mcp"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/notify"
)

type Repository interface {
	ControlTask(context.Context, string, model.TaskAction, time.Time) error
	TaskRuns(context.Context, string, time.Time) ([]model.TaskRun, error)
	CorrectTraffic(context.Context, string, int64, int64, time.Time) error
	NetworkHistory(context.Context, string, int, time.Time) ([]model.NetworkSeries, error)
	IsPublic(context.Context, string) (bool, error)
	Nodes(context.Context, time.Time) ([]model.NodeView, error)
	SaveNode(context.Context, model.Node, bool) (string, string, error)
	SaveNodeOrder(context.Context, []string) error
	DeleteNode(context.Context, string) error
	Rotate(context.Context, string) (string, error)
	Authenticate(context.Context, string) (string, error)
	Report(context.Context, string, model.Metrics, time.Time) error
	History(context.Context, string, int, time.Time) ([]model.Sample, error)
	Tasks(context.Context) ([]model.Task, error)
	SaveTask(context.Context, model.Task, bool) (string, error)
	DeleteTask(context.Context, string) error
	Lease(context.Context, string, time.Time) ([]model.Job, error)
	Finish(context.Context, string, model.Result, time.Time) error
	Observations(context.Context, string, time.Time) ([]model.Observation, error)
	Unlocks(context.Context, string) (*model.UnlockSummary, error)
	RecentLatencySamples(context.Context, []string, int, time.Time) ([]model.LatencySample, error)
	Setting(context.Context, string) (string, error)
	SetSetting(context.Context, string, string) error
	Events(context.Context, bool, time.Time) ([]model.Event, error)
}
type API struct {
	Store         Repository
	Notifier      notify.Sender
	AdminToken    string
	Assets        http.Handler
	Agents        *agentdist.Dist // nil disables /install.sh and /dl/
	mu            sync.Mutex
	sessions      map[string]time.Time
	loginWindow   time.Time
	loginAttempts int
}

func (a *API) Handler() http.Handler {
	a.sessions = map[string]time.Time{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		write(w, 200, map[string]any{"version": model.Version, "protocol": model.Protocol})
	})
	mux.HandleFunc("POST /api/v1/login", a.login)
	mux.HandleFunc("POST /api/v1/logout", a.admin(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("tza_session"); err == nil {
			a.mu.Lock()
			delete(a.sessions, model.Hash(c.Value))
			a.mu.Unlock()
		}
		http.SetCookie(w, &http.Cookie{Name: "tza_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
		w.WriteHeader(204)
	}))
	mux.HandleFunc("GET /api/v1/session", a.admin(func(w http.ResponseWriter, r *http.Request) { write(w, 200, map[string]bool{"authenticated": true}) }))
	mux.HandleFunc("GET /api/v1/nodes", a.nodes)
	mux.HandleFunc("GET /api/v1/nodes/{id}/history", a.history)
	mux.HandleFunc("GET /api/v1/nodes/{id}/network-history", a.networkHistory)
	mux.HandleFunc("GET /api/v1/nodes/{id}/observations", a.observations)
	mux.HandleFunc("GET /api/v1/latency-samples", a.latencySamples)
	mux.HandleFunc("GET /api/v1/unlock-catalog", a.unlockCatalog)
	mux.HandleFunc("GET /api/v1/nodes/{id}/unlocks", a.unlocks)
	mux.HandleFunc("GET /api/v1/admin/nodes", a.admin(a.nodes))
	mux.HandleFunc("POST /api/v1/admin/nodes", a.admin(a.saveNode))
	mux.HandleFunc("POST /api/v1/admin/nodes/order", a.admin(a.saveNodeOrder))
	mux.HandleFunc("PUT /api/v1/admin/nodes/{id}", a.admin(a.saveNode))
	mux.HandleFunc("DELETE /api/v1/admin/nodes/{id}", a.admin(func(w http.ResponseWriter, r *http.Request) {
		respond(w, a.Store.DeleteNode(r.Context(), r.PathValue("id")))
	}))
	mux.HandleFunc("POST /api/v1/admin/nodes/{id}/traffic", a.admin(func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Used  int64 `json:"used"`
			Cycle int64 `json:"cycle"`
		}
		if !decode(w, r, &v) {
			return
		}
		respond(w, a.Store.CorrectTraffic(r.Context(), r.PathValue("id"), v.Used, v.Cycle, time.Now()))
	}))
	mux.HandleFunc("POST /api/v1/admin/nodes/{id}/token", a.admin(func(w http.ResponseWriter, r *http.Request) {
		token, err := a.Store.Rotate(r.Context(), r.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, map[string]string{"token": token})
	}))
	mux.HandleFunc("GET /api/v1/admin/tasks", a.admin(func(w http.ResponseWriter, r *http.Request) {
		tasks, err := a.Store.Tasks(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, tasks)
	}))
	mux.HandleFunc("POST /api/v1/admin/tasks/{id}/control", a.admin(func(w http.ResponseWriter, r *http.Request) {
		var v model.TaskAction
		if !decode(w, r, &v) {
			return
		}
		respond(w, a.Store.ControlTask(r.Context(), r.PathValue("id"), v, time.Now()))
	}))
	mux.HandleFunc("GET /api/v1/admin/tasks/{id}/runs", a.admin(func(w http.ResponseWriter, r *http.Request) {
		out, err := a.Store.TaskRuns(r.Context(), r.PathValue("id"), time.Now())
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, out)
	}))
	mux.HandleFunc("POST /api/v1/admin/tasks", a.admin(a.saveTask))
	mux.HandleFunc("PUT /api/v1/admin/tasks/{id}", a.admin(a.saveTask))
	mux.HandleFunc("DELETE /api/v1/admin/tasks/{id}", a.admin(func(w http.ResponseWriter, r *http.Request) {
		respond(w, a.Store.DeleteTask(r.Context(), r.PathValue("id")))
	}))
	mux.HandleFunc("GET /api/v1/admin/catalog", a.admin(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(catalog.JSON)
	}))
	mux.HandleFunc("GET /api/v1/admin/events", a.admin(func(w http.ResponseWriter, r *http.Request) {
		events, err := a.Store.Events(r.Context(), false, time.Now())
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, events)
	}))
	mux.HandleFunc("GET /api/v1/admin/settings", a.admin(func(w http.ResponseWriter, r *http.Request) {
		config, err := notify.Status(r.Context(), a.Store)
		if err != nil {
			fail(w, err)
			return
		}
		window, _, err := a.latencyWindow(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		view := config.Public()
		view[model.SettingLatencyWindowMinutes] = window
		theme, skin, err := a.panelDefaults(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		view[model.SettingDefaultTheme] = theme
		view[model.SettingDefaultSkin] = skin
		write(w, 200, view)
	}))
	// 面板显示项单独一个出口：通知那套的语义是「空值即清空」，混在同一个 PUT 里
	// 会让只想改显示项的人顺手把 webhook 或 Bot Token 清掉。
	mux.HandleFunc("PUT /api/v1/admin/panel", a.admin(func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			LatencyWindowMinutes *int    `json:"latency_window_minutes"`
			DefaultTheme         *string `json:"default_theme"`
			DefaultSkin          *string `json:"default_skin"`
		}
		if !decode(w, r, &v) {
			return
		}
		if v.LatencyWindowMinutes == nil && v.DefaultTheme == nil && v.DefaultSkin == nil {
			write(w, 400, map[string]string{"error": "至少给一项面板显示设置"})
			return
		}
		if v.LatencyWindowMinutes != nil {
			if err := model.ValidateLatencyWindowMinutes(*v.LatencyWindowMinutes); err != nil {
				write(w, 400, map[string]string{"error": err.Error()})
				return
			}
		}
		if v.DefaultTheme != nil {
			if err := model.ValidatePanelTheme(*v.DefaultTheme); err != nil {
				write(w, 400, map[string]string{"error": err.Error()})
				return
			}
		}
		if v.DefaultSkin != nil {
			if err := model.ValidatePanelSkin(*v.DefaultSkin); err != nil {
				write(w, 400, map[string]string{"error": err.Error()})
				return
			}
		}
		if v.LatencyWindowMinutes != nil {
			if err := a.Store.SetSetting(r.Context(), model.SettingLatencyWindowMinutes, strconv.Itoa(*v.LatencyWindowMinutes)); err != nil {
				fail(w, err)
				return
			}
		}
		if v.DefaultTheme != nil {
			if err := a.Store.SetSetting(r.Context(), model.SettingDefaultTheme, *v.DefaultTheme); err != nil {
				fail(w, err)
				return
			}
		}
		if v.DefaultSkin != nil {
			if err := a.Store.SetSetting(r.Context(), model.SettingDefaultSkin, *v.DefaultSkin); err != nil {
				fail(w, err)
				return
			}
		}
		respond(w, nil)
	}))
	// 前台默认外观必须在样式表之前定下来，否则首屏会先按内置默认闪一下再跳到设置里的皮肤。
	// CSP 是 script-src 'self'，index.html 里写不了内联脚本，所以这里发一份几十字节的
	// 动态脚本，由 index.html 在 theme-boot.js 之前引用。
	mux.HandleFunc("GET /theme-default.js", func(w http.ResponseWriter, r *http.Request) {
		theme, skin, err := a.panelDefaults(r.Context())
		if err != nil {
			theme, skin = model.DefaultPanelTheme, model.DefaultPanelSkin
		}
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		if r.Method == http.MethodGet {
			fmt.Fprintf(w, "window.__tzaDefault={theme:%q,skin:%q};\n", theme, skin)
		}
	})
	mux.HandleFunc("PUT /api/v1/admin/settings", a.admin(func(w http.ResponseWriter, r *http.Request) {
		var v notify.Config
		if !decode(w, r, &v) {
			return
		}
		next, err := notify.Normalize(r.Context(), a.Store, v)
		if err != nil {
			write(w, 400, map[string]string{"error": err.Error()})
			return
		}
		respond(w, notify.Persist(r.Context(), a.Store, next))
	}))
	mux.HandleFunc("POST /api/v1/admin/settings/test", a.admin(func(w http.ResponseWriter, r *http.Request) {
		value, err := notify.Destination(r.Context(), a.Store)
		if err != nil {
			fail(w, err)
			return
		}
		if value == "" {
			write(w, 400, map[string]string{"error": "请先配置通知渠道"})
			return
		}
		if a.Notifier == nil {
			write(w, 503, map[string]string{"error": "notification sender unavailable"})
			return
		}
		err = a.Notifier.Send(r.Context(), value, model.Event{Kind: "test", Message: "TZA Probe 通知测试", At: time.Now().Unix()})
		if err != nil {
			write(w, 502, map[string]string{"error": err.Error()})
			return
		}
		w.WriteHeader(204)
	}))
	mux.HandleFunc("POST /api/v1/agent/report", a.agent(func(w http.ResponseWriter, r *http.Request, id string) {
		var v model.Report
		if !decode(w, r, &v) {
			return
		}
		if v.Protocol != model.Protocol {
			write(w, 400, map[string]string{"error": "unsupported protocol"})
			return
		}
		if err := v.Metrics.Validate(); err != nil {
			write(w, 400, map[string]string{"error": err.Error()})
			return
		}
		respond(w, a.Store.Report(r.Context(), id, v.Metrics, time.Now()))
	}))
	mux.HandleFunc("GET /api/v1/agent/jobs", a.agent(func(w http.ResponseWriter, r *http.Request, id string) {
		jobs, err := a.Store.Lease(r.Context(), id, time.Now())
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, jobs)
	}))
	mux.HandleFunc("POST /api/v1/agent/results", a.agent(func(w http.ResponseWriter, r *http.Request, id string) {
		var v model.Result
		if !decode(w, r, &v) {
			return
		}
		if err := v.Validate(); err != nil {
			write(w, 400, map[string]string{"error": err.Error()})
			return
		}
		v.Route = model.RouteLabel(v.Hops)
		if v.Status != "ok" {
			v.Route = "未知"
		}
		respond(w, a.Store.Finish(r.Context(), id, v, time.Now()))
	}))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { write(w, 404, map[string]string{"error": "not found"}) })
	// Model Context Protocol: the same administrator key as the API, sent as a
	// bearer token by the AI client. The tools cover what the panel does, reads
	// and writes both.
	probe := &mcp.Server{Source: a.Store, Notifier: a.Notifier}
	mux.HandleFunc("POST /mcp", a.admin(probe.ServeHTTP))
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		// The modern revision dropped the standalone SSE stream (GET) and
		// session termination (DELETE), so both answer 405.
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "MCP endpoint accepts POST only", http.StatusMethodNotAllowed)
	})
	// Agent distribution: the install script is public and secret-free; the
	// node key travels only as an argument of the command shown to the admin.
	mux.HandleFunc("GET /install.sh", func(w http.ResponseWriter, r *http.Request) {
		if a.Agents == nil {
			http.Error(w, "installation script not available", 404)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(agentdist.Script())
	})
	mux.HandleFunc("GET /dl/{name}", func(w http.ResponseWriter, r *http.Request) {
		arch, sum, ok := agentdist.ParseDownload(r.PathValue("name"))
		if a.Agents == nil || !ok {
			http.NotFound(w, r)
			return
		}
		if sum {
			s, err := a.Agents.Sum(arch)
			if err != nil {
				http.Error(w, "agent binary not available for "+arch, 404)
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			io.WriteString(w, s+"  "+agentdist.Name(arch)+"\n")
			return
		}
		if err := a.Agents.ServeBinary(w, r, arch); errors.Is(err, agentdist.ErrMissing) {
			http.Error(w, "agent binary not available for "+arch+"; see internal/agentdist/bin/README.md", 404)
		} else if err != nil {
			slog.Warn("agent download failed", "arch", arch, "error", err)
		}
	})
	mux.Handle("/", a.Assets)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		mux.ServeHTTP(w, r)
	})
}
func (a *API) isAdmin(r *http.Request) bool {
	if token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "); len(token) == len(a.AdminToken) && subtle.ConstantTimeCompare([]byte(token), []byte(a.AdminToken)) == 1 {
		return true
	}
	c, err := r.Cookie("tza_session")
	if err != nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	expiry, ok := a.sessions[model.Hash(c.Value)]
	return ok && time.Now().Before(expiry)
}
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host == r.Host && (u.Scheme == "https" || u.Scheme == "http")
}
func (a *API) admin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.isAdmin(r) {
			write(w, 401, map[string]string{"error": "请先登录"})
			return
		}
		if r.Method != "GET" && !sameOrigin(r) {
			write(w, 403, map[string]string{"error": "origin rejected"})
			return
		}
		next(w, r)
	}
}
func (a *API) agent(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			write(w, 401, map[string]string{"error": "unauthorized"})
			return
		}
		id, err := a.Store.Authenticate(r.Context(), strings.TrimPrefix(header, "Bearer "))
		if err != nil {
			write(w, 401, map[string]string{"error": "unauthorized"})
			return
		}
		next(w, r, id)
	}
}

// maxSessions bounds the in-memory browser sessions.
const maxSessions = 100

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		write(w, 403, map[string]string{"error": "origin rejected"})
		return
	}
	var v struct {
		Token string `json:"token"`
	}
	if !decode(w, r, &v) {
		return
	}
	now := time.Now()
	// Only wrong keys count toward the limit and the right key always gets in:
	// counting every attempt let anyone hold the administrator out of the panel
	// with 30 junk requests a minute. The key is 256 bits, so the limit is a
	// brake on noise, not what stops guessing.
	if len(v.Token) != len(a.AdminToken) || subtle.ConstantTimeCompare([]byte(v.Token), []byte(a.AdminToken)) != 1 {
		a.mu.Lock()
		if now.Sub(a.loginWindow) > time.Minute {
			a.loginAttempts = 0
			a.loginWindow = now
		}
		a.loginAttempts++
		limited := a.loginAttempts > 30
		a.mu.Unlock()
		if limited {
			write(w, 429, map[string]string{"error": "登录过于频繁，请稍后重试"})
			return
		}
		write(w, 401, map[string]string{"error": "管理密钥错误"})
		return
	}
	token := model.Secret()
	a.mu.Lock()
	for key, expiry := range a.sessions {
		if now.After(expiry) {
			delete(a.sessions, key)
		}
	}
	// At the cap the session closest to expiry gives way, so logging in never
	// fails because older browsers are still signed in.
	for len(a.sessions) >= maxSessions {
		oldest, first := "", time.Time{}
		for key, expiry := range a.sessions {
			if oldest == "" || expiry.Before(first) {
				oldest, first = key, expiry
			}
		}
		delete(a.sessions, oldest)
	}
	a.sessions[model.Hash(token)] = now.Add(12 * time.Hour)
	a.mu.Unlock()
	secure := r.TLS != nil
	if u, err := url.Parse(r.Header.Get("Origin")); err == nil && u.Scheme == "https" {
		secure = true
	}
	http.SetCookie(w, &http.Cookie{Name: "tza_session", Value: token, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode, MaxAge: 43200})
	w.WriteHeader(204)
}
func (a *API) nodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := a.Store.Nodes(r.Context(), time.Now())
	if err != nil {
		fail(w, err)
		return
	}
	admin := a.isAdmin(r)
	out := []model.NodeView{}
	for _, n := range nodes {
		if n.Public || admin {
			if !admin {
				n.Alerts = model.Alerts{}
			}
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].Name < out[j].Name
	})
	write(w, 200, out)
}
func (a *API) visible(r *http.Request) bool {
	public, err := a.Store.IsPublic(r.Context(), r.PathValue("id"))
	return err == nil && (public || a.isAdmin(r))
}

func (a *API) history(w http.ResponseWriter, r *http.Request) {
	if !a.visible(r) {
		write(w, 404, map[string]string{"error": "not found"})
		return
	}
	hours, _ := strconv.Atoi(r.URL.Query().Get("hours"))
	if hours == 0 {
		hours = 24
	}
	history, err := a.Store.History(r.Context(), r.PathValue("id"), hours, time.Now())
	if err != nil {
		fail(w, err)
		return
	}
	write(w, 200, history)
}

// observations lists the newest result per target (see storage.ProbeWindow).
// `slim=1` drops the hop tables: the public card only draws the route label and
// the hop list is ~90% of the payload (40KB per node), while the detail dialog
// asks for them without the flag.
func (a *API) observations(w http.ResponseWriter, r *http.Request) {
	if !a.visible(r) {
		write(w, 404, map[string]string{"error": "not found"})
		return
	}
	slim := r.URL.Query().Get("slim") != ""
	out, err := a.Store.Observations(r.Context(), r.PathValue("id"), time.Now())
	if err != nil {
		fail(w, err)
		return
	}
	for i := range out {
		out[i] = out[i].Redact()
		if slim {
			out[i].Result.Hops = nil
		}
	}
	write(w, 200, out)
}

// unlockCatalog is the fixed service list the unlock panel groups by: id, name
// and category. Public on purpose — the anonymous panel draws the same panel as
// the admin one, and the list is the same for every node.
func (a *API) unlockCatalog(w http.ResponseWriter, r *http.Request) {
	write(w, 200, map[string]any{"categories": model.UnlockCategories, "services": model.UnlockCatalog})
}

// unlocks returns one node's stored verdicts. A node that simply never ran the
// check answers with an empty summary instead of 404: the panel has to tell
// 未检测 apart from 无权查看, and an empty summary says exactly that.
func (a *API) unlocks(w http.ResponseWriter, r *http.Request) {
	if !a.visible(r) {
		write(w, 404, map[string]string{"error": "not found"})
		return
	}
	summary, err := a.Store.Unlocks(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	if summary == nil {
		summary = &model.UnlockSummary{Services: []model.UnlockResult{}, Total: len(model.UnlockCatalog)}
	}
	write(w, 200, summary)
}

// latencyWindow is the configured look-back for the public latency bands
// (model.SettingLatencyWindowMinutes), falling back to the default when unset.
func (a *API) latencyWindow(ctx context.Context) (int, time.Time, error) {
	stored, err := a.Store.Setting(ctx, model.SettingLatencyWindowMinutes)
	if err != nil {
		return 0, time.Time{}, err
	}
	minutes := model.LatencyWindowMinutes(stored)
	return minutes, time.Now().Add(-time.Duration(minutes) * time.Minute), nil
}

// panelDefaults reads the visitor-facing default look: which 明暗 and which 皮肤 a
// visitor sees before they pick anything themselves. Values are normalized, so a
// hand-edited database row cannot hand the browser an unknown skin id.
func (a *API) panelDefaults(ctx context.Context) (string, string, error) {
	theme, err := a.Store.Setting(ctx, model.SettingDefaultTheme)
	if err != nil {
		return "", "", err
	}
	skin, err := a.Store.Setting(ctx, model.SettingDefaultSkin)
	if err != nil {
		return "", "", err
	}
	return model.PanelTheme(theme), model.PanelSkin(skin), nil
}

// latencySamples feeds the per-probe cells on the public card. Only nodes the
// caller may already see are included, so this adds no new visibility.
func (a *API) latencySamples(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = min(max(n, 1), 60)
		}
	}
	_, since, err := a.latencyWindow(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	nodes, err := a.Store.Nodes(r.Context(), time.Now())
	if err != nil {
		fail(w, err)
		return
	}
	admin := a.isAdmin(r)
	ids := []string{}
	for _, n := range nodes {
		if n.Public || admin {
			ids = append(ids, n.ID)
		}
	}
	out, err := a.Store.RecentLatencySamples(r.Context(), ids, limit, since)
	if err != nil {
		fail(w, err)
		return
	}
	write(w, 200, map[string]any{"samples": out})
}
func (a *API) saveNode(w http.ResponseWriter, r *http.Request) {
	var n model.Node
	if !decode(w, r, &n) {
		return
	}
	n.ID = r.PathValue("id")
	if err := n.Validate(); err != nil {
		write(w, 400, map[string]string{"error": err.Error()})
		return
	}
	id, token, err := a.Store.SaveNode(r.Context(), n, r.Method == "POST")
	if err != nil {
		fail(w, err)
		return
	}
	write(w, 200, map[string]string{"id": id, "token": token})
}

// saveNodeOrder 保存后台拖动排序后的顺序。请求体是**当前可见的整份节点 id 列表**
// （拖动只改这个列表的顺序），服务端按 0,1,2… 重写 order 一个字段——由服务端
// 定号，前端不必自己算，也不会因为逐台 PUT 整份配置而把别的字段顺手清掉。
func (a *API) saveNodeOrder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []string `json:"ids"`
	}
	if !decode(w, r, &body) {
		return
	}
	if len(body.IDs) == 0 || len(body.IDs) > 500 {
		write(w, 400, map[string]string{"error": "节点数量须在 1—500 之间"})
		return
	}
	seen := map[string]bool{}
	for _, id := range body.IDs {
		if id == "" || seen[id] {
			write(w, 400, map[string]string{"error": "节点 id 不能为空或重复"})
			return
		}
		seen[id] = true
	}
	respond(w, a.Store.SaveNodeOrder(r.Context(), body.IDs))
}

func (a *API) saveTask(w http.ResponseWriter, r *http.Request) {
	var t model.Task
	if !decode(w, r, &t) {
		return
	}
	t.ID = r.PathValue("id")
	if err := t.Validate(); err != nil {
		write(w, 400, map[string]string{"error": err.Error()})
		return
	}
	_, err := a.Store.SaveTask(r.Context(), t, r.Method == "POST")
	respond(w, err)
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		write(w, 415, map[string]string{"error": "application/json required"})
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		write(w, 400, map[string]string{"error": "invalid JSON"})
		return false
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		write(w, 400, map[string]string{"error": "one JSON value required"})
		return false
	}
	return true
}
func respond(w http.ResponseWriter, err error) {
	if err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(204)
}
func fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, model.ErrInvalid):
		write(w, 400, map[string]string{"error": err.Error()})
	case errors.Is(err, model.ErrNotFound):
		write(w, 404, map[string]string{"error": "not found"})
	case errors.Is(err, model.ErrConflict):
		write(w, 409, map[string]string{"error": "stale job, duplicate result, or report too frequent"})
	default:
		slog.Error("request failed", "error", err)
		write(w, 500, map[string]string{"error": "服务器处理失败"})
	}
}
func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("response write failed")
	}
}

func (a *API) networkHistory(w http.ResponseWriter, r *http.Request) {
	if !a.visible(r) {
		write(w, 404, map[string]string{"error": "not found"})
		return
	}
	hours, _ := strconv.Atoi(r.URL.Query().Get("hours"))
	if hours == 0 {
		hours = 24
	}
	out, err := a.Store.NetworkHistory(r.Context(), r.PathValue("id"), hours, time.Now())
	if err != nil {
		fail(w, err)
		return
	}
	write(w, 200, out)
}
