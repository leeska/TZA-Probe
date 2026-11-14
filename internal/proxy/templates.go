package proxy

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// Rule policies.
const (
	PolicyProxy  = "PROXY"
	PolicyDirect = "DIRECT"
	PolicyReject = "REJECT"
)

// Rule is one line of a rule template, written as in Clash:
// TYPE,VALUE,POLICY[,no-resolve]; MATCH,POLICY ends the list.
type Rule struct {
	Type      string `json:"type"`
	Value     string `json:"value,omitempty"`
	Policy    string `json:"policy"`
	NoResolve bool   `json:"no_resolve,omitempty"`
}

var ruleTypes = map[string]bool{"DOMAIN": true, "DOMAIN-SUFFIX": true, "DOMAIN-KEYWORD": true, "IP-CIDR": true, "IP-CIDR6": true, "GEOSITE": true, "GEOIP": true, "MATCH": true}

var geoName = regexp.MustCompile(`^!?[a-z0-9][a-z0-9@!_.-]{0,62}$`)

func policyOf(s string) (string, bool) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "PROXY", "代理", GroupSelect:
		return PolicyProxy, true
	case "DIRECT", "直连":
		return PolicyDirect, true
	case "REJECT", "拒绝", "屏蔽":
		return PolicyReject, true
	}
	return "", false
}

// ParseRules reads a template: one rule per line, blank lines and lines
// starting with # ignored. A missing final MATCH sends the rest to the proxy.
func ParseRules(text string) ([]Rule, error) {
	out := []Rule{}
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		bad := func(why string) error { return fmt.Errorf("第 %d 行「%s」：%s", i+1, line, why) }
		if len(out) > 0 && out[len(out)-1].Type == "MATCH" {
			return nil, bad("MATCH 之后不能再有规则")
		}
		parts := strings.Split(line, ",")
		for j := range parts {
			parts[j] = strings.TrimSpace(parts[j])
		}
		r := Rule{Type: strings.ToUpper(parts[0])}
		if !ruleTypes[r.Type] {
			return nil, bad("不支持的规则类型（可用 DOMAIN、DOMAIN-SUFFIX、DOMAIN-KEYWORD、IP-CIDR、GEOSITE、GEOIP、MATCH）")
		}
		if r.Type == "MATCH" {
			if len(parts) != 2 {
				return nil, bad("MATCH 的写法是 MATCH,代理")
			}
			p, ok := policyOf(parts[1])
			if !ok {
				return nil, bad("出口只能是 代理、DIRECT 或 REJECT")
			}
			r.Policy = p
			out = append(out, r)
			continue
		}
		if len(parts) < 3 || len(parts) > 4 || parts[1] == "" {
			return nil, bad("写法是 类型,值,出口")
		}
		p, ok := policyOf(parts[2])
		if !ok {
			return nil, bad("出口只能是 代理、DIRECT 或 REJECT")
		}
		r.Value, r.Policy = parts[1], p
		if len(parts) == 4 {
			if !strings.EqualFold(parts[3], "no-resolve") {
				return nil, bad("第四项只能是 no-resolve")
			}
			r.NoResolve = true
		}
		switch r.Type {
		case "IP-CIDR", "IP-CIDR6":
			if _, err := netip.ParsePrefix(r.Value); err != nil {
				return nil, bad("不是有效的 IP 段")
			}
		case "GEOSITE", "GEOIP":
			r.Value = strings.ToLower(r.Value)
			if !geoName.MatchString(r.Value) {
				return nil, bad("规则集名字无效")
			}
		default:
			if strings.ContainsAny(r.Value, " \"'") || len(r.Value) > 253 {
				return nil, bad("域名写法无效")
			}
		}
		out = append(out, r)
	}
	if len(out) > 2000 {
		return nil, errors.New("规则最多 2000 条")
	}
	if len(out) == 0 || out[len(out)-1].Type != "MATCH" {
		out = append(out, Rule{Type: "MATCH", Policy: PolicyProxy})
	}
	return out, nil
}

// Template is a named rule list. The built-in ones (ID "rule", "global")
// cannot be edited.
type Template struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Rules   string `json:"rules"`
	BuiltIn bool   `json:"built_in,omitempty"`
	Created int64  `json:"created_at,omitempty"`
}

// BuiltInTemplates are the templates every panel has.
var BuiltInTemplates = []Template{
	{ID: ModeRule, Name: "国内直连", BuiltIn: true, Rules: "# 私有地址和中国大陆直连，其余走代理\nGEOSITE,private,DIRECT\nGEOIP,private,DIRECT,no-resolve\nGEOSITE,cn,DIRECT\nGEOIP,CN,DIRECT\nMATCH,代理"},
	{ID: ModeGlobal, Name: "全局代理", BuiltIn: true, Rules: "# 全部走代理（私有地址除外）\nGEOIP,private,DIRECT,no-resolve\nMATCH,代理"},
}

// Validate checks a custom template.
func (t *Template) Validate() error {
	t.Name = strings.TrimSpace(t.Name)
	if t.Name == "" || utf8.RuneCountInString(t.Name) > 40 {
		return errors.New("名称需要 1–40 个字")
	}
	if len(t.Rules) > 256<<10 {
		return errors.New("规则太长")
	}
	_, err := ParseRules(t.Rules)
	return err
}

func clashPolicy(p string) string {
	switch p {
	case PolicyDirect:
		return "DIRECT"
	case PolicyReject:
		return "REJECT"
	}
	return GroupSelect
}

// clashRules renders rules as Clash rule strings.
func clashRules(rules []Rule) []string {
	out := []string{}
	for _, r := range rules {
		switch r.Type {
		case "MATCH":
			out = append(out, "MATCH,"+clashPolicy(r.Policy))
		case "GEOIP":
			line := "GEOIP," + strings.ToUpper(r.Value) + "," + clashPolicy(r.Policy)
			if r.Value == "private" {
				line = "GEOIP,private," + clashPolicy(r.Policy)
			}
			if r.NoResolve {
				line += ",no-resolve"
			}
			out = append(out, line)
		default:
			line := r.Type + "," + r.Value + "," + clashPolicy(r.Policy)
			if r.NoResolve {
				line += ",no-resolve"
			}
			out = append(out, line)
		}
	}
	return out
}

// usesChina reports whether the rules split mainland China out, which
// decides the DNS setup.
func usesChina(rules []Rule) bool {
	return slices.ContainsFunc(rules, func(r Rule) bool {
		return (r.Type == "GEOSITE" && r.Value == "cn") || (r.Type == "GEOIP" && r.Value == "cn")
	})
}

// singBoxRoute renders rules as sing-box route rules, the rule sets they use
// and the final outbound.
func singBoxRoute(rules []Rule) (out []any, sets []any, final string) {
	used := []string{}
	useSet := func(tag, repo string) string {
		if !slices.Contains(used, tag) {
			used = append(used, tag)
			sets = append(sets, ruleSet(tag, repo))
		}
		return tag
	}
	final = GroupSelect
	for _, r := range rules {
		rule := obj{}
		switch r.Type {
		case "MATCH":
			if r.Policy == PolicyDirect {
				final = "direct"
			}
			// REJECT as a final has no sing-box outbound; keep the proxy.
			continue
		case "DOMAIN":
			rule["domain"] = []string{r.Value}
		case "DOMAIN-SUFFIX":
			rule["domain_suffix"] = []string{r.Value}
		case "DOMAIN-KEYWORD":
			rule["domain_keyword"] = []string{r.Value}
		case "IP-CIDR", "IP-CIDR6":
			rule["ip_cidr"] = []string{r.Value}
		case "GEOSITE":
			rule["rule_set"] = []string{useSet("geosite-"+r.Value, "sing-geosite")}
		case "GEOIP":
			if r.Value == "private" {
				rule["ip_is_private"] = true
			} else {
				rule["rule_set"] = []string{useSet("geoip-"+r.Value, "sing-geoip")}
			}
		}
		switch r.Policy {
		case PolicyReject:
			rule["action"] = "reject"
		case PolicyDirect:
			rule["outbound"] = "direct"
		default:
			rule["outbound"] = GroupSelect
		}
		out = append(out, rule)
	}
	return out, sets, final
}
