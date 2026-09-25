package model

import (
	"strings"
	"testing"
	"time"
)

func TestBillingCycleClampAndLeapYear(t *testing.T) {
	cases := []struct {
		now, anchor string
		start, end  string
	}{ // 锚点的「日」与时分秒逐月顺延，短月取当月最后一天。
		{"2026-02-27T12:00:00Z", "2000-01-31T00:00:00Z", "2026-01-31T00:00:00Z", "2026-02-28T00:00:00Z"},
		{"2026-02-28T00:00:00Z", "2000-01-31T00:00:00Z", "2026-02-28T00:00:00Z", "2026-03-31T00:00:00Z"},
		{"2024-02-29T10:00:00Z", "2000-01-31T00:00:00Z", "2024-02-29T00:00:00Z", "2024-03-31T00:00:00Z"},
		{"2026-01-01T00:00:00Z", "2000-01-01T00:00:00Z", "2026-01-01T00:00:00Z", "2026-02-01T00:00:00Z"},
		// 周期时刻精确到秒：重置在 12:30:45 时边界也是 12:30:45。
		{"2026-09-01T12:30:44Z", "2026-09-01T12:30:45Z", "2026-08-01T12:30:45Z", "2026-09-01T12:30:45Z"},
		{"2026-09-01T12:30:45Z", "2026-09-01T12:30:45Z", "2026-09-01T12:30:45Z", "2026-10-01T12:30:45Z"},
		{"2026-09-22T12:30:45Z", "2026-09-01T12:30:45Z", "2026-09-01T12:30:45Z", "2026-10-01T12:30:45Z"},
		// 锚点在将来时，周期起点也必须落在 now 之前。
		{"2026-01-15T00:00:00Z", "2026-03-10T08:00:00Z", "2026-01-10T08:00:00Z", "2026-02-10T08:00:00Z"},
	}
	for _, c := range cases {
		now, _ := time.Parse(time.RFC3339, c.now)
		anchor, _ := time.Parse(time.RFC3339, c.anchor)
		start, end := Cycle(now, anchor)
		if start.Format(time.RFC3339) != c.start || end.Format(time.RFC3339) != c.end {
			t.Fatalf("%s anchor %s: %s -> %s", c.now, c.anchor, start, end)
		}
	}
}

func TestBillingAnchorFallsBack(t *testing.T) {
	now, _ := time.Parse(time.RFC3339, "2026-09-22T12:00:00Z")
	for _, raw := range []string{"", "1", "2026-09-01 00:00:00"} {
		// 缺省或格式不对时退回「当月 1 日 00:00:00Z」，而不是让整个周期算不出来。
		if got := BillingAnchor(raw, now).Format(time.RFC3339); got != "2026-09-01T00:00:00Z" {
			t.Fatalf("anchor %q = %s", raw, got)
		}
	}
	if got := BillingAnchor("2026-08-31T06:07:08Z", now).Format(time.RFC3339); got != "2026-08-31T06:07:08Z" {
		t.Fatalf("anchor = %s", got)
	}
}
func TestRouteRequiresEvidence(t *testing.T) {
	if RouteLabel(nil) != "未知" {
		t.Fatal("empty route must be unknown")
	}
	hops := []Hop{{ASN: "AS10099"}, {ASN: "AS4837"}, {ASN: "AS4837"}}
	if got := RouteLabel(hops); got != "10099 → 4837" {
		t.Fatal(got)
	}
	// GIA and GT share AS4809, so the split needs hop prefix evidence.
	if RouteLabel([]Hop{{ASN: "AS4809"}}) != "CN2" {
		t.Fatal("ASN alone must not invent a GIA or GT product classification")
	}
	// GT hands the traffic to the 163 backbone on the China side of the trace, so
	// a 202.97.* hop behind the CN2 hops is what separates it from GIA. The 163
	// hops on either side of the CN2 run are that same product, not a second
	// backbone, so the label stays a single name.
	gt := []Hop{
		{ASN: "AS25820", Address: "45.78.*.*"},
		{ASN: "AS4809", Address: "59.43.1.*"},
		{ASN: "AS4134", Address: "202.97.1.*"},
		{ASN: "AS4134", Address: "202.97.2.*"},
		{ASN: "AS4134", Address: "202.96.*.*"},
	}
	if got := RouteLabel(gt); got != "CN2GT" {
		t.Fatalf("CN2GT: %s", got)
	}
	// A live 电信 trace: the CN2 hops came back without an ASN, so the 59.43.*
	// addresses are the only CN2 evidence, and 163 shows up both in front of and
	// behind them. The 202.97.* hop behind them is what proves CN2 GT.
	unresolved := []Hop{
		{ASN: "AS25820", Address: "45.78.*.*"},
		{ASN: "AS4134", Address: "218.30.*.*"},
		{Address: "59.43.*.*"},
		{Address: "59.43.*.*"},
		{ASN: "AS4134", Address: "202.97.1.*"},
	}
	if got := RouteLabel(unresolved); got != "CN2GT" {
		t.Fatalf("unresolved CN2 hops: %s", got)
	}
	// The reported regression: a GIA trace whose China Telecom hops are AS4134
	// must not become GT. 218.30.* is 电信's international entry and 110.184.* is
	// the target province's access network; neither is the 163 backbone, and no
	// 202.97.* hop follows the CN2 run, so the line is GIA.
	gia := []Hop{
		{ASN: "AS25820", Address: "45.78.*.*"},
		{ASN: "AS4134", Address: "218.30.*.*"},
		{ASN: "AS4809", Address: "59.43.*.*"},
		{Address: "59.43.*.*"},
		{ASN: "AS4134", Address: "110.184.*.*"},
	}
	if got := RouteLabel(gia); got != "CN2GIA" {
		t.Fatalf("live GIA trace: %s", got)
	}
	// Same evidence without the ASN on the CN2 hops, and a 163 hop in front of
	// the CN2 run that a prefix alone proves.
	giaByPrefix := []Hop{
		{Address: "219.141.*.*"},
		{Address: "59.43.*.*"},
		{ASN: "AS4134", Address: "110.190.*.*"},
	}
	if got := RouteLabel(giaByPrefix); got != "CN2GIA" {
		t.Fatalf("GIA proven by prefix: %s", got)
	}
	// A CTG trace crosses the same China Telecom access ranges, so it stays one
	// name as well instead of reporting an extra 163 hop.
	ctg := []Hop{
		{ASN: "AS25820", Address: "45.78.*.*"},
		{ASN: "AS4134", Address: "218.30.*.*"},
		{ASN: "AS23764", Address: "69.194.*.*"},
	}
	if got := RouteLabel(ctg); got != "CTG" {
		t.Fatalf("CTG trace: %s", got)
	}
	// A CTG trace that also shows CN2 hops is still a CTG line: CTG wins over the
	// CN2 split, the same way the neighbouring route classifiers resolve 电信.
	ctgWithCN2 := []Hop{
		{ASN: "AS25820", Address: "45.78.*.*"},
		{ASN: "AS4809", Address: "59.43.*.*"},
		{ASN: "AS23764", Address: "69.194.*.*"},
	}
	if got := RouteLabel(ctgWithCN2); got != "CTG" {
		t.Fatalf("CTG with CN2 hops: %s", got)
	}
	// 电信 is one name, but another carrier's backbone still joins the chain.
	ctThenCU := []Hop{
		{ASN: "AS4134", Address: "202.97.1.*"},
		{ASN: "AS4837", Address: "219.158.*.*"},
	}
	if got := RouteLabel(ctThenCU); got != "163 → 4837" {
		t.Fatalf("163 then 4837: %s", got)
	}
	// The ASN stays authoritative when it disagrees with the address prefix.
	if got := RouteLabel([]Hop{{ASN: "AS9929", Address: "219.158.*.*"}}); got != "9929" {
		t.Fatal(got)
	}
	// 移动 is one product too: CMI and CMIN2 are both 中移国际, and a trace that
	// crosses both is the CMIN2 line — the same rule 电信 gets.
	cmMixed := []Hop{
		{ASN: "AS58453", Address: "2402:4f00:****"},
		{ASN: "AS58807", Address: "2402:4f00:****"},
		{ASN: "AS9808", Address: "2409:8080:****"},
		{ASN: "AS139080", Address: "2409:8c62:****"},
	}
	if got := RouteLabel(cmMixed); got != "CMIN2" {
		t.Fatalf("CMI then CMIN2: %s", got)
	}
	if got := RouteLabel([]Hop{{ASN: "AS58453", Address: "223.119.*.*"}}); got != "CMI" {
		t.Fatalf("CMI only: %s", got)
	}
	for _, c := range []struct {
		hops []Hop
		want string
	}{
		{[]Hop{{ASN: "AS4134", Address: "202.97.1.*"}}, "163"},
		{[]Hop{{ASN: "AS4134"}}, "163"},
		{[]Hop{{Address: "202.97.1.*"}}, "163"},
		{[]Hop{{ASN: "AS23764", Address: "203.0.113.*"}}, "CTG"},
		{[]Hop{{ASN: "AS58807"}}, "CMIN2"},
		// 中国移动's own ASN carries its domestic backbone and its international
		// entry alike, so it names no 中移国际 product on its own. Naming those
		// hops CMI used to add a phantom CMI leg to every 移动 回程.
		{[]Hop{{ASN: "AS9808"}}, "未知"},
		{[]Hop{{ASN: "AS9808", Address: "2409:8080:****"}}, "未知"},
		{[]Hop{{ASN: "AS139080", Address: "2409:8c62:****"}}, "未知"},
		{[]Hop{{Address: "2409:8080:****"}}, "未知"},
		{[]Hop{{Address: "221.183.*.*"}}, "未知"},
		{[]Hop{{Address: "111.24.*.*"}}, "未知"},
		{[]Hop{{Address: "111.13.*.*"}}, "未知"},
		// 223.120.* is half CMI (AS58453) and half CMIN2 (AS58807); the mask
		// cannot tell the halves apart, so it stays unknown.
		{[]Hop{{Address: "223.120.*.*"}}, "未知"},
		// The tool regularly returns no ASN at all. Prefixes carry those hops.
		{[]Hop{{Address: "210.51.*.*"}}, "9929"},
		{[]Hop{{Address: "219.158.*.*"}}, "4837"},
		{[]Hop{{Address: "240e:1234:****"}}, "163"},
		{[]Hop{{Address: "223.119.*.*"}}, "CMI"},
		{[]Hop{{Address: "69.194.*.*"}}, "CTG"},
		{[]Hop{{Address: "103.239.176.*"}}, "10099"},
		{[]Hop{{Address: "2408:8120:****"}}, "4837"},
		// Entries the RIPE registry attributes to another carrier, and the CMIN2
		// IPv6 range the mask cannot split, stay unknown: a wrong operator is
		// worse than an honest 未知.
		{[]Hop{{Address: "210.14.*.*"}}, "未知"},
		{[]Hop{{Address: "103.214.*.*"}}, "未知"},
		{[]Hop{{Address: "2402:4f00:****"}}, "未知"},
		// An unlisted prefix stays unknown rather than being guessed.
		{[]Hop{{Address: "203.0.113.*.*"}}, "未知"},
	} {
		if got := RouteLabel(c.hops); got != c.want {
			t.Fatalf("%v: %s", c.hops, got)
		}
	}
}

// A real 移动 回程 recorded on 2026-09-22 towards 四川移动 (2409:8c62:…, AS139080).
// The trace leaves the node through AS58453 (CMI) and AS58807 (CMIN2), then
// hands off to 中国移动's own backbone (AS9808) for hops 6-14. It used to read
// "CMI → CMIN2" because every AS9808 hop was named CMI; the line is the CMIN2
// product and the domestic hops are its landing side.
func TestRouteLabelOnRecordedMobileTrace(t *testing.T) {
	hops := []Hop{
		{TTL: 1, Address: "2602:f656:****"},
		{TTL: 2, Address: "2602:f656:****", ASN: "AS1054"},
		{TTL: 3, Address: "2402:4f00:****", ASN: "AS58453"},
		{TTL: 4, Address: "2402:4f00:****", ASN: "AS58807"},
		{TTL: 5, Address: "2402:4f00:****", ASN: "AS58807"},
		{TTL: 6, Address: "2409:8080:****", ASN: "AS9808"},
		{TTL: 7, Address: "2409:8080:****", ASN: "AS9808"},
		{TTL: 8, Address: "2409:8080:****", ASN: "AS9808"},
		{TTL: 9, Address: "2409:8080:****", ASN: "AS9808"},
		{TTL: 10, Address: "*"},
		{TTL: 11, Address: "2409:8062:****", ASN: "AS9808"},
		{TTL: 12, Address: "2409:8062:****", ASN: "AS139080"},
		{TTL: 13, Address: "*"},
		{TTL: 14, Address: "2409:8c62:****", ASN: "AS139080"},
	}
	if got := RouteLabel(hops); got != "CMIN2" {
		t.Fatalf("recorded 四川移动 trace: %s", got)
	}
}

// Recorded 中国联通 回程 (masked addresses, measured ASNs, `*` gaps preserved),
// replayed from the production store on 2026-09-25. AS4837 is 联通's own 169
// backbone and the network its 9929 international line lands on, so a trace
// that crossed 9929 names that line, not the 169 hops behind it; the same task
// towards a 联通 target without 9929 keeps 4837, because there the 169 backbone
// is the line the traffic crossed.
func TestRouteLabelOnRecordedUnicomTrace(t *testing.T) {
	// 734 of the stored 联通 results read "10099 → 9929 → 4837": 联通国际 entry
	// (AS10099, 162.219.*), the 9929 line (218.105.*), then 169 (219.158.*,
	// 58.144.*) all the way to the 四川 target.
	on9929 := []Hop{
		{TTL: 1, Address: "10.29.*.*"},
		{TTL: 2, Address: "*"},
		{TTL: 3, Address: "45.78.*.*", ASN: "AS25820"},
		{TTL: 4, Address: "142.0.*.*", ASN: "AS25820"},
		{TTL: 5, Address: "118.26.*.*"},
		{TTL: 6, Address: "162.219.*.*", ASN: "AS10099"},
		{TTL: 7, Address: "218.105.*.*", ASN: "AS9929"},
		{TTL: 8, Address: "*"},
		{TTL: 9, Address: "*"},
		{TTL: 10, Address: "210.14.*.*"},
		{TTL: 11, Address: "219.158.*.*", ASN: "AS4837"},
		{TTL: 12, Address: "219.158.*.*", ASN: "AS4837"},
		{TTL: 13, Address: "58.144.*.*", ASN: "AS4837"},
		{TTL: 14, Address: "*"},
		{TTL: 15, Address: "58.144.*.*", ASN: "AS4837"},
	}
	// 358 stored results went straight from 联通国际 to 169 ("10099 → 4837"):
	// no 9929 hop anywhere, so 4837 is the 联通 name.
	on169 := []Hop{
		{TTL: 1, Address: "10.29.*.*"},
		{TTL: 2, Address: "*"},
		{TTL: 3, Address: "45.78.*.*", ASN: "AS25820"},
		{TTL: 4, Address: "142.0.*.*", ASN: "AS25820"},
		{TTL: 5, Address: "118.26.*.*"},
		{TTL: 6, Address: "162.219.*.*", ASN: "AS10099"},
		{TTL: 7, Address: "210.14.*.*"},
		{TTL: 8, Address: "*"},
		{TTL: 9, Address: "*"},
		{TTL: 10, Address: "210.14.*.*"},
		{TTL: 11, Address: "219.158.*.*", ASN: "AS4837"},
		{TTL: 12, Address: "219.158.*.*", ASN: "AS4837"},
		{TTL: 13, Address: "58.144.*.*", ASN: "AS4837"},
		{TTL: 14, Address: "*"},
		{TTL: 15, Address: "58.144.*.*", ASN: "AS4837"},
	}
	// An IPv6 trace from the same task (2401:8a00:* = 联通国际): the tool left
	// hop 5 without an ASN, so the prefix table reads 2408:8120:* as 4837 even
	// though hop 6 resolves that same address space as AS9929. The 169 hops
	// behind the 9929 line are its landing side either way.
	on9929v6 := []Hop{
		{TTL: 1, Address: "2602:f656:****"},
		{TTL: 2, Address: "2602:f656:****", ASN: "AS1054"},
		{TTL: 3, Address: "2401:8a00:****", ASN: "AS10099"},
		{TTL: 4, Address: "2401:8a00:****", ASN: "AS10099"},
		{TTL: 5, Address: "2408:8120:****"},
		{TTL: 6, Address: "2408:8120:****", ASN: "AS9929"},
		{TTL: 7, Address: "*"},
		{TTL: 8, Address: "2408:8000:****", ASN: "AS4837"},
		{TTL: 9, Address: "2408:8001:****", ASN: "AS4837"},
		{TTL: 10, Address: "2408:8762:****", ASN: "AS4837"},
		{TTL: 11, Address: "*"},
		{TTL: 12, Address: "2408:8763:****", ASN: "AS4837"},
	}
	for _, c := range []struct {
		name string
		hops []Hop
		want string
	}{
		{"9929 line with 169 behind it", on9929, "10099 → 9929"},
		{"169 only, no 9929", on169, "10099 → 4837"},
		{"IPv6 9929 line with 169 behind it", on9929v6, "10099 → 9929"},
	} {
		if got := RouteLabel(c.hops); got != c.want {
			t.Fatalf("%s: %s", c.name, got)
		}
	}
	// 9929 alone keeps its own name, and another carrier's backbone still joins
	// the chain: the rule only removes 联通's 169 landing side.
	if got := RouteLabel([]Hop{{Address: "218.105.*.*"}}); got != "9929" {
		t.Fatalf("9929 only: %s", got)
	}
	if got := RouteLabel([]Hop{{ASN: "AS4134", Address: "202.97.1.*"}, {ASN: "AS9929", Address: "218.105.*.*"}, {ASN: "AS4837", Address: "219.158.*.*"}}); got != "163 → 9929" {
		t.Fatalf("163 then the 9929 line: %s", got)
	}
}

func TestMasks(t *testing.T) {
	for input, want := range map[string]string{"219.158.1.3": "219.158.*.*", "2001:db8::1": "2001:db8:****", "anything": "*"} {
		if got := MaskAddress(input); got != want {
			t.Fatalf("%s: %s", input, got)
		}
	}
}
func TestTaskBoundaries(t *testing.T) {
	base := Task{Name: "test", Kind: "route", Host: "example.com", Port: 80, Family: "ipv4", Region: "上海", Carrier: "电信", Interval: 900}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"-evil", "example.com;id", "$(id)", "https://example.com", "::1"} {
		x := base
		x.Host = host
		if x.Validate() == nil {
			t.Fatalf("accepted %s", host)
		}
	}
	base.Interval = 899
	if base.Validate() == nil {
		t.Fatal("accepted excessive route frequency")
	}
}

func TestPublicAddressRejectsEmbeddedSecrets(t *testing.T) {
	for _, s := range []string{"219.158.1.1*", "* 10.0.0.1", "2001:db8::1:****", "hostname.internal*"} {
		if PublicAddress(s) != "*" {
			t.Fatal(s)
		}
	}
	if PublicAddress("219.158.*.*") != "219.158.*.*" {
		t.Fatal("valid masked IPv4 lost")
	}
}

func TestCombinedTaskValidation(t *testing.T) {
	task := Task{Name: "海外双栈", Checks: []string{"tcp", "route"}, Targets: []Target{
		{Host: "example.com", Port: 443, Family: "ipv4", Region: "新加坡", Carrier: "其他"},
		{Host: "example.com", Port: 443, Family: "ipv6", Region: "新加坡", Carrier: "其他"},
	}, Interval: 60, RouteInterval: 900}
	if err := task.Validate(); err != nil {
		t.Fatal(err)
	}
	task.Targets[1].Host = "1.1.1.1"
	if err := task.Validate(); err == nil {
		t.Fatal("accepted mismatched family")
	}
	task.Targets[1].Host = "example.com"
	task.Checks = []string{"tcp", "tcp"}
	if err := task.Validate(); err == nil {
		t.Fatal("accepted duplicate checks")
	}
	// 名字写错时要说清是检测项本身不对，而不是让叶子任务报「任务类型须为 tcp 或 route」。
	task.Checks = []string{"latency"}
	if err := task.Validate(); err == nil || !strings.Contains(err.Error(), "检测内容须为") {
		t.Fatalf("check name error = %v", err)
	}
	task.Checks = []string{"tcp", "route"}
	task.Checks = []string{"route"}
	task.RouteInterval = 30
	if err := task.Validate(); err == nil {
		t.Fatal("accepted excessive route frequency")
	}
}

// TestObservationRedact pins the masking both the panel and the MCP endpoint
// apply: bound node ids and unmasked hop addresses must never leave the server.
func TestObservationRedact(t *testing.T) {
	fresh := Observation{
		Task: Task{ID: "t1", Kind: "route", NodeIDs: []string{"n1", "n2"}},
		Result: Result{
			Status: "ok", Route: "CN2GT",
			Hops: []Hop{{TTL: 1, Address: "59.43.1.2", ASN: "4134"}, {TTL: 2, Address: "202.97.5.6", ASN: "as4134"}},
		},
	}
	out := fresh.Redact()
	if out.Task.NodeIDs != nil {
		t.Fatalf("node bindings leaked: %v", out.Task.NodeIDs)
	}
	if out.Result.Hops[0].Address != "59.43.*.*" || out.Result.Hops[1].Address != "202.97.*.*" {
		t.Fatalf("hop addresses not masked: %v", out.Result.Hops)
	}
	if out.Result.Hops[0].ASN != "AS4134" || out.Result.Hops[1].ASN != "AS4134" {
		t.Fatalf("asn not normalised: %v", out.Result.Hops)
	}
	if out.Result.Route != "CN2GT" {
		t.Fatalf("a successful probe keeps its label: %v", out.Result.Route)
	}
	stale := Observation{Result: Result{Status: "ok", Route: "CN2GIA"}, Stale: true}
	if stale.Redact().Result.Route != "未知" {
		t.Fatalf("a stale probe must not show a label: %v", stale.Redact().Result.Route)
	}
	failed := Observation{Result: Result{Status: "timeout", Route: "CN2GIA"}}
	if failed.Redact().Result.Route != "未知" {
		t.Fatalf("a failed probe must not show a label: %v", failed.Redact().Result.Route)
	}
}

func TestMetricsAddressFamilyCapability(t *testing.T) {
	valid := Metrics{MemoryTotal: 1}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	if !valid.AllowsFamily("ipv4") || !valid.AllowsFamily("ipv6") {
		t.Fatal("unreported capabilities must stay backwards compatible")
	}
	valid.IPFamilies = []string{}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	if valid.AllowsFamily("ipv4") || valid.AllowsFamily("ipv6") {
		t.Fatal("empty capability must allow neither family")
	}
	valid.IPFamilies = []string{"ipv6"}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	if valid.AllowsFamily("ipv4") || !valid.AllowsFamily("ipv6") {
		t.Fatal("reported IPv6-only capability filtered incorrectly")
	}
	for _, bad := range [][]string{{"ipv4", "ipv4"}, {"ipv4", "ipv6", "ipv4"}, {"ipv5"}} {
		valid.IPFamilies = bad
		if err := valid.Validate(); err == nil {
			t.Fatalf("accepted invalid families %v", bad)
		}
	}
}
