// Package market is the Probe server's client of TZA-CC, the cloud catalog of
// theme packages, add-ons and plugins. The Server only calls CC when an
// administrator browses the market, installs or updates a package, or binds
// the instance; CC never receives monitoring data. What goes out is the
// Probe version, the instance token once bound, and the id@version of
// packages installed from the market.
//
// The browser never talks to CC: httpapi proxies the catalog and preview
// images through this client, so the panel CSP stays 'self'. A package is
// installed only after its archive matches the size, SHA-256 and Ed25519
// signature CC issued for it (signed with a key this Server trusts) and its
// theme.json names the same id and version; then theme.Store installs it with
// the same validation a local upload goes through. See TZA-CC docs/DESIGN.md.
package market

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/plugin"
	"github.com/leeska/TZA-Probe/internal/theme"
	"github.com/leeska/TZA-Probe/pkg/tzapkg"
)

// DefaultURL is the official TZA-CC; --cc-url / TZA_CC_URL point the market
// at another (test or privately deployed) CC, and "off" in either place turns it off.
const DefaultURL = "https://tza.cc"

// OfficialKeys are the signing keys of the official TZA-CC, in the
// "<key id>:<base64>" form of --cc-key; --cc-key / TZA_CC_KEYS add keys for a
// test or privately deployed CC. A rotation ships the new key here next to the old
// one before CC starts signing with it.
var OfficialKeys = []string{"tzacc-2026:kB6Y9ur5Byl5S4Ycz0JR9mkL8UVyKMi2l1I+0wlP49k="}

// Limits on what CC may send back.
const (
	maxJSON  = 1 << 20 // one API response
	maxMedia = 5 << 20 // one preview image
	maxQuery = 64      // search text, in runes

	apiTimeout      = 15 * time.Second
	downloadTimeout = 2 * time.Minute
	mediaTimeout    = 15 * time.Second
)

var (
	// ErrNotConfigured means the Server was started without a CC address.
	ErrNotConfigured = errors.New("TZA-CC 市场已关闭：Server 启动时用了 --cc-url off（或 TZA_CC_URL=off），去掉即连官方 TZA-CC")
	// ErrNoTrustedKeys means no signing key is trusted, so nothing can be installed.
	ErrNoTrustedKeys = errors.New("没有可信的 TZA-CC 签名公钥，无法验证安装包：启动 Server 时用 --cc-key 或 TZA_CC_KEYS 添加")
	// ErrUnreachable wraps transport failures talking to CC.
	ErrUnreachable = errors.New("连不上 TZA-CC")
	// ErrRejected wraps every reason a downloaded package or image is refused.
	ErrRejected = errors.New("TZA-CC 返回的内容未通过校验")
)

// RemoteError is a non-2xx answer from CC, carrying its {"error": …} text.
type RemoteError struct {
	Status  int
	Message string
}

func (e *RemoteError) Error() string { return "TZA-CC：" + e.Message }

// Config wires a client. URL empty means no market (New returns nil).
type Config struct {
	URL     string
	Keys    map[string]ed25519.PublicKey
	Dir     string // data directory; the instance credentials live in <Dir>/cc-instance
	Probe   string // this Server's version, reported to CC
	Themes  *theme.Store
	Plugins *plugin.Store
	// Transport replaces the HTTP transport (tests); nil uses a copy of the default.
	Transport http.RoundTripper
	// Now replaces the clock (tests); nil uses time.Now.
	Now func() time.Time
}

// Client talks to one CC. A nil *Client is a Server without a market: every
// method answers ErrNotConfigured (Status reports configured=false).
type Client struct {
	base    *url.URL
	keys    map[string]ed25519.PublicKey
	dir     string
	probe   string
	themes  *theme.Store
	plugins *plugin.Store
	api     *http.Client // CC API: no redirects, so the token never follows one
	files   *http.Client // downloads and images: a few redirects, http(s) only
	now     func() time.Time

	mu      sync.Mutex // pending and the credentials file
	pollMu  sync.Mutex // one device-token request at a time
	pending *pending
}

// ParseURL checks a CC base address: http(s), a host, no credentials, query
// or fragment. The trailing slash is dropped.
func ParseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("TZA-CC 地址 %q 无效：须为 http(s)://主机[:端口][/路径]", raw)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u, nil
}

// New builds a client, or returns nil when cfg.URL is empty.
func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.URL) == "" {
		return nil, nil
	}
	base, err := ParseURL(cfg.URL)
	if err != nil {
		return nil, err
	}
	transport := cfg.Transport
	if transport == nil {
		transport = http.DefaultTransport.(*http.Transport).Clone()
	}
	c := &Client{base: base, keys: cfg.Keys, dir: cfg.Dir, probe: ProbeVersion(cfg.Probe), themes: cfg.Themes, plugins: cfg.Plugins, now: cfg.Now}
	if c.now == nil {
		c.now = time.Now
	}
	c.api = &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	c.files = &http.Client{Transport: transport, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("重定向次数过多")
		}
		if !c.fetchable(r.URL) {
			return errors.New("重定向到了不允许的地址")
		}
		return nil
	}}
	return c, nil
}

// URL is the CC base address.
func (c *Client) URL() string {
	if c == nil {
		return ""
	}
	return c.base.String()
}

// fetchable is the rule for download and image URLs (and their redirects):
// https, or http when CC itself is plain http (a test or privately deployed CC).
func (c *Client) fetchable(u *url.URL) bool {
	return u.Host != "" && u.User == nil && (u.Scheme == "https" || (u.Scheme == "http" && c.base.Scheme == "http"))
}

// onOrigin reports whether u is on CC's own origin (scheme + host + port).
func (c *Client) onOrigin(u *url.URL) bool {
	return u.User == nil && strings.EqualFold(u.Scheme, c.base.Scheme) && strings.EqualFold(u.Host, c.base.Host)
}

// mediaURL keeps a preview-image URL only if the proxy will serve it.
func (c *Client) mediaURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || !c.onOrigin(u) || len(raw) > 2048 {
		return ""
	}
	return u.String()
}

// call makes one CC API request and decodes the JSON answer into out. With
// auth the instance token goes along when bound; a 401 to a request that
// carried it means CC no longer knows the binding: the local credentials are
// dropped and the request is retried once without a token (P0 routes accept
// anonymous calls).
func (c *Client) call(ctx context.Context, method string, path []string, query url.Values, body, out any, auth bool) error {
	ctx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()
	u := c.base.JoinPath(append([]string{"api", "v1"}, path...)...)
	if query != nil {
		u.RawQuery = query.Encode()
	}
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	token := ""
	if auth {
		token = c.token()
	}
	for {
		status, data, err := c.send(ctx, method, u.String(), payload, token)
		if err != nil {
			return err
		}
		if status == http.StatusUnauthorized && token != "" {
			c.dropToken(token)
			token = ""
			continue
		}
		if status < 200 || status > 299 {
			return remoteError(status, data)
		}
		if out == nil || status == http.StatusNoContent {
			return nil
		}
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("%w：响应不是预期的 JSON", ErrRejected)
		}
		return nil
	}
}

// send performs one request and reads at most maxJSON bytes of the answer.
func (c *Client) send(ctx context.Context, method, target string, payload []byte, token string) (int, []byte, error) {
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "tza-probe/"+c.probe)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.api.Do(req)
	if err != nil {
		return 0, nil, transportError(ctx, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxJSON+1))
	if err != nil {
		return 0, nil, transportError(ctx, err)
	}
	if len(data) > maxJSON {
		return 0, nil, fmt.Errorf("%w：响应超过 %d MB", ErrRejected, maxJSON>>20)
	}
	return resp.StatusCode, data, nil
}

// transportError hides nothing secret (no token is ever in a URL) but turns
// a deadline into plain words.
func transportError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w：请求超时", ErrUnreachable)
	}
	var uerr *url.Error
	if errors.As(err, &uerr) {
		err = uerr.Err
	}
	return fmt.Errorf("%w：%v", ErrUnreachable, err)
}

func remoteError(status int, body []byte) error {
	var v struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &v)
	msg := clean(v.Error, 200)
	if msg == "" {
		msg = fmt.Sprintf("请求失败（HTTP %d）", status)
	}
	return &RemoteError{Status: status, Message: msg}
}

// clean trims text from CC to n runes and drops control codes: it ends
// up in error messages and the panel.
func clean(s string, n int) string {
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' {
			return -1
		}
		return r
	}, s))
	if utf8.RuneCountInString(s) > n {
		s = string([]rune(s)[:n]) + "…"
	}
	return s
}

// ---- catalog

// Author is a package's developer on CC.
type Author struct {
	Name   string `json:"name"`
	Handle string `json:"handle,omitempty"`
}

// Installed says what this Server has under a market package's id.
type Installed struct {
	Version string `json:"version,omitempty"`
	Source  string `json:"source"` // "market" or "local"
}

// Item is one catalog entry. Installed is added by this Server; whether an
// installed package has an update comes from Updates (CC's compatibility
// check), not from comparing Latest here.
type Item struct {
	ID      string  `json:"id"`
	Type    string  `json:"type"`
	Name    string  `json:"name"`
	Summary string  `json:"summary,omitempty"`
	Author  Author  `json:"author"`
	Latest  string  `json:"latest"`
	Price   float64 `json:"price"` // in cents; 0 is free
	// Owned: free, or the bound TZA-CC account owns this paid package.
	Owned     bool     `json:"owned"`
	Downloads int64    `json:"downloads"`
	UpdatedAt string   `json:"updated_at,omitempty"`
	Swatch    []string `json:"swatch,omitempty"`
	// For is the skin an add-on extends.
	For string `json:"for,omitempty"`
	// Kind and Permissions are set for plugins only: notify | targets, and what
	// the newest compatible release may do (tzapkg Plugin.Permissions).
	Kind        string   `json:"kind,omitempty"`
	Permissions []string `json:"permissions,omitempty"`
	Cover       string   `json:"cover,omitempty"`
	// URL is the package's page on CC, for the administrator to open.
	URL       string     `json:"url,omitempty"`
	Installed *Installed `json:"installed,omitempty"`
}

// Catalog is one page of the market.
type Catalog struct {
	Items []Item `json:"items"`
	Page  int    `json:"page"`
	More  bool   `json:"more"`
}

// Query selects a catalog page.
type Query struct {
	Type string // "", skin, addon or plugin
	// For keeps only the add-ons of this skin (with Type addon).
	For  string
	Q    string
	Page int
}

// Release is one published version of a package.
type Release struct {
	Version     string `json:"version"`
	Changelog   string `json:"changelog,omitempty"`
	Probe       string `json:"probe,omitempty"`
	Size        int64  `json:"size"`
	PublishedAt string `json:"published_at,omitempty"`
	// Compatible: this Server's version satisfies Probe.
	Compatible bool `json:"compatible"`
}

// Package is a package's detail page.
type Package struct {
	Item
	Description string    `json:"description,omitempty"`
	Screenshots []string  `json:"screenshots,omitempty"`
	Versions    []Release `json:"versions"`
}

// Update is an installed market package with a newer compatible release.
type Update struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Latest  string `json:"latest"`
}

var hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// installable are the package types this Server can install.
func installable(t string) bool {
	return t == tzapkg.TypeSkin || t == tzapkg.TypeAddon || t == tzapkg.TypePlugin
}

// Catalog lists one page of the market, filtered by CC for this Probe version.
func (c *Client) Catalog(ctx context.Context, q Query) (Catalog, error) {
	if c == nil {
		return Catalog{}, ErrNotConfigured
	}
	q.Q = strings.TrimSpace(q.Q)
	switch {
	case q.Type != "" && !installable(q.Type):
		return Catalog{}, model.Invalid(`type 须为 "skin"（主题）、"addon"（附加包）或 "plugin"（插件）`)
	case q.For != "" && !tzapkg.ValidID(q.For):
		return Catalog{}, model.Invalid("for 须为主题 id")
	case utf8.RuneCountInString(q.Q) > maxQuery:
		return Catalog{}, model.Invalid(fmt.Sprintf("搜索词最多 %d 个字", maxQuery))
	case q.Page < 0 || q.Page > 1000:
		return Catalog{}, model.Invalid("page 须在 1–1000 之间")
	}
	if q.Page == 0 {
		q.Page = 1
	}
	params := url.Values{"probe": {c.probe}, "page": {strconv.Itoa(q.Page)}}
	if q.Type != "" {
		params.Set("type", q.Type)
	}
	if q.Q != "" {
		params.Set("q", q.Q)
	}
	if q.For != "" {
		params.Set("for", q.For)
	}
	var out Catalog
	if err := c.call(ctx, http.MethodGet, []string{"catalog"}, params, nil, &out, true); err != nil {
		return Catalog{}, err
	}
	installed := c.installed()
	items := make([]Item, 0, len(out.Items))
	for _, it := range out.Items {
		if c.cleanItem(&it, installed) {
			items = append(items, it)
		}
	}
	out.Items = items
	if out.Page <= 0 {
		out.Page = q.Page
	}
	return out, nil
}

// Package reads one package's detail and marks which releases this Server
// can install.
func (c *Client) Package(ctx context.Context, id string) (Package, error) {
	if c == nil {
		return Package{}, ErrNotConfigured
	}
	if !tzapkg.ValidID(id) {
		return Package{}, model.Invalid("包 id 须为 2–32 位小写字母、数字或连字符，以字母开头")
	}
	var out Package
	// CC marks each release compatible for the probe it was told; both its
	// verdict and this Server's own reading of the range must agree.
	var verdicts struct {
		Versions []struct {
			Version    string `json:"version"`
			Compatible *bool  `json:"compatible"`
		} `json:"versions"`
	}
	var raw json.RawMessage
	if err := c.call(ctx, http.MethodGet, []string{"packages", id}, url.Values{"probe": {c.probe}}, nil, &raw, true); err != nil {
		var remote *RemoteError
		if errors.As(err, &remote) && remote.Status == http.StatusNotFound {
			return Package{}, model.NotFound("市场里没有这个包：" + id)
		}
		return Package{}, err
	}
	if json.Unmarshal(raw, &out) != nil || json.Unmarshal(raw, &verdicts) != nil {
		return Package{}, fmt.Errorf("%w：响应不是预期的 JSON", ErrRejected)
	}
	if out.ID != id || !c.cleanItem(&out.Item, c.installed()) {
		return Package{}, fmt.Errorf("%w：返回的包与请求的 %s 不符或不能安装", ErrRejected, id)
	}
	ccSays := map[string]bool{}
	for _, v := range verdicts.Versions {
		if v.Compatible != nil {
			ccSays[v.Version] = *v.Compatible
		}
	}
	out.Description = clean(out.Description, 4000)
	shots := []string{}
	for _, s := range out.Screenshots {
		if s = c.mediaURL(s); s != "" && len(shots) < 12 {
			shots = append(shots, s)
		}
	}
	out.Screenshots = shots
	versions := []Release{}
	for _, v := range out.Versions {
		if !tzapkg.ValidVersion(v.Version) {
			continue
		}
		v.Changelog = clean(v.Changelog, 2000)
		v.Probe = clean(v.Probe, 60)
		v.PublishedAt = clean(v.PublishedAt, 40)
		cc, told := ccSays[v.Version]
		v.Compatible = Compatible(v.Probe, c.probe) && (!told || cc)
		versions = append(versions, v)
	}
	out.Versions = versions
	return out, nil
}

// cleanItem normalizes one entry from CC and marks what is installed; false
// drops it (a malformed id, or a type this Server cannot install yet).
func (c *Client) cleanItem(it *Item, installed map[string]Installed) bool {
	if !tzapkg.ValidID(it.ID) || !installable(it.Type) || theme.IsBuiltin(it.ID) {
		return false // theme.IsBuiltin: an old package of a look this Server now compiles in
	}
	if it.Type == tzapkg.TypePlugin {
		if it.Kind != tzapkg.KindNotify && it.Kind != tzapkg.KindTargets {
			return false // a plugin kind this Server cannot run yet
		}
		perms := []string{}
		for _, p := range it.Permissions {
			if p = clean(p, 200); p != "" && len(perms) < 10 {
				perms = append(perms, p)
			}
		}
		it.Permissions = perms
	} else {
		it.Kind, it.Permissions = "", nil
	}
	it.Name = clean(it.Name, 30)
	if it.Name == "" {
		it.Name = it.ID
	}
	it.Summary = clean(it.Summary, 120)
	it.Author.Name, it.Author.Handle = clean(it.Author.Name, 40), clean(it.Author.Handle, 40)
	it.UpdatedAt = clean(it.UpdatedAt, 40)
	if !tzapkg.ValidVersion(it.Latest) {
		it.Latest = ""
	}
	swatch := []string{}
	for _, s := range it.Swatch {
		if hexColor.MatchString(s) && len(swatch) < 3 {
			swatch = append(swatch, s)
		}
	}
	it.Swatch = swatch
	it.Cover = c.mediaURL(it.Cover)
	it.URL = c.mediaURL(it.URL) // same rule: a link on CC's own origin only
	it.Installed = nil
	if have, ok := installed[it.ID]; ok {
		it.Installed = &have
	}
	return true
}

// installed maps the ids of installed packages to their version and source.
func (c *Client) installed() map[string]Installed {
	out := map[string]Installed{}
	for _, m := range c.themes.Installed() {
		have := Installed{Version: m.Version, Source: "local"}
		if o, ok := c.themes.Origin(m.ID); ok {
			have = Installed{Version: o.Version, Source: tzapkg.SourceMarket}
		}
		out[m.ID] = have
	}
	// Plugins and themes share the id space on CC (one package id, one type),
	// so a plugin never shadows a theme of the same id here.
	for _, p := range c.plugins.List() {
		have := Installed{Version: p.Version, Source: "local"}
		if o, ok := c.plugins.Origin(p.ID); ok {
			have = Installed{Version: o.Version, Source: tzapkg.SourceMarket}
		}
		out[p.ID] = have
	}
	return out
}

// Updates asks CC which packages installed from the market have a newer
// release this Probe can run. Local uploads are never sent. With nothing
// installed from the market CC is not contacted at all.
func (c *Client) Updates(ctx context.Context) ([]Update, error) {
	if c == nil {
		return nil, ErrNotConfigured
	}
	type entry struct {
		ID      string `json:"id"`
		Version string `json:"version"`
	}
	sent := map[string]string{}
	list := []entry{}
	for id, have := range c.installed() {
		if have.Source == tzapkg.SourceMarket {
			sent[id] = have.Version
			list = append(list, entry{id, have.Version})
		}
	}
	if len(list) == 0 {
		return []Update{}, nil
	}
	var out struct {
		Updates []Update `json:"updates"`
	}
	body := map[string]any{"installed": list, "probe": c.probe}
	if err := c.call(ctx, http.MethodPost, []string{"updates"}, nil, body, &out, true); err != nil {
		return nil, err
	}
	updates := []Update{}
	for _, u := range out.Updates {
		have, ok := sent[u.ID]
		if ok && tzapkg.ValidVersion(u.Latest) && Newer(u.Latest, have) {
			updates = append(updates, Update{ID: u.ID, Version: have, Latest: u.Latest})
		}
	}
	return updates, nil
}
