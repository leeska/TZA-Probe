package tzapkg

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// A template is text with {{ path | filter:arg | … }} placeholders. It is the
// only logic a declarative plugin has: no loops, no calls, no access to
// anything but the variables the host hands it. Paths are event.*, settings.*,
// computed.* and now.*; the filters are the fixed set below.
type template struct {
	parts []tplPart
}

type tplPart struct {
	text    string // literal text when expr is nil
	expr    *tplExpr
	literal bool
}

type tplExpr struct {
	scope, name string
	filters     []tplFilter
}

type tplFilter struct {
	name string
	arg  string
	n    int
}

var (
	placeholder = regexp.MustCompile(`\{\{\s*([^{}]*?)\s*\}\}`)
	pathPattern = regexp.MustCompile(`^(event|settings|computed|now)\.([a-z][a-z0-9_]{0,31})$`)
)

// filters lists the filter names; true when the filter takes an argument.
var filters = map[string]bool{
	"urlencode": false, "urlpath": false, "base64": false, "upper": false, "lower": false,
	"trim": false, "single_line": false, "truncate": true, "number": false, "bool": false,
}

// EventVars are the event.* variables a host must provide (docs/PLUGINS.md).
var EventVars = []string{"title", "text", "message", "status", "kind", "kind_name", "node", "node_id", "time", "timestamp", "id", "active", "urgent", "priority"}

var nowVars = []string{"unix", "unix_ms", "iso"}

const maxTemplate = 8 << 10

// parseTemplate compiles s; known reports whether a settings/computed name
// may be referenced at this point.
func parseTemplate(s string, known func(scope, name string) bool) (*template, error) {
	if len(s) > maxTemplate {
		return nil, fmt.Errorf("模板太长（最多 %d 字节）", maxTemplate)
	}
	t := &template{}
	last := 0
	for _, m := range placeholder.FindAllStringSubmatchIndex(s, -1) {
		if m[0] > last {
			t.parts = append(t.parts, tplPart{text: s[last:m[0]], literal: true})
		}
		expr, err := parseExpr(s[m[2]:m[3]], known)
		if err != nil {
			return nil, err
		}
		t.parts = append(t.parts, tplPart{expr: expr})
		last = m[1]
	}
	if last < len(s) {
		t.parts = append(t.parts, tplPart{text: s[last:], literal: true})
	}
	for _, p := range t.parts {
		if p.literal && (strings.Contains(p.text, "{{") || strings.Contains(p.text, "}}")) {
			return nil, fmt.Errorf("模板 %q 里有没闭合的 {{ }}", s)
		}
	}
	return t, nil
}

func parseExpr(src string, known func(scope, name string) bool) (*tplExpr, error) {
	fields := strings.Split(src, "|")
	path := strings.TrimSpace(fields[0])
	m := pathPattern.FindStringSubmatch(path)
	if m == nil {
		return nil, fmt.Errorf("变量 %q 无效：须为 event.*、settings.*、computed.* 或 now.*", path)
	}
	e := &tplExpr{scope: m[1], name: m[2]}
	switch e.scope {
	case "event":
		if !slices.Contains(EventVars, e.name) {
			return nil, fmt.Errorf("没有 event.%s 这个变量", e.name)
		}
	case "now":
		if !slices.Contains(nowVars, e.name) {
			return nil, fmt.Errorf("没有 now.%s 这个变量", e.name)
		}
	default:
		if !known(e.scope, e.name) {
			return nil, fmt.Errorf("%s.%s 未定义（settings 须在 settings 里声明，computed 须在使用前定义）", e.scope, e.name)
		}
	}
	for i, raw := range fields[1:] {
		name, arg, hasArg := strings.Cut(strings.TrimSpace(raw), ":")
		takesArg, ok := filters[name]
		if !ok {
			return nil, fmt.Errorf("没有 %q 这个过滤器", name)
		}
		if takesArg != hasArg {
			if takesArg {
				return nil, fmt.Errorf("过滤器 %s 需要参数，如 %s:100", name, name)
			}
			return nil, fmt.Errorf("过滤器 %s 不带参数", name)
		}
		f := tplFilter{name: name, arg: arg}
		if name == "truncate" {
			n, err := strconv.Atoi(arg)
			if err != nil || n < 1 || n > 100000 {
				return nil, errors.New("truncate 的参数须为 1–100000 的整数")
			}
			f.n = n
		}
		if (name == "number" || name == "bool") && i != len(fields)-2 {
			return nil, fmt.Errorf("过滤器 %s 只能放在最后", name)
		}
		e.filters = append(e.filters, f)
	}
	return e, nil
}

// typed reports whether t is one expression ending in number / bool, which a
// JSON body emits as that JSON type instead of a string.
func (t *template) typed() string {
	if len(t.parts) != 1 || t.parts[0].expr == nil {
		return ""
	}
	fs := t.parts[0].expr.filters
	if len(fs) == 0 {
		return ""
	}
	if last := fs[len(fs)-1].name; last == "number" || last == "bool" {
		return last
	}
	return ""
}

// vars is the variable environment of one render.
type vars struct {
	event, settings, computed, now map[string]string
}

func (v vars) lookup(scope, name string) string {
	switch scope {
	case "event":
		return v.event[name]
	case "settings":
		return v.settings[name]
	case "computed":
		return v.computed[name]
	}
	return v.now[name]
}

func (t *template) render(v vars) string {
	var b strings.Builder
	for _, p := range t.parts {
		if p.literal {
			b.WriteString(p.text)
			continue
		}
		b.WriteString(p.expr.apply(v.lookup(p.expr.scope, p.expr.name)))
	}
	return b.String()
}

func (e *tplExpr) apply(s string) string {
	for _, f := range e.filters {
		switch f.name {
		case "urlencode":
			s = url.QueryEscape(s)
		case "urlpath":
			s = url.PathEscape(s)
		case "base64":
			s = base64.StdEncoding.EncodeToString([]byte(s))
		case "upper":
			s = strings.ToUpper(s)
		case "lower":
			s = strings.ToLower(s)
		case "trim":
			s = strings.TrimSpace(s)
		case "single_line":
			s = strings.Join(strings.Fields(s), " ")
		case "truncate":
			if utf8.RuneCountInString(s) > f.n {
				r := []rune(s)
				s = string(r[:max(f.n-1, 0)]) + "…"
			}
		}
	}
	return s
}
