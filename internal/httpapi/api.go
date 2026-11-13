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
	"net"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/leeska/TZA-Probe/internal/account"
	"github.com/leeska/TZA-Probe/internal/agentdist"
	"github.com/leeska/TZA-Probe/internal/brand"
	"github.com/leeska/TZA-Probe/internal/httpjson"
	"github.com/leeska/TZA-Probe/internal/mcp"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/module"
	"github.com/leeska/TZA-Probe/internal/notify"
	"github.com/leeska/TZA-Probe/internal/plugin"
	"github.com/leeska/TZA-Probe/internal/service"
	"github.com/leeska/TZA-Probe/internal/theme"
	"github.com/leeska/TZA-Probe/internal/totp"
)

// Repository is the persistence the HTTP contract needs: the management
// operations (through the service) plus the public visibility check and the
// Agent protocol.
type Repository interface {
	service.Store
	IsPublic(context.Context, string) (bool, error)
	Authenticate(context.Context, string) (string, error)
	Report(context.Context, string, model.Metrics, time.Time) error
	Heartbeat(context.Context, string, model.AgentStatus, time.Time) error
	Lease(context.Context, string, time.Time) ([]model.Job, error)
	Finish(context.Context, string, model.Result, time.Time) error
	Audit(context.Context, model.AuditEntry) error
	AuditLog(ctx context.Context, before int64, limit int, node string) ([]model.AuditEntry, error)
}
type browserSession struct {
	Expires     time.Time
	AccountHash string
}

type API struct {
	// Modules mount their routes behind the API's authentication (see
	// internal/module); cmd/server lists them.
	Modules    []module.Module
	Store      Repository
	Notifier   notify.Sender
	AdminToken string
	AccountDir string // data directory holding the panel account; "" disables password login
	Assets     http.Handler
	Agents     *agentdist.Dist // nil disables /install.sh and /dl/
	Themes     *theme.Store    // uploaded theme packages; nil means built-in skins only
	Plugins    *plugin.Store   // installed declarative plugins; nil means none
	Logo       *brand.Store    // uploaded site logo; nil means the built-in mark only
	mu         sync.Mutex
	sessions   map[string]browserSession
	// totpPending is a secret offered by setup and not yet confirmed.
	totpPending   string
	totpOffered   time.Time
	loginWindow   time.Time
	loginFailures map[string]int       // 当前一分钟内各限流桶的失败次数，见 login_limit.go
	loginNext     map[string]time.Time // 各桶的下一次密码校验预约时间
	loginSlots    chan struct{}        // 密码验证的并发上限，懒初始化，见 verifyLogin
	deviceKey     []byte               // 设备 Cookie 的签名密钥
	// 匿名面板共用的快照，见 panelNodes / allObservations / latencySamples。
	nodesSnap        snapshot[[]model.NodeView]
	observationsSnap snapshot[map[string][]model.Observation]
	publicPolicy     policyCache
	samplesSnap      snapshot[map[string]any]
	svc              *service.Service
	// upgrades 是一键升级 Agent 的队列，见 upgrade.go。
	upgrades upgradeQueue
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
// siteBrand is the first-paint brand /theme-default.js hands the page.
type siteBrand struct {
	Name string `json:"name"`
	Logo string `json:"logo"`
}

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
	a.sessions = map[string]browserSession{}
	a.deviceKey = deviceSigningKey(a.AdminToken)
	a.svc = &service.Service{Store: a.Store, Notifier: a.Notifier, Themes: a.Themes, Plugins: a.Plugins, Logo: a.Logo}
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
	// 访客接口都经 a.public：整个前台需要登录时一律 401；第一个参数是所属区块，
	// 后台关掉该区块时访客拿到 404（见 public.go）。
	mux.HandleFunc("GET /api/v1/nodes", a.public("", a.nodes))
	mux.HandleFunc("GET /api/v1/nodes/live", a.public("", a.liveNodes))
	mux.HandleFunc("GET /api/v1/nodes/{id}/history", a.public("", a.history))
	mux.HandleFunc("GET /api/v1/nodes/{id}/network-history", a.public("network", a.networkHistory))
	mux.HandleFunc("GET /api/v1/nodes/{id}/observations", a.public("", a.observations))
	mux.HandleFunc("GET /api/v1/observations", a.public("", a.allObservations))
	mux.HandleFunc("GET /api/v1/latency-samples", a.public("network", a.latencySamples))
	mux.HandleFunc("GET /api/v1/unlock-catalog", a.public("unlock", a.unlockCatalog))
	a.adminRoutes(mux)
	module.Mount(a.Modules, moduleRoutes{a: a, mux: mux})
	// 前台默认外观必须在样式表之前定下来，否则首屏会先按内置默认闪一下再跳到设置里的皮肤。
	// CSP 是 script-src 'self'，index.html 里写不了内联脚本，所以这里发一份几十字节的
	// 动态脚本，由 index.html 在 theme-boot.js 之前引用。
	mux.HandleFunc("GET /theme-default.js", func(w http.ResponseWriter, r *http.Request) {
		theme, skin := model.DefaultPanelTheme, model.DefaultPanelSkin
		var site siteBrand
		unit := model.DefaultPriceUnit
		if panel, err := a.svc.Panel(r.Context()); err == nil {
			theme, skin = panel.DefaultTheme, panel.DefaultSkin
			site = siteBrand{Name: panel.SiteName, Logo: panel.SiteLogo}
			unit = panel.PriceUnit
		}
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		if r.Method == http.MethodGet {
			fmt.Fprintf(w, "window.__tzaDefault={theme:%q,skin:%q};\n", theme, skin)
			// 皮肤清单（内置 + 已安装的主题包）也在这里一起发：theme-boot.js 要在首屏前认出
			// 访客存下的皮肤，theme-css.js 要在首屏前挂上已安装主题的样式表。
			options, _ := a.svc.SkinOptions(r.Context())
			list, _ := json.Marshal(themeList(a.Themes, options))
			fmt.Fprintf(w, "window.__tzaThemes=%s;\n", list)
			// 站点名称与 logo 也要在首屏前就位：theme-boot.js 据此换掉标签页标题与 favicon，
			// app.js 拼品牌区时直接用，不先闪一下内置的 TZA Probe。空值即内置品牌。
			brandJS, _ := json.Marshal(site)
			fmt.Fprintf(w, "window.__tzaBrand=%s;\n", brandJS)
			// 计费汇总单位：公开面板顶部「每月成本 / 剩余价值汇总」用它统一折算（web/format.js
			// 的固定参考汇率表按币种查）。跟着首屏脚本走，面板设置改完下一次打开即生效。
			fmt.Fprintf(w, "window.__tzaPriceUnit=%q;\n", unit)
			// 前台区块开关与「需要登录」：app.js 据此不画关掉的区块、未登录时显示登录提示。
			// 数据本身由 a.public 在服务端过滤，这里只决定版面。
			policy := a.policy(r)
			publicJS, _ := json.Marshal(map[string]any{"hidden": policy.hidden, "login": policy.login})
			fmt.Fprintf(w, "window.__tzaPublic=%s;\n", publicJS)
		}
	})
	// 后台上传的站点 logo（没有时 404）；类型白名单、SVG 检查与它自己的 CSP 见 brand.Store。
	mux.Handle("GET "+brand.Path, a.Logo)
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
		now := time.Now()
		if err := a.Store.Report(r.Context(), id, v.Metrics, now); err != nil {
			fail(w, err)
			return
		}
		// 平常照旧回 204（旧 Agent 只认这个）；排了升级的节点才回一份带指令的正文。
		if up := a.upgradeReply(id, v.Metrics, now); up != nil {
			write(w, 200, model.ReportReply{Upgrade: up})
			return
		}
		w.WriteHeader(204)
	}))
	mux.HandleFunc("POST /api/v1/agent/heartbeat", a.agent(func(w http.ResponseWriter, r *http.Request, id string) {
		var body model.Heartbeat
		if !decode(w, r, &body) {
			return
		}
		if body.Protocol != model.Protocol {
			write(w, 400, map[string]string{"error": "unsupported protocol"})
			return
		}
		if err := body.Status.Validate(); err != nil {
			write(w, 400, map[string]string{"error": err.Error()})
			return
		}
		now := time.Now()
		if err := a.Store.Heartbeat(r.Context(), id, body.Status, now); err != nil {
			fail(w, err)
			return
		}
		meta := model.NodeView{AgentStatus: &body.Status}.AgentMetrics()
		if up := a.upgradeReply(id, *meta, now); up != nil {
			write(w, 200, model.ReportReply{Upgrade: up})
			return
		}
		w.WriteHeader(204)
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
	probe := &mcp.Server{Source: a.Store, Notifier: a.Notifier, Themes: a.Themes, Plugins: a.Plugins, Logo: a.Logo}
	mux.HandleFunc("POST /mcp", a.mcpAuth(probe.ServeHTTP))
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
	// 运维者的升级公钥：安装脚本把它钉在节点上，此后 Agent 只接受它签过名的升级。
	// 公钥本身不是秘密；没有配置时 404，节点照常安装，只是不能一键升级。
	mux.HandleFunc("GET /dl/"+agentdist.KeyFile, func(w http.ResponseWriter, r *http.Request) {
		if a.Agents == nil {
			http.NotFound(w, r)
			return
		}
		key, err := a.Agents.UpgradeKey()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(key)
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
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data: https://tile.openstreetmap.org; frame-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
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
	key := model.Hash(c.Value)
	a.mu.Lock()
	session, ok := a.sessions[key]
	a.mu.Unlock()
	if !ok || !time.Now().Before(session.Expires) {
		return false
	}
	if a.AccountDir == "" {
		return session.AccountHash == ""
	}
	acct, err := account.Load(a.AccountDir)
	if err == nil && session.AccountHash != "" && subtle.ConstantTimeCompare([]byte(session.AccountHash), []byte(acct.Hash)) == 1 {
		return true
	}
	a.mu.Lock()
	delete(a.sessions, key)
	a.mu.Unlock()
	return false
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
		if r.Method == "GET" || r.Method == "HEAD" {
			next(w, r)
			return
		}
		// Every write is audited: the route pattern and the outcome, never the
		// body. A handler that records its own, more detailed entry (through
		// moduleRoutes.Audit) suppresses this one.
		state := &auditState{}
		recorder := &statusRecorder{ResponseWriter: w, status: 200}
		next(recorder, r.WithContext(context.WithValue(r.Context(), auditKey{}, state)))
		if !state.recorded {
			a.audit(r, model.AuditEntry{Action: r.Pattern, NodeID: r.PathValue("id"), Status: recorder.status})
		}
	}
}

type auditKey struct{}

type auditState struct{ recorded bool }

type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.status, s.wrote = code, true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	s.wrote = true
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// audit records e with the actor and source taken from r. A failure to write
// the log is logged, not returned: the action itself already happened.
func (a *API) audit(r *http.Request, e model.AuditEntry) {
	if state, ok := r.Context().Value(auditKey{}).(*auditState); ok {
		state.recorded = true
	}
	if e.Actor == "" {
		e.Actor = a.actor(r)
	}
	e.Source = requestSource(r)
	if err := a.Store.Audit(context.WithoutCancel(r.Context()), e); err != nil {
		slog.Error("audit log write failed", "action", e.Action, "error", err)
	}
}

// actor names who is signed in: the account's username for a browser
// session, "api-key" for the admin token.
func (a *API) actor(r *http.Request) string {
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		return "api-key"
	}
	if a.AccountDir != "" {
		if acct, err := account.Load(a.AccountDir); err == nil {
			return acct.Username
		}
	}
	return "admin"
}

// requestSource is the peer address, followed by the forwarded-for chain a
// reverse proxy added (recorded as given: it is evidence, not trusted).
func requestSource(r *http.Request) string {
	source := r.RemoteAddr
	if host, _, err := net.SplitHostPort(source); err == nil {
		source = host
	}
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		if len(forwarded) > 200 {
			forwarded = forwarded[:200]
		}
		source += " (" + forwarded + ")"
	}
	return source
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
		Code     string `json:"code"`
	}
	if !decode(w, r, &v) {
		return
	}
	now := time.Now()
	if retry, allowed := a.reserveLogin(r, now); !allowed {
		seconds := max(1, int((retry+time.Second-1)/time.Second))
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		write(w, 429, map[string]string{"error": "登录过于频繁，请稍后重试"})
		return
	}
	hash, busy := a.verifyLogin(v.Username, v.Password)
	if busy {
		write(w, 429, map[string]string{"error": "登录过于频繁，请稍后重试"})
		return
	}
	if hash == "" {
		if a.loginLimited(r, now, true) {
			write(w, 429, map[string]string{"error": "登录过于频繁，请稍后重试"})
			return
		}
		a.audit(r, model.AuditEntry{Actor: auditName(v.Username), Action: "login.failed", Status: 401})
		write(w, 401, map[string]string{"error": "用户名或密码错误"})
		return
	}
	// Second factor, after the password: the code prompt only appears to
	// someone who already knows the password.
	if _, enabled, err := totp.Load(a.AccountDir); err != nil {
		slog.Error("two-factor configuration unreadable", "error", err)
		write(w, 500, map[string]string{"error": "两步验证配置无法读取，请在服务器执行 tza-server reset-password --disable-totp"})
		return
	} else if enabled {
		if strings.TrimSpace(v.Code) == "" {
			write(w, 401, map[string]any{"error": "请输入两步验证码", "totp": true})
			return
		}
		if ok, err := totp.Use(a.AccountDir, v.Code, now); err != nil || !ok {
			a.loginLimited(r, now, true)
			a.audit(r, model.AuditEntry{Actor: auditName(v.Username), Action: "login.failed", Status: 401, Detail: "两步验证码错误"})
			write(w, 401, map[string]any{"error": "两步验证码错误或已用过", "totp": true})
			return
		}
	}
	a.audit(r, model.AuditEntry{Actor: auditName(v.Username), Action: "login", Status: 204})
	a.startSession(w, r, time.Now(), hash)
}

func (a *API) checkAccount(username, password string) string {
	if a.AccountDir == "" || username == "" || password == "" {
		return ""
	}
	acct, err := account.Load(a.AccountDir)
	if err != nil {
		if !errors.Is(err, account.ErrNotFound) {
			slog.Warn("administrator account unreadable", "error", err)
		}
		return ""
	}
	if !acct.Check(username, password) {
		return ""
	}
	return acct.Hash
}

// startSession issues a browser session cookie.
func (a *API) startSession(w http.ResponseWriter, r *http.Request, now time.Time, accountHash string) {
	token := model.Secret()
	a.mu.Lock()
	for key, session := range a.sessions {
		if now.After(session.Expires) {
			delete(a.sessions, key)
		}
	}
	// At the cap the session closest to expiry gives way, so logging in never
	// fails because older browsers are still signed in.
	for len(a.sessions) >= maxSessions {
		oldest, first := "", time.Time{}
		for key, session := range a.sessions {
			if oldest == "" || session.Expires.Before(first) {
				oldest, first = key, session.Expires
			}
		}
		delete(a.sessions, oldest)
	}
	a.sessions[model.Hash(token)] = browserSession{Expires: now.Add(12 * time.Hour), AccountHash: accountHash}
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "tza_session", Value: token, Path: "/", HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteStrictMode, MaxAge: 43200})
	a.setDeviceCookie(w, r, now, accountHash)
	w.WriteHeader(204)
}

// visibleNodes is the node list the caller may see, in panel order: every node
// for the administrator, the public ones (redacted) for everyone else. A public
// node that has never reported (等待接入) stays hidden from visitors until its
// Agent connects — same rule as Store.IsPublic for the per-node endpoints.
func (a *API) visibleNodes(r *http.Request) ([]model.NodeView, error) {
	admin := a.isAdmin(r)
	nodes, err := a.panelNodes(r, admin)
	if err != nil {
		return nil, err
	}
	policy := a.policy(r)
	out := []model.NodeView{}
	for _, n := range nodes {
		if admin || (n.Public && n.LastSeen > 0) {
			if !admin {
				n = policy.strip(n.Redact())
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
	if !a.isAdmin(r) {
		policy := a.policy(r)
		out = slices.DeleteFunc(out, func(o model.Observation) bool { return !policy.keepObservation(o) })
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
			if !admin && !(n.Public && n.LastSeen > 0) { // 与 visibleNodes 同一条：等待接入的不给访客
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
		// The snapshot is shared: filter into a new map, never in place.
		if policy := a.policy(r); err == nil && len(policy.hidden) > 0 {
			filtered := make(map[string][]model.Observation, len(out))
			for id, list := range out {
				keep := []model.Observation{}
				for _, o := range list {
					if policy.keepObservation(o) {
						keep = append(keep, o)
					}
				}
				filtered[id] = keep
			}
			out = filtered
		}
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
			if admin || (n.Public && n.LastSeen > 0) { // 与 visibleNodes 同一条：等待接入的不给访客
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

func decode(w http.ResponseWriter, r *http.Request, v any) bool { return httpjson.Decode(w, r, v) }
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
	var challenge *model.AgentSessionChallenge
	if errors.As(err, &challenge) {
		write(w, http.StatusConflict, challenge)
		return
	}
	var sendErr service.SendError
	switch {
	case errors.Is(err, service.ErrNoSender):
		write(w, 503, map[string]string{"error": err.Error()})
	case errors.As(err, &sendErr):
		write(w, 502, map[string]string{"error": err.Error()})
	default:
		httpjson.Fail(w, err)
	}
}

func write(w http.ResponseWriter, status int, v any) { httpjson.Write(w, status, v) }

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

// moduleRoutes mounts a module's handlers with the API's own middleware.
type moduleRoutes struct {
	a   *API
	mux *http.ServeMux
}

func (m moduleRoutes) Public(pattern string, h http.HandlerFunc) { m.mux.HandleFunc(pattern, h) }
func (m moduleRoutes) Admin(pattern string, h http.HandlerFunc) {
	m.mux.HandleFunc(pattern, m.a.admin(h))
}
func (m moduleRoutes) Agent(pattern string, h func(http.ResponseWriter, *http.Request, string)) {
	m.mux.HandleFunc(pattern, m.a.agent(h))
}

// Audit lets a module record its own entry for r (the command it ran, the
// terminal it opened) in place of the generic one.
func (m moduleRoutes) Audit(r *http.Request, e model.AuditEntry) { m.a.audit(r, e) }

// auditName keeps a typed username short and printable for the log.
func auditName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	if len([]rune(name)) > 32 {
		name = string([]rune(name)[:32]) + "…"
	}
	return name
}
