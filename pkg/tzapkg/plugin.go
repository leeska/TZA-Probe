package tzapkg

import (
	"archive/zip"
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// TypePlugin is a declarative plugin: plugin.json and nothing else. It holds
// data and templates the host interprets, never code. See docs/PLUGINS.md.
const TypePlugin = "plugin"

// Plugin kinds.
const (
	KindNotify  = "notify"  // a notification channel: an HTTP request template
	KindTargets = "targets" // extra probe targets for the task catalog
)

// Plugin is a parsed plugin.json. Source is set by the host, like Manifest's.
type Plugin struct {
	ID      string      `json:"id"`
	Type    string      `json:"type"`
	Kind    string      `json:"kind"`
	Name    string      `json:"name"`
	Hint    string      `json:"hint,omitempty"`
	Version string      `json:"version,omitempty"`
	Author  string      `json:"author,omitempty"`
	Notify  *NotifySpec `json:"notify,omitempty"`
	Targets []Target    `json:"targets,omitempty"`
	Source  string      `json:"source,omitempty"`

	compiled *compiledNotify
}

// NotifySpec describes one notification channel declaratively.
type NotifySpec struct {
	// Hosts the request may go to: exact names, "*.example.com" for
	// subdomains, or ["*"] for whatever address the administrator types into
	// a url setting (self-hosted services).
	Hosts    []string    `json:"hosts"`
	Settings []Setting   `json:"settings,omitempty"`
	Computed []Computed  `json:"computed,omitempty"`
	Request  RequestSpec `json:"request"`
	Success  SuccessSpec `json:"success,omitempty"`
}

// Setting is one field of the channel form in the Probe panel.
type Setting struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Type        string   `json:"type"` // text | secret | url | select
	Required    bool     `json:"required,omitempty"`
	Secret      bool     `json:"secret,omitempty"` // never shown back; implied by type secret
	Placeholder string   `json:"placeholder,omitempty"`
	Help        string   `json:"help,omitempty"`
	Default     string   `json:"default,omitempty"`
	Pattern     string   `json:"pattern,omitempty"`
	Options     []Option `json:"options,omitempty"`

	pattern *regexp.Regexp
}

// Option is one choice of a select setting.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Computed is a value derived before the request is built, evaluated in
// order. When/Unless name a variable (e.g. "settings.secret") that must be
// non-empty / empty for the entry to apply; a later entry of the same name
// overrides an earlier one.
type Computed struct {
	Name       string    `json:"name"`
	Value      string    `json:"value,omitempty"`
	HMACSHA256 *HMACSpec `json:"hmac_sha256,omitempty"`
	SHA256     string    `json:"sha256,omitempty"`
	Encode     string    `json:"encode,omitempty"` // base64 (default) | base64url | hex, for hmac_sha256 / sha256
	When       string    `json:"when,omitempty"`
	Unless     string    `json:"unless,omitempty"`
}

// HMACSpec is an HMAC-SHA256 over Message with Key, both templates.
type HMACSpec struct {
	Key     string `json:"key"`
	Message string `json:"message"`
}

// RequestSpec is the HTTP request. Body is any JSON for body_type json (its
// string leaves are templates), an object of templates for form, a template
// string for text. A key ending in "?" (body object or header) is left out
// when it renders empty.
type RequestSpec struct {
	Method   string            `json:"method"`
	URL      string            `json:"url"`
	Headers  map[string]string `json:"headers,omitempty"`
	BodyType string            `json:"body_type,omitempty"` // json (default) | form | text | none
	Body     json.RawMessage   `json:"body,omitempty"`
}

// SuccessSpec decides whether the service accepted the message.
type SuccessSpec struct {
	Status []int     `json:"status,omitempty"` // [min, max], default [200, 299]
	JSON   *JSONTest `json:"json,omitempty"`
}

// JSONTest requires the response body's value at Path (dot-separated) to
// equal Equals, or one of its elements when Equals is an array.
type JSONTest struct {
	Path   string          `json:"path"`
	Equals json.RawMessage `json:"equals"`
}

// Target is one probe target a targets plugin adds to the catalog.
type Target struct {
	ID      string `json:"id"`
	Family  string `json:"family"`
	Region  string `json:"region"`
	Carrier string `json:"carrier"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
}

// Carriers are the carrier names the task catalog uses.
var Carriers = []string{"电信", "联通", "移动", "其他"}

const (
	maxSettings = 12
	maxComputed = 16
	maxHeaders  = 20
	maxHosts    = 20
	maxTargets  = 500
	maxBody     = 64 << 10
)

var (
	settingKey  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	hostPattern = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)
	targetID    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)
	refPattern  = regexp.MustCompile(`^(event|settings|computed)\.([a-z][a-z0-9_]{0,31})$`)
	forbidden   = []string{"Host", "Content-Length", "Transfer-Encoding", "Connection", "Cookie", "Te", "Trailer", "Upgrade", "Proxy-Authorization"}
)

// ParsePlugin decodes and checks a plugin.json and compiles its templates.
func ParsePlugin(raw []byte) (Plugin, error) {
	var p Plugin
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return Plugin{}, errors.New("plugin.json 不是有效的 JSON：" + err.Error())
	}
	p.Source = ""
	p.Name, p.Hint, p.Version, p.Author = strings.TrimSpace(p.Name), strings.TrimSpace(p.Hint), strings.TrimSpace(p.Version), strings.TrimSpace(p.Author)
	switch {
	case p.Type != TypePlugin:
		return Plugin{}, errors.New(`plugin.json 的 type 须为 "plugin"`)
	case !idPattern.MatchString(p.ID):
		return Plugin{}, errors.New("id 须为 2–32 位小写字母、数字或连字符，以字母开头")
	case slices.Contains(Reserved, p.ID) || slices.Contains([]string{"telegram", "webhook", "none"}, p.ID):
		return Plugin{}, fmt.Errorf("id %q 是保留字，换一个名字", p.ID)
	case p.Name == "" || utf8.RuneCountInString(p.Name) > 30:
		return Plugin{}, errors.New("name 必填，最多 30 个字")
	case utf8.RuneCountInString(p.Hint) > 60, utf8.RuneCountInString(p.Version) > 20, utf8.RuneCountInString(p.Author) > 40:
		return Plugin{}, errors.New("hint 最多 60 字，version 最多 20 字，author 最多 40 字")
	}
	switch p.Kind {
	case KindNotify:
		if p.Notify == nil || len(p.Targets) > 0 {
			return Plugin{}, errors.New(`kind 为 notify 的插件须有 notify，且不带 targets`)
		}
		c, err := compileNotify(p.Notify)
		if err != nil {
			return Plugin{}, fmt.Errorf("notify：%w", err)
		}
		p.compiled = c
	case KindTargets:
		if p.Notify != nil {
			return Plugin{}, errors.New(`kind 为 targets 的插件不带 notify`)
		}
		if err := checkTargets(p.Targets); err != nil {
			return Plugin{}, fmt.Errorf("targets：%w", err)
		}
	default:
		return Plugin{}, errors.New(`kind 须为 "notify"（通知渠道）或 "targets"（探测目标集）`)
	}
	return p, nil
}

// OpenPlugin validates a plugin archive: plugin.json only.
func OpenPlugin(archive []byte) (Plugin, error) {
	if len(archive) > MaxUpload {
		return Plugin{}, fmt.Errorf("压缩包最大 %d MB", MaxUpload>>20)
	}
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return Plugin{}, errors.New("不是有效的 zip 压缩包")
	}
	files, err := entries(zr)
	if err != nil {
		return Plugin{}, err
	}
	raw, ok := files["plugin.json"]
	if !ok {
		return Plugin{}, errors.New("压缩包根目录（或唯一的顶层文件夹）里没有 plugin.json")
	}
	if len(files) != 1 {
		return Plugin{}, errors.New("插件包只含 plugin.json 一个文件")
	}
	return ParsePlugin(raw)
}

// Info is what a market lists about any package.
type Info struct {
	ID, Type, Kind, Name, Hint, Version, Author string
	// For is the skin an add-on extends; empty for every other type.
	For         string
	Swatch      []string
	Permissions []string
	Manifest    any // *Manifest or *Plugin
}

// Inspect validates an archive of any type: a theme or add-on
// (theme.json) or a plugin (plugin.json).
func Inspect(archive []byte) (Info, error) {
	if len(archive) > MaxUpload {
		return Info{}, fmt.Errorf("压缩包最大 %d MB", MaxUpload>>20)
	}
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return Info{}, errors.New("不是有效的 zip 压缩包")
	}
	plugin := false
	for _, f := range zr.File {
		if base := f.Name[strings.LastIndex(f.Name, "/")+1:]; base == "plugin.json" {
			plugin = true
		}
	}
	if plugin {
		p, err := OpenPlugin(archive)
		if err != nil {
			return Info{}, err
		}
		return Info{ID: p.ID, Type: p.Type, Kind: p.Kind, Name: p.Name, Hint: p.Hint, Version: p.Version, Author: p.Author, Permissions: p.Permissions(), Manifest: &p}, nil
	}
	m, _, err := Open(archive)
	if err != nil {
		return Info{}, err
	}
	perms := []string(nil)
	if m.Frontend != nil {
		perms = []string{"独立 HTML / JavaScript 前台（隔离运行）", "只读公开监控数据；不授予后台权限"}
	}
	return Info{ID: m.ID, Type: m.Type, For: m.For, Name: m.Name, Hint: m.Hint, Version: m.Version, Author: m.Author, Swatch: m.Swatch, Permissions: perms, Manifest: &m}, nil
}

// Permissions describes in plain words what the plugin can do, for the
// install confirmation and the market review.
func (p Plugin) Permissions() []string {
	switch p.Kind {
	case KindNotify:
		if p.Notify == nil {
			return nil
		}
		if slices.Contains(p.Notify.Hosts, "*") {
			return []string{"把告警通知发到你在设置里填写的地址", "收到的内容：告警类型、节点名称、读数与时间"}
		}
		return []string{"把告警通知发到 " + strings.Join(p.Notify.Hosts, "、"), "收到的内容：告警类型、节点名称、读数与时间"}
	case KindTargets:
		regions := []string{}
		for _, t := range p.Targets {
			if !slices.Contains(regions, t.Region) {
				regions = append(regions, t.Region)
			}
		}
		if len(regions) > 8 {
			regions = append(regions[:8], "…")
		}
		return []string{fmt.Sprintf("为任务目录添加 %d 个探测目标（%s）", len(p.Targets), strings.Join(regions, "、")), "只添加可选目标，不会自动创建任务"}
	}
	return nil
}

// ---- notify

type compiledNotify struct {
	spec     *NotifySpec
	computed []compiledComputed
	url      *template
	headers  []compiledHeader
	body     compiledBody
	equals   []any
}

type compiledComputed struct {
	name, encode    string
	value, key, msg *template
	digest          *template
	when, unless    string
}

type compiledHeader struct {
	name     string
	optional bool
	value    *template
}

type compiledBody struct {
	kind string // json | form | text | none
	json any    // decoded JSON with *template leaves (and optionalKey keys)
	form []compiledHeader
	text *template
}

type optionalKey string

func compileNotify(n *NotifySpec) (*compiledNotify, error) {
	c := &compiledNotify{spec: n}
	if len(n.Hosts) == 0 || len(n.Hosts) > maxHosts {
		return nil, fmt.Errorf("hosts 须列出 1–%d 个域名，或 [\"*\"]", maxHosts)
	}
	wildcard := slices.Contains(n.Hosts, "*")
	if wildcard && len(n.Hosts) != 1 {
		return nil, errors.New(`hosts 用 "*" 时不能再列别的域名`)
	}
	for i, h := range n.Hosts {
		n.Hosts[i] = strings.ToLower(strings.TrimSpace(h))
		if !wildcard && !hostPattern.MatchString(n.Hosts[i]) {
			return nil, fmt.Errorf("hosts 里的 %q 不是有效的域名", h)
		}
	}
	if len(n.Settings) > maxSettings {
		return nil, fmt.Errorf("settings 最多 %d 项", maxSettings)
	}
	keys := map[string]bool{}
	hasURL := false
	for i := range n.Settings {
		s := &n.Settings[i]
		if err := checkSetting(s); err != nil {
			return nil, err
		}
		if keys[s.Key] {
			return nil, fmt.Errorf("settings 的 key %q 重复", s.Key)
		}
		keys[s.Key] = true
		hasURL = hasURL || s.Type == "url"
	}
	if wildcard && !hasURL {
		return nil, errors.New(`hosts 为 ["*"] 时须有一项 type 为 url 的设置，由管理员填写地址`)
	}
	defined := map[string]bool{}
	known := func(scope, name string) bool {
		if scope == "settings" {
			return keys[name]
		}
		return defined[name]
	}
	checkRef := func(ref string) (string, error) {
		if ref == "" {
			return "", nil
		}
		m := refPattern.FindStringSubmatch(ref)
		if m == nil || (m[1] == "event" && !slices.Contains(EventVars, m[2])) || (m[1] != "event" && !known(m[1], m[2])) {
			return "", fmt.Errorf("条件 %q 须指向已定义的 event.*、settings.* 或 computed.*", ref)
		}
		return ref, nil
	}
	if len(n.Computed) > maxComputed {
		return nil, fmt.Errorf("computed 最多 %d 项", maxComputed)
	}
	for _, cv := range n.Computed {
		if !settingKey.MatchString(cv.Name) {
			return nil, fmt.Errorf("computed 的 name %q 须为小写字母开头的字母、数字或下划线", cv.Name)
		}
		cc := compiledComputed{name: cv.Name, encode: cv.Encode}
		var err error
		if cc.when, err = checkRef(cv.When); err != nil {
			return nil, err
		}
		if cc.unless, err = checkRef(cv.Unless); err != nil {
			return nil, err
		}
		set := 0
		if cv.Value != "" {
			set++
			if cc.value, err = parseTemplate(cv.Value, known); err != nil {
				return nil, fmt.Errorf("computed %s：%w", cv.Name, err)
			}
		}
		if cv.HMACSHA256 != nil {
			set++
			if cc.key, err = parseTemplate(cv.HMACSHA256.Key, known); err != nil {
				return nil, fmt.Errorf("computed %s：%w", cv.Name, err)
			}
			if cc.msg, err = parseTemplate(cv.HMACSHA256.Message, known); err != nil {
				return nil, fmt.Errorf("computed %s：%w", cv.Name, err)
			}
		}
		if cv.SHA256 != "" {
			set++
			if cc.digest, err = parseTemplate(cv.SHA256, known); err != nil {
				return nil, fmt.Errorf("computed %s：%w", cv.Name, err)
			}
		}
		if set != 1 {
			return nil, fmt.Errorf("computed %s 须且只能有 value、hmac_sha256、sha256 其中一项", cv.Name)
		}
		if cv.Encode != "" && (cc.value != nil || !slices.Contains([]string{"base64", "base64url", "hex"}, cv.Encode)) {
			return nil, fmt.Errorf("computed %s 的 encode 须为 base64、base64url 或 hex，且只用于 hmac_sha256 / sha256", cv.Name)
		}
		defined[cv.Name] = true
		c.computed = append(c.computed, cc)
	}
	r := &n.Request
	r.Method = strings.ToUpper(strings.TrimSpace(r.Method))
	if r.Method == "" {
		r.Method = "POST"
	}
	if !slices.Contains([]string{"GET", "POST", "PUT"}, r.Method) {
		return nil, errors.New("request.method 须为 GET、POST 或 PUT")
	}
	var err error
	if c.url, err = parseTemplate(r.URL, known); err != nil || r.URL == "" {
		if err == nil {
			err = errors.New("不能为空")
		}
		return nil, fmt.Errorf("request.url：%w", err)
	}
	if len(r.Headers) > maxHeaders {
		return nil, fmt.Errorf("request.headers 最多 %d 个", maxHeaders)
	}
	names := make([]string, 0, len(r.Headers))
	for name := range r.Headers {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		h := compiledHeader{name: strings.TrimSuffix(name, "?"), optional: strings.HasSuffix(name, "?")}
		canon := textproto.CanonicalMIMEHeaderKey(h.name)
		if !validHeaderName(h.name) || slices.Contains(forbidden, canon) {
			return nil, fmt.Errorf("request.headers 不能设置 %q", h.name)
		}
		if h.value, err = parseTemplate(r.Headers[name], known); err != nil {
			return nil, fmt.Errorf("request.headers.%s：%w", h.name, err)
		}
		c.headers = append(c.headers, h)
	}
	if r.BodyType == "" {
		r.BodyType = "json"
		if len(r.Body) == 0 {
			r.BodyType = "none"
		}
	}
	c.body.kind = r.BodyType
	switch r.BodyType {
	case "none":
		if len(r.Body) > 0 {
			return nil, errors.New(`body_type 为 none 时不带 body`)
		}
	case "json":
		var v any
		if err := json.Unmarshal(r.Body, &v); err != nil {
			return nil, errors.New("request.body 不是有效的 JSON")
		}
		if c.body.json, err = compileJSON(v, known, 0); err != nil {
			return nil, fmt.Errorf("request.body：%w", err)
		}
	case "form":
		var form map[string]string
		if err := json.Unmarshal(r.Body, &form); err != nil {
			return nil, errors.New("body_type 为 form 时 body 须为字符串到字符串的对象")
		}
		keys := make([]string, 0, len(form))
		for k := range form {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			f := compiledHeader{name: strings.TrimSuffix(k, "?"), optional: strings.HasSuffix(k, "?")}
			if f.value, err = parseTemplate(form[k], known); err != nil {
				return nil, fmt.Errorf("request.body.%s：%w", f.name, err)
			}
			c.body.form = append(c.body.form, f)
		}
	case "text":
		var s string
		if err := json.Unmarshal(r.Body, &s); err != nil {
			return nil, errors.New("body_type 为 text 时 body 须为字符串")
		}
		if c.body.text, err = parseTemplate(s, known); err != nil {
			return nil, fmt.Errorf("request.body：%w", err)
		}
	default:
		return nil, errors.New("request.body_type 须为 json、form、text 或 none")
	}
	if s := n.Success.Status; len(s) != 0 && (len(s) != 2 || s[0] < 100 || s[1] > 599 || s[0] > s[1]) {
		return nil, errors.New("success.status 须为 [最小, 最大] 两个 HTTP 状态码")
	}
	if t := n.Success.JSON; t != nil {
		if t.Path == "" || len(t.Path) > 100 {
			return nil, errors.New("success.json.path 必填")
		}
		var eq any
		if err := json.Unmarshal(t.Equals, &eq); err != nil {
			return nil, errors.New("success.json.equals 须为 JSON 值")
		}
		if list, ok := eq.([]any); ok {
			c.equals = list
		} else {
			c.equals = []any{eq}
		}
	}
	return c, nil
}

func compileJSON(v any, known func(string, string) bool, depth int) (any, error) {
	if depth > 8 {
		return nil, errors.New("嵌套太深（最多 8 层）")
	}
	switch x := v.(type) {
	case string:
		return parseTemplate(x, known)
	case map[string]any:
		out := map[any]any{}
		for k, val := range x {
			c, err := compileJSON(val, known, depth+1)
			if err != nil {
				return nil, err
			}
			if strings.HasSuffix(k, "?") {
				out[optionalKey(strings.TrimSuffix(k, "?"))] = c
			} else {
				out[k] = c
			}
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			c, err := compileJSON(val, known, depth+1)
			if err != nil {
				return nil, err
			}
			out[i] = c
		}
		return out, nil
	}
	return v, nil
}

func validHeaderName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

func checkSetting(s *Setting) error {
	s.Label, s.Help, s.Placeholder = strings.TrimSpace(s.Label), strings.TrimSpace(s.Help), strings.TrimSpace(s.Placeholder)
	label := "settings." + s.Key
	switch {
	case !settingKey.MatchString(s.Key):
		return fmt.Errorf("settings 的 key %q 须为小写字母开头的字母、数字或下划线", s.Key)
	case s.Label == "" || utf8.RuneCountInString(s.Label) > 30:
		return fmt.Errorf("%s：label 必填，最多 30 字", label)
	case utf8.RuneCountInString(s.Help) > 200, utf8.RuneCountInString(s.Placeholder) > 100, len(s.Default) > 2048:
		return fmt.Errorf("%s：help 最多 200 字，placeholder 最多 100 字", label)
	}
	switch s.Type {
	case "text", "url":
	case "secret":
		s.Secret = true
	case "select":
		if len(s.Options) == 0 || len(s.Options) > 30 {
			return fmt.Errorf("%s：select 须有 1–30 个 options", label)
		}
		for _, o := range s.Options {
			if o.Value == "" || len(o.Value) > 200 || o.Label == "" || utf8.RuneCountInString(o.Label) > 40 {
				return fmt.Errorf("%s：options 的 value / label 不能为空", label)
			}
		}
	default:
		return fmt.Errorf("%s：type 须为 text、secret、url 或 select", label)
	}
	if s.Pattern != "" {
		if len(s.Pattern) > 200 {
			return fmt.Errorf("%s：pattern 太长", label)
		}
		re, err := regexp.Compile("^(?:" + s.Pattern + ")$")
		if err != nil {
			return fmt.Errorf("%s：pattern 不是有效的正则表达式", label)
		}
		s.pattern = re
	}
	if s.Default != "" && s.Secret {
		return fmt.Errorf("%s：密钥类设置不能有默认值", label)
	}
	return nil
}

// compiledNotify returns the compiled templates; only a notify plugin that
// came from ParsePlugin has them.
func (p Plugin) compiledNotify() (*compiledNotify, error) {
	if p.Kind != KindNotify || p.compiled == nil {
		return nil, errors.New("不是已解析的通知插件")
	}
	return p.compiled, nil
}

// CheckSettings validates values for a notify plugin's settings and returns
// them normalized (trimmed, defaults applied, unknown keys dropped).
func (p Plugin) CheckSettings(values map[string]string) (map[string]string, error) {
	c, err := p.compiledNotify()
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, s := range c.spec.Settings {
		v := strings.TrimSpace(values[s.Key])
		if v == "" {
			v = s.Default
		}
		if v == "" {
			if s.Required {
				return nil, fmt.Errorf("请填写「%s」", s.Label)
			}
			continue
		}
		if len(v) > 2048 {
			return nil, fmt.Errorf("「%s」太长", s.Label)
		}
		switch s.Type {
		case "url":
			u, err := url.Parse(v)
			if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
				return nil, fmt.Errorf("「%s」须为有效的 HTTP(S) 地址", s.Label)
			}
			if err := c.allowHost(u); err != nil {
				return nil, fmt.Errorf("「%s」%s", s.Label, err.Error())
			}
		case "select":
			ok := false
			for _, o := range s.Options {
				ok = ok || o.Value == v
			}
			if !ok {
				return nil, fmt.Errorf("「%s」须为给定的选项之一", s.Label)
			}
		}
		if s.pattern != nil && !s.pattern.MatchString(v) {
			return nil, fmt.Errorf("「%s」格式不对", s.Label)
		}
		out[s.Key] = v
	}
	return out, nil
}

// SecretKeys lists the settings never shown back to the panel.
func (p Plugin) SecretKeys() []string {
	if p.Notify == nil {
		return nil
	}
	var out []string
	for _, s := range p.Notify.Settings {
		if s.Secret || s.Type == "secret" {
			out = append(out, s.Key)
		}
	}
	return out
}

// allowHost checks a URL against the declared hosts. A fixed host list
// requires https; ["*"] (an address the administrator typed) may be http.
func (c *compiledNotify) allowHost(u *url.URL) error {
	host := strings.ToLower(u.Hostname())
	if slices.Contains(c.spec.Hosts, "*") {
		return nil
	}
	if u.Scheme != "https" {
		return errors.New("须为 https 地址")
	}
	if net.ParseIP(host) != nil {
		return fmt.Errorf("只能发往 %s", strings.Join(c.spec.Hosts, "、"))
	}
	for _, h := range c.spec.Hosts {
		if host == h || (strings.HasPrefix(h, "*.") && strings.HasSuffix(host, h[1:])) {
			return nil
		}
	}
	return fmt.Errorf("只能发往 %s", strings.Join(c.spec.Hosts, "、"))
}

// Request is a rendered notification request.
type Request struct {
	Method  string
	URL     string
	Headers http.Header
	Body    []byte
}

// RenderNotify builds the request for one event. settings must come from
// CheckSettings; event holds EventVars.
func (p Plugin) RenderNotify(settings, event map[string]string, now time.Time) (Request, error) {
	c, err := p.compiledNotify()
	if err != nil {
		return Request{}, err
	}
	v := vars{event: event, settings: settings, computed: map[string]string{}, now: map[string]string{
		"unix": strconv.FormatInt(now.Unix(), 10), "unix_ms": strconv.FormatInt(now.UnixMilli(), 10), "iso": now.UTC().Format(time.RFC3339),
	}}
	ref := func(r string) string {
		scope, name, _ := strings.Cut(r, ".")
		return v.lookup(scope, name)
	}
	for _, cc := range c.computed {
		if (cc.when != "" && ref(cc.when) == "") || (cc.unless != "" && ref(cc.unless) != "") {
			if _, ok := v.computed[cc.name]; !ok {
				v.computed[cc.name] = ""
			}
			continue
		}
		var sum []byte
		switch {
		case cc.value != nil:
			v.computed[cc.name] = cc.value.render(v)
			continue
		case cc.key != nil:
			mac := hmac.New(sha256.New, []byte(cc.key.render(v)))
			mac.Write([]byte(cc.msg.render(v)))
			sum = mac.Sum(nil)
		default:
			d := sha256.Sum256([]byte(cc.digest.render(v)))
			sum = d[:]
		}
		switch cc.encode {
		case "hex":
			v.computed[cc.name] = hex.EncodeToString(sum)
		case "base64url":
			v.computed[cc.name] = base64.RawURLEncoding.EncodeToString(sum)
		default:
			v.computed[cc.name] = base64.StdEncoding.EncodeToString(sum)
		}
	}
	req := Request{Method: c.spec.Request.Method, URL: c.url.render(v), Headers: http.Header{}}
	u, err := url.Parse(req.URL)
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return Request{}, errors.New("插件生成的地址无效，请检查设置")
	}
	if err := c.allowHost(u); err != nil {
		return Request{}, errors.New("插件生成的地址不在它声明的范围内：" + err.Error())
	}
	for _, h := range c.headers {
		val := h.value.render(v)
		if val == "" && h.optional {
			continue
		}
		if strings.ContainsAny(val, "\r\n") {
			return Request{}, fmt.Errorf("请求头 %s 含换行", h.name)
		}
		req.Headers.Set(h.name, val)
	}
	switch c.body.kind {
	case "json":
		body, err := json.Marshal(renderJSON(c.body.json, v))
		if err != nil {
			return Request{}, err
		}
		req.Body = body
		if req.Headers.Get("Content-Type") == "" {
			req.Headers.Set("Content-Type", "application/json; charset=utf-8")
		}
	case "form":
		form := url.Values{}
		for _, f := range c.body.form {
			if val := f.value.render(v); val != "" || !f.optional {
				form.Set(f.name, val)
			}
		}
		req.Body = []byte(form.Encode())
		if req.Headers.Get("Content-Type") == "" {
			req.Headers.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	case "text":
		req.Body = []byte(c.body.text.render(v))
		if req.Headers.Get("Content-Type") == "" {
			req.Headers.Set("Content-Type", "text/plain; charset=utf-8")
		}
	}
	if len(req.Body) > maxBody {
		return Request{}, fmt.Errorf("请求体超过 %d KB", maxBody>>10)
	}
	return req, nil
}

func renderJSON(node any, v vars) any {
	switch x := node.(type) {
	case *template:
		s := x.render(v)
		switch x.typed() {
		case "number":
			if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
				return f
			}
			return 0
		case "bool":
			return s != "" && s != "false" && s != "0"
		}
		return s
	case map[any]any:
		out := map[string]any{}
		for k, val := range x {
			r := renderJSON(val, v)
			if opt, ok := k.(optionalKey); ok {
				if r == "" || r == nil {
					continue
				}
				out[string(opt)] = r
				continue
			}
			out[k.(string)] = r
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = renderJSON(val, v)
		}
		return out
	}
	return node
}

// CheckResponse applies the success rule to a response.
func (p Plugin) CheckResponse(status int, body []byte) error {
	c, err := p.compiledNotify()
	if err != nil {
		return err
	}
	lo, hi := 200, 299
	if s := c.spec.Success.Status; len(s) == 2 {
		lo, hi = s[0], s[1]
	}
	if status < lo || status > hi {
		return fmt.Errorf("%s 返回 HTTP %d", p.Name, status)
	}
	t := c.spec.Success.JSON
	if t == nil {
		return nil
	}
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		return fmt.Errorf("%s 的回应不是 JSON", p.Name)
	}
	for _, key := range strings.Split(t.Path, ".") {
		m, ok := doc.(map[string]any)
		if !ok {
			doc = nil
			break
		}
		doc = m[key]
	}
	for _, want := range c.equals {
		if jsonEqual(doc, want) {
			return nil
		}
	}
	got, _ := json.Marshal(doc)
	return fmt.Errorf("%s 拒收：%s = %s", p.Name, t.Path, truncateBytes(string(got), 80))
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ---- targets

func checkTargets(ts []Target) error {
	if len(ts) == 0 || len(ts) > maxTargets {
		return fmt.Errorf("须有 1–%d 个目标", maxTargets)
	}
	seen := map[string]bool{}
	for i := range ts {
		t := &ts[i]
		t.Region, t.Host = strings.TrimSpace(t.Region), strings.TrimSpace(t.Host)
		label := fmt.Sprintf("第 %d 个目标", i+1)
		switch {
		case !targetID.MatchString(t.ID):
			return fmt.Errorf("%s：id %q 须为小写字母、数字或连字符", label, t.ID)
		case seen[t.ID]:
			return fmt.Errorf("目标 id %q 重复", t.ID)
		case t.Family != "ipv4" && t.Family != "ipv6":
			return fmt.Errorf("%s：family 须为 ipv4 或 ipv6", label)
		case t.Region == "" || utf8.RuneCountInString(t.Region) > 30:
			return fmt.Errorf("%s：region 必填，最多 30 字", label)
		case !slices.Contains(Carriers, t.Carrier):
			return fmt.Errorf("%s：carrier 须为 电信、联通、移动 或 其他", label)
		case t.Port < 1 || t.Port > 65535:
			return fmt.Errorf("%s：port 须为 1–65535", label)
		}
		if ip := net.ParseIP(t.Host); ip != nil {
			if (ip.To4() != nil) != (t.Family == "ipv4") {
				return fmt.Errorf("%s：地址 %s 与 family 不符", label, t.Host)
			}
		} else if len(t.Host) > 253 || !hostPattern.MatchString(strings.ToLower(t.Host)) || strings.HasPrefix(t.Host, "*.") {
			return fmt.Errorf("%s：host 须为 IP 地址或域名", label)
		}
		seen[t.ID] = true
	}
	return nil
}
