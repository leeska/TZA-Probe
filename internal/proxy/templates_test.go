package proxy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseRules(t *testing.T) {
	rules, err := ParseRules("# comment\nDOMAIN-SUFFIX, openai.com ,代理\nDOMAIN-KEYWORD,ads,REJECT\nIP-CIDR,10.0.0.0/8,DIRECT,no-resolve\nGEOSITE,CN,直连\nGEOIP,cn,DIRECT")
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 6 || rules[0].Value != "openai.com" || rules[0].Policy != PolicyProxy || rules[3].Value != "cn" || rules[5].Type != "MATCH" {
		t.Fatalf("%+v", rules)
	}
	for _, bad := range []string{"FOO,a,DIRECT", "DOMAIN,a", "DOMAIN,a,ELSEWHERE", "IP-CIDR,999.0.0.0/8,DIRECT", "MATCH,代理\nDOMAIN,a,DIRECT", "DOMAIN,a b,DIRECT", "GEOIP,CN,DIRECT,resolve"} {
		if _, err := ParseRules(bad); err == nil {
			t.Errorf("%q 要拒绝", bad)
		}
	}
	for _, tpl := range BuiltInTemplates {
		if _, err := ParseRules(tpl.Rules); err != nil {
			t.Errorf("内置模板 %s：%v", tpl.Name, err)
		}
	}
}

func TestRuleTranslation(t *testing.T) {
	rules, _ := ParseRules("DOMAIN-SUFFIX,openai.com,代理\nDOMAIN-KEYWORD,ads,REJECT\nGEOIP,private,DIRECT,no-resolve\nGEOSITE,cn,DIRECT\nGEOIP,CN,DIRECT\nMATCH,DIRECT")
	clash := strings.Join(clashRules(rules), "|")
	if clash != "DOMAIN-SUFFIX,openai.com,节点选择|DOMAIN-KEYWORD,ads,REJECT|GEOIP,private,DIRECT,no-resolve|GEOSITE,cn,DIRECT|GEOIP,CN,DIRECT|MATCH,DIRECT" {
		t.Fatal(clash)
	}
	route, sets, final := singBoxRoute(rules)
	raw, _ := json.Marshal(route)
	for _, want := range []string{`{"domain_suffix":["openai.com"],"outbound":"节点选择"}`, `{"action":"reject","domain_keyword":["ads"]}`, `{"ip_is_private":true,"outbound":"direct"}`, `"rule_set":["geosite-cn"]`, `"rule_set":["geoip-cn"]`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("缺 %s：%s", want, raw)
		}
	}
	if len(sets) != 2 || final != "direct" {
		t.Fatal(sets, final)
	}
}
