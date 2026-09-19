// Package httpapi adapts the v1 HTTP contract to persistence ports. The browser
// and Agent use this contract; neither imports the storage implementation.
package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/leeska/TZA-Probe/internal/catalog"
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
	SaveNode(context.Context, model.Node, bool) (string, error)
	DeleteNode(context.Context, string) error
	Rotate(context.Context, string) (string, error)
	Authenticate(context.Context, string) (string, error)
	Report(context.Context, string, model.Metrics, time.Time) error
	History(context.Context, string, int, time.Time) ([]model.Sample, error)
	Tasks(context.Context) ([]model.Task, error)
	SaveTask(context.Context, model.Task, bool) error
	DeleteTask(context.Context, string) error
	Lease(context.Context, string, time.Time) ([]model.Job, error)
	Finish(context.Context, string, model.Result, time.Time) error
	Observations(context.Context, string, time.Time) ([]model.Observation, error)
	Setting(context.Context, string) (string, error)
	SetSetting(context.Context, string, string) error
	Events(context.Context, bool, time.Time) ([]model.Event, error)
}
type API struct {
	Store         Repository
	Notifier      notify.Sender
	AdminToken    string
	Assets        http.Handler
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
	mux.HandleFunc("GET /api/v1/admin/nodes", a.admin(a.nodes))
	mux.HandleFunc("POST /api/v1/admin/nodes", a.admin(a.saveNode))
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
		value, err := notify.Destination(r.Context(), a.Store)
		if err != nil {
			fail(w, err)
			return
		}
		c := notify.ReadConfig(value)
		write(w, 200, map[string]any{"webhook_configured": c.Channel == "webhook", "telegram_configured": c.Channel == "telegram", "channel": c.Channel, "chat_id": c.ChatID})
	}))
	mux.HandleFunc("PUT /api/v1/admin/settings", a.admin(func(w http.ResponseWriter, r *http.Request) {
		var v notify.Config
		if !decode(w, r, &v) {
			return
		}
		if v.Channel == "" {
			v.Channel = "webhook"
			if v.Webhook == "" {
				v.Channel = "none"
			}
		}
		if v.Channel == "telegram" && v.BotToken == "" {
			old, err := notify.Destination(r.Context(), a.Store)
			if err != nil {
				fail(w, err)
				return
			}
			v.BotToken = notify.ReadConfig(old).BotToken
		}
		if err := v.Validate(); err != nil {
			write(w, 400, map[string]string{"error": err.Error()})
			return
		}
		// Only persist credentials for the selected destination.
		if v.Channel != "telegram" {
			v.BotToken = ""
			v.ChatID = ""
		}
		if v.Channel != "webhook" {
			v.Webhook = ""
		}
		data, err := json.Marshal(v)
		if err != nil {
			fail(w, err)
			return
		}
		respond(w, a.Store.SetSetting(r.Context(), "notification", string(data)))
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
func (a *API) login(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		write(w, 403, map[string]string{"error": "origin rejected"})
		return
	}
	a.mu.Lock()
	now := time.Now()
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
	var v struct {
		Token string `json:"token"`
	}
	if !decode(w, r, &v) {
		return
	}
	if len(v.Token) != len(a.AdminToken) || subtle.ConstantTimeCompare([]byte(v.Token), []byte(a.AdminToken)) != 1 {
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
	if len(a.sessions) >= 100 {
		a.mu.Unlock()
		write(w, 429, map[string]string{"error": "会话过多"})
		return
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
func (a *API) observations(w http.ResponseWriter, r *http.Request) {
	if !a.visible(r) {
		write(w, 404, map[string]string{"error": "not found"})
		return
	}
	out, err := a.Store.Observations(r.Context(), r.PathValue("id"), time.Now())
	if err != nil {
		fail(w, err)
		return
	}
	for i := range out {
		o := &out[i]
		o.Task.NodeIDs = nil
		for h := range o.Result.Hops {
			hop := &o.Result.Hops[h]
			hop.Address = model.PublicAddress(hop.Address)
			hop.ASN = model.NormalizeASN(hop.ASN)
		}
		if o.Stale || o.Result.Status != "ok" {
			o.Result.Route = "未知"
		}
	}
	write(w, 200, out)
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
	token, err := a.Store.SaveNode(r.Context(), n, r.Method == "POST")
	if err != nil {
		fail(w, err)
		return
	}
	write(w, 200, map[string]string{"token": token})
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
	respond(w, a.Store.SaveTask(r.Context(), t, r.Method == "POST"))
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
