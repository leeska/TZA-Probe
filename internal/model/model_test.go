package model

import (
	"testing"
	"time"
)

func TestBillingCycleClampAndLeapYear(t *testing.T) {
	cases := []struct {
		now        string
		day        int
		start, end string
	}{
		{"2026-02-27T12:00:00Z", 31, "2026-01-31T00:00:00Z", "2026-02-28T00:00:00Z"},
		{"2026-02-28T00:00:00Z", 31, "2026-02-28T00:00:00Z", "2026-03-31T00:00:00Z"},
		{"2024-02-29T10:00:00Z", 31, "2024-02-29T00:00:00Z", "2024-03-31T00:00:00Z"},
		{"2026-01-01T00:00:00Z", 1, "2026-01-01T00:00:00Z", "2026-02-01T00:00:00Z"},
	}
	for _, c := range cases {
		now, _ := time.Parse(time.RFC3339, c.now)
		start, end := Cycle(now, c.day)
		if start.Format(time.RFC3339) != c.start || end.Format(time.RFC3339) != c.end {
			t.Fatalf("%s: %s -> %s", c.now, start, end)
		}
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
	gia := []Hop{{ASN: "AS4809", Address: "59.43.1.*"}, {ASN: "AS4809", Address: "59.43.2.*"}}
	if got := RouteLabel(gia); got != "CN2GIA" {
		t.Fatal(got)
	}
	gt := []Hop{{ASN: "AS4134", Address: "202.97.1.*"}, {ASN: "AS4809", Address: "59.43.2.*"}}
	if got := RouteLabel(gt); got != "163 → CN2GT" {
		t.Fatal(got)
	}
	// A live 电信 trace: the CN2 hops came back without an ASN, and 163 shows
	// up both before and after them. The address prefix still proves CN2 GT.
	unresolved := []Hop{
		{ASN: "AS25820", Address: "45.78.*.*"},
		{ASN: "AS4134", Address: "218.30.*.*"},
		{Address: "59.43.*.*"},
		{Address: "59.43.*.*"},
		{ASN: "AS4134", Address: "110.190.*.*"},
	}
	if got := RouteLabel(unresolved); got != "163 → CN2GT" {
		t.Fatal(got)
	}
	// A direct CN2 entry with no 163 hop in front of it is GIA.
	direct := []Hop{{ASN: "AS25820", Address: "45.78.*.*"}, {Address: "59.43.*.*"}}
	if got := RouteLabel(direct); got != "CN2GIA" {
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
