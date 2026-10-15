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

	"github.com/leeska/TZA-Probe/internal/account"
	"github.com/leeska/TZA-Probe/internal/agentdist"
	"github.com/leeska/TZA-Probe/internal/market"
	"github.com/leeska/TZA-Probe/internal/mcp"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/notify"
	"github.com/leeska/TZA-Probe/internal/plugin"
	"github.com/leeska/TZA-Probe/internal/service"
	"github.com/leeska/TZA-Probe/internal/theme"
)

// Repository is the persistence the HTTP contract needs: the management
// operations (through the service) plus the public visibility check and the
// Agent protocol.
type Repository interface {
	service.Store
	IsPublic(context.Context, string) (bool, error)
	Authenticate(context.Context, string) (string, error)
	Report(context.Context, string, model.Metrics, time.Time) error
	Lease(context.Context, string, time.Time) ([]model.Job, error)
	Finish(context.Context, string, model.Result, time.Time) error
}
type API struct {
	Store         Repository
	Notifier      notify.Sender
	AdminToken    string
	AccountDir    string // data directory holding the panel account; "" disables password login
	Assets        http.Handler
	Agents        *agentdist.Dist // nil disables /install.sh and /dl/
	Themes        *theme.Store    // uploaded theme packages; nil means built-in skins only
	Market        *market.Client  // TZA-CC client; nil means no market (routes answer 503)
	Plugins       *plugin.Store   // installed declarative plugins; nil means none
	mu            sync.Mutex
	sessions      map[string]time.Time
	loginWindow   time.Time
	loginAttempts int
	// 匿名面板共用的快照，见 panelNodes / allObservations / latencySamples。
	nodesSnap        snapshot[[]model.NodeView]
	observationsSnap snapshot[map[string][]model.Observation]
	samplesSnap      snapshot[map[string]any]
	svc              *service.Service
}

// snapshot 是匿名访客共用的一份计算结果：有效期内直接复用，过期后同时到达的请求排队等同一次
// 计算，再多访客每个周期也只算一次。存进来的值之后只读，调用方不得修改。
type snapshot[T any] struct {
	mu    sync.Mutex
	at    time.Time
	value T
	ok    bool
}

func (c *snapshot[T]) get(ttl time.Duration, compute func() (T, error)) (T, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ok && time.Since(c.at) < ttl {
		return c.value, nil
	}
	value, err := compute()
	if err != nil {
		var zero T
		return zero, err
	}
	c.at, c.value, c.ok = time.Now(), value, true
	return value, nil
}

// nodesTTL 是匿名访客共用节点快照的有效期。Agent 本来就约 1 秒报一次，面板也是 1 秒一拍，
// 再新也没有新数据可看。
const nodesTTL = time.Second

// probesTTL 是匿名访客共用探测快照（逐目标观测、延迟样本）的有效期。面板 10 秒才取一轮，
// 探测本身 30 秒以上一次，5 秒的延迟看不出来；而这两条查询要逐行 json_extract 时间窗内的
// 结果，以前每个访客每 10 秒各算一遍。
const probesTTL = 5 * time.Second

// panelNodes 返回节点视图。首页每秒拉一次 /nodes，而一次 Store.Nodes 要读全部任务、逐台查
// 流量与降速窗口（13 台约 30 条查询）：以前每个开着面板的访客都各算一遍，访客越多 1 核机越吃力。
// 匿名请求改为共用一份 1 秒内的快照，同时到达的请求排队等同一次计算，再多访客每秒也只算一次。
// 管理员不走快照：后台改完节点马上要看到结果。
func (a *API) panelNodes(r *http.Request, admin bool) ([]model.NodeView, error) {
	if admin {
		return a.Store.Nodes(r.Context(), time.Now())
	}
	return a.nodesSnap.get(nodesTTL, func() ([]model.NodeView, error) {
		// 排在后面的请求等的是这次计算，发起者断开不该让它们一起失败。
		return a.Store.Nodes(context.WithoutCancel(r.Context()), time.Now())
	})
}

func (a *API) Handler() http.Handler {
	a.sessions = map[string]time.Time{}
	a.svc = &service.Service{Store: a.Store, Notifier: a.Notifier, Themes: a.Themes, Plugins: a.Plugins}
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
	// version 是服务端（也是它内置的 Agent 安装包）的版本：后台据此标出该升级的 Agent。
	mux.HandleFunc("GET /api/v1/session", a.admin(func(w http.ResponseWriter, r *http.Request) {
		write(w, 200, map[string]any{"authenticated": true, "version": model.Version})
	}))
	mux.HandleFunc("GET /api/v1/nodes", a.nodes)
	mux.HandleFunc("GET /api/v1/nodes/live", a.liveNodes)
	mux.HandleFunc("GET /api/v1/nodes/{id}/history", a.history)
	mux.HandleFunc("GET /api/v1/nodes/{id}/network-history", a.networkHistory)
	mux.HandleFunc("GET /api/v1/nodes/{id}/observations", a.observations)
	mux.HandleFunc("GET /api/v1/observations", a.allObservations)
	mux.HandleFunc("GET /api/v1/latency-samples", a.latencySamples)
	mux.HandleFunc("GET /api/v1/unlock-catalog", a.unlockCatalog)
	a.adminRoutes(mux)
	// 前台默认外观必须在样式表之前定下来，否则首屏会先按内置默认闪一下再跳到设置里的皮肤。
	// CSP 是 script-src 'self'，index.html 里写不了内联脚本，所以这里发一份几十字节的
	// 动态脚本，由 index.html 在 theme-boot.js 之前引用。
	mux.HandleFunc("GET /theme-default.js", func(w http.ResponseWriter, r *http.Request) {
		theme, skin := model.DefaultPanelTheme, model.DefaultPanelSkin
		if panel, err := a.svc.Panel(r.Context()); err == nil {
			theme, skin = panel.DefaultTheme, panel.DefaultSkin
		}
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		if r.Method == http.MethodGet {
			fmt.Fprintf(w, "window.__tzaDefault={theme:%q,skin:%q};\n", theme, skin)
			// 皮肤清单（内置 + 已安装的主题包）也在这里一起发：theme-boot.js 要在首屏前认出
			// 访客存下的皮肤，theme-css.js 要在首屏前挂上已安装主题的样式表。
			list, _ := json.Marshal(themeList(a.Themes))
			fmt.Fprintf(w, "window.__tzaThemes=%s;\n", list)
		}
	})
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
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		write(w, 404, map[string]string{"error": "接口不存在"})
	})
	// MCP uses an independent, revocable API key managed in the panel.
	a.mcpKeyRoutes(mux)
	probe := &mcp.Server{Source: a.Store, Notifier: a.Notifier, Themes: a.Themes, Market: a.Market, Plugins: a.Plugins}
	mux.HandleFunc("POST /mcp", a.mcpAuth(func(w http.ResponseWriter, r *http.Request) {
		// install_market_package downloads from TZA-CC and can outlast the 30-second WriteTimeout.
		http.NewResponseController(w).SetWriteDeadline(time.Now().Add(installDeadline))
		probe.ServeHTTP(w, r)
	}))
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
	// 已安装主题包的静态文件（样式表、图片、字体）；类型白名单与 nosniff 见 theme.Store.ServeHTTP。
	mux.Handle("GET /themes/{id}/{file...}", a.Themes)
	mux.Handle("/", a.Assets)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
			gzipAPI(w, r, mux)
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func (a *API) isAdmin(r *http.Request) bool {
	// 与 agent() 一样只认标准的 Bearer 形式：TrimPrefix 在没有前缀时原样返回，会让裸放密钥的
	// Authorization 头也通过。
	if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && a.AdminToken != "" && len(token) == len(a.AdminToken) && subtle.ConstantTimeCompare([]byte(token), []byte(a.AdminToken)) == 1 {
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
			write(w, 403, map[string]string{"error": "请求来源不受信任"})
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
		write(w, 403, map[string]string{"error": "请求来源不受信任"})
		return
	}
	var v struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &v) {
		return
	}
	now := time.Now()
	if a.loginLimited(now, false) {
		write(w, 429, map[string]string{"error": "登录过于频繁，请稍后重试"})
		return
	}
	if !a.checkAccount(v.Username, v.Password) {
		if a.loginLimited(now, true) {
			write(w, 429, map[string]string{"error": "登录过于频繁，请稍后重试"})
			return
		}
		write(w, 401, map[string]string{"error": "用户名或密码错误"})
		return
	}
	a.startSession(w, r, now)
}

// loginLimited reports whether wrong logins in the current minute passed the
// limit, counting one more first when failed is set.
func (a *API) loginLimited(now time.Time, failed bool) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if now.Sub(a.loginWindow) > time.Minute {
		a.loginAttempts = 0
		a.loginWindow = now
	}
	if failed {
		a.loginAttempts++
	}
	return a.loginAttempts > 30
}

func (a *API) checkAccount(username, password string) bool {
	if a.AccountDir == "" || username == "" || password == "" {
		return false
	}
	acct, err := account.Load(a.AccountDir)
	if err != nil {
		if !errors.Is(err, account.ErrNotFound) {
			slog.Warn("administrator account unreadable", "error", err)
		}
		return false
	}
	return acct.Check(username, password)
}

// startSession issues a browser session cookie.
func (a *API) startSession(w http.ResponseWriter, r *http.Request, now time.Time) {
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

// visibleNodes is the node list the caller may see, in panel order: every node
// for the administrator, the public ones (redacted) for everyone else.
func (a *API) visibleNodes(r *http.Request) ([]model.NodeView, error) {
	admin := a.isAdmin(r)
	nodes, err := a.panelNodes(r, admin)
	if err != nil {
		return nil, err
	}
	out := []model.NodeView{}
	for _, n := range nodes {
		if n.Public || admin {
			if !admin {
				n = n.Redact()
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
	return out, nil
}

func (a *API) nodes(w http.ResponseWriter, r *http.Request) {
	out, err := a.visibleNodes(r)
	if err != nil {
		fail(w, err)
		return
	}
	write(w, 200, out)
}

// liveNodes is what the public panel polls every second: only the state that
// moves (online, metrics, cycle usage, throttle window) of the same nodes
// /nodes returns. Configuration, the probe plan and unlock verdicts change on
// an edit, not a second, and come with the 10-second /nodes refresh.
func (a *API) liveNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := a.visibleNodes(r)
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]model.LiveView, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.Live())
	}
	write(w, 200, out)
}

func (a *API) visible(r *http.Request) bool {
	public, err := a.Store.IsPublic(r.Context(), r.PathValue("id"))
	return err == nil && (public || a.isAdmin(r))
}

func (a *API) history(w http.ResponseWriter, r *http.Request) {
	if !a.visible(r) {
		write(w, 404, map[string]string{"error": "对象不存在"})
		return
	}
	hours, _ := strconv.Atoi(r.URL.Query().Get("hours"))
	history, _, err := a.svc.History(r.Context(), r.PathValue("id"), hours)
	if err != nil {
		fail(w, err)
		return
	}
	if r.URL.Query().Get("slim") != "" {
		// 详情图表只画 CPU / 内存 / 上下行速率：系统、版本、IP 族这些每条样本都一样的字段
		// 占了样本体积的大半，slim 时不发。外形仍是 {at, metrics:{…}}，前端不用改读法。
		type chartMetrics struct {
			CPU         float64 `json:"cpu"`
			MemoryUsed  uint64  `json:"memory_used"`
			MemoryTotal uint64  `json:"memory_total"`
			RateIn      float64 `json:"rate_in"`
			RateOut     float64 `json:"rate_out"`
		}
		type chartSample struct {
			At      int64        `json:"at"`
			Metrics chartMetrics `json:"metrics"`
		}
		out := make([]chartSample, len(history))
		for i, x := range history {
			m := x.Metrics
			out[i] = chartSample{x.At, chartMetrics{m.CPU, m.MemoryUsed, m.MemoryTotal, m.RateIn, m.RateOut}}
		}
		write(w, 200, out)
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
		write(w, 404, map[string]string{"error": "对象不存在"})
		return
	}
	slim := r.URL.Query().Get("slim") != ""
	out, err := a.svc.Observations(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	for i := range out {
		if slim {
			out[i].Result.Hops = nil
		}
	}
	write(w, 200, out)
}

// allObservations is the slim observation list of every node the caller may see,
// keyed by node id. The public panel used to ask once per node; with a dozen
// nodes that was a dozen round trips for rows the server reads in well under 1ms.
func (a *API) allObservations(w http.ResponseWriter, r *http.Request) {
	admin := a.isAdmin(r)
	compute := func(ctx context.Context) (map[string][]model.Observation, error) {
		nodes, err := a.panelNodes(r, admin)
		if err != nil {
			return nil, err
		}
		out := map[string][]model.Observation{}
		for _, n := range nodes {
			if !n.Public && !admin {
				continue
			}
			list, err := a.svc.Observations(ctx, n.ID)
			if err != nil {
				return nil, err
			}
			for i := range list {
				list[i].Result.Hops = nil
			}
			out[n.ID] = list
		}
		return out, nil
	}
	var out map[string][]model.Observation
	var err error
	if admin {
		out, err = compute(r.Context())
	} else {
		out, err = a.observationsSnap.get(probesTTL, func() (map[string][]model.Observation, error) {
			return compute(context.WithoutCancel(r.Context()))
		})
	}
	if err != nil {
		fail(w, err)
		return
	}
	write(w, 200, out)
}

// unlockCatalog is the fixed service list the unlock panel groups by: id, name
// and category. Public on purpose — the anonymous panel draws the same panel as
// the admin one, and the list is the same for every node.
func (a *API) unlockCatalog(w http.ResponseWriter, r *http.Request) {
	write(w, 200, map[string]any{"categories": model.UnlockCategories, "services": model.UnlockCatalog})
}

// latencySamples feeds the per-probe cells on the public card. Only nodes the
// caller may already see are included, so this adds no new visibility.
func (a *API) latencySamples(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	limit = service.Window(limit, service.DefaultLatencySamples, service.MaxLatencySamples)
	admin := a.isAdmin(r)
	compute := func(ctx context.Context) (map[string]any, error) {
		nodes, err := a.panelNodes(r, admin)
		if err != nil {
			return nil, err
		}
		ids := []string{}
		for _, n := range nodes {
			if n.Public || admin {
				ids = append(ids, n.ID)
			}
		}
		out, _, _, err := a.svc.LatencySamples(ctx, ids, limit)
		if err != nil {
			return nil, err
		}
		return map[string]any{"samples": out}, nil
	}
	var out map[string]any
	var err error
	// 快照只存面板实际用的那一档（limit=20）；别的 limit 很少见，直接算。
	if admin || limit != service.DefaultLatencySamples {
		out, err = compute(r.Context())
	} else {
		out, err = a.samplesSnap.get(probesTTL, func() (map[string]any, error) {
			return compute(context.WithoutCancel(r.Context()))
		})
	}
	if err != nil {
		fail(w, err)
		return
	}
	write(w, 200, out)
}

func jsonRequest(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		write(w, 415, map[string]string{"error": "请求须为 application/json"})
		return false
	}
	return true
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if !jsonRequest(w, r) {
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		write(w, 400, map[string]string{"error": "请求体不是有效的 JSON"})
		return false
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		write(w, 400, map[string]string{"error": "请求体只能包含一个 JSON 值"})
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

// fail answers with the error's kind as the status and its message as the
// body. The bare sentinels get a generic Chinese line; a message built with
// model.Invalid / NotFound / Conflict is shown as is.
func fail(w http.ResponseWriter, err error) {
	var sendErr service.SendError
	switch {
	case errors.Is(err, model.ErrInvalid):
		write(w, 400, map[string]string{"error": message(err, model.ErrInvalid, "请求参数无效")})
	case errors.Is(err, model.ErrNotFound):
		write(w, 404, map[string]string{"error": message(err, model.ErrNotFound, "对象不存在")})
	case errors.Is(err, model.ErrConflict):
		write(w, 409, map[string]string{"error": message(err, model.ErrConflict, "操作冲突，请刷新后重试")})
	case errors.Is(err, service.ErrNoSender):
		write(w, 503, map[string]string{"error": err.Error()})
	case errors.As(err, &sendErr):
		write(w, 502, map[string]string{"error": err.Error()})
	default:
		slog.Error("request failed", "error", err)
		write(w, 500, map[string]string{"error": "服务器处理失败"})
	}
}

func message(err, sentinel error, fallback string) string {
	if err == sentinel {
		return fallback
	}
	return err.Error()
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
		write(w, 404, map[string]string{"error": "对象不存在"})
		return
	}
	hours, _ := strconv.Atoi(r.URL.Query().Get("hours"))
	out, _, err := a.svc.NetworkHistory(r.Context(), r.PathValue("id"), hours)
	if err != nil {
		fail(w, err)
		return
	}
	write(w, 200, out)
}
