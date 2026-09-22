package model

import (
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
	for _, c := range []struct {
		hops []Hop
		want string
	}{
		{[]Hop{{ASN: "AS4134", Address: "202.97.1.*"}}, "163"},
		{[]Hop{{ASN: "AS4134"}}, "163"},
		{[]Hop{{Address: "202.97.1.*"}}, "163"},
		{[]Hop{{ASN: "AS23764", Address: "203.0.113.*"}}, "CTG"},
		{[]Hop{{ASN: "AS58807"}}, "CMIN2"},
		// The tool regularly returns no ASN at all. Prefixes carry those hops.
		{[]Hop{{Address: "210.51.*.*"}}, "9929"},
		{[]Hop{{Address: "219.158.*.*"}}, "4837"},
		{[]Hop{{Address: "240e:1234:****"}}, "163"},
		{[]Hop{{Address: "223.120.*.*"}}, "CMI"},
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
	task.Checks = []string{"route"}
	task.RouteInterval = 30
	if err := task.Validate(); err == nil {
		t.Fatal("accepted excessive route frequency")
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
