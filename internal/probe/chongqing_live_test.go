package probe

import (
	"fmt"
	"testing"

	"github.com/leeska/TZA-Probe/internal/model"
)

// Regression fixture: the raw nexttrace output a node actually produced for
// 重庆 58.144.112.204, captured on us-bwh-dc9 — the run whose 9929-side edge answered from
// 210.14.165.37 / 210.14.178.106, addresses the CLI's own geo source left
// unnamed. Fields are the ones nexttrace --raw emits: ttl|address|mpls|rtt|asn.
const liveTraceChongqing = `1|10.29.0.1||0.5|
2|*||0.5|
3|45.78.0.78||0.5|25820
4|142.0.32.54||0.5|25820
5|118.26.151.29||0.5|
6|162.219.85.173||0.5|10099
7|210.14.165.37||0.5|
8|*||0.5|
9|*||0.5|
10|210.14.178.106||0.5|
11|219.158.45.105||0.5|4837
12|219.158.106.229||0.5|4837
13|58.144.255.86||0.5|4837
14|*||0.5|
19|58.144.112.204||0.5|4837
`

func TestChongqingTraceNamesThe9929EdgeWithoutGeoSource(t *testing.T) {
	hops, _, _ := parse([]byte(liveTraceChongqing), "58.144.112.204")
	for _, h := range hops {
		fmt.Printf("ttl=%-3d addr=%-16s asn=%s\n", h.TTL, h.Address, h.ASN)
	}
	for _, h := range hops {
		if h.Address == "210.14.165.37" || h.Address == "210.14.178.106" {
			if h.ASN != "AS9929" {
				t.Fatalf("hop %d (%s): ASN=%q，期望 AS9929", h.TTL, h.Address, h.ASN)
			}
		}
	}
	label := model.RouteLabel(hops)
	fmt.Printf("标签 = %s\n", label)
	if label != "10099 → 9929" {
		t.Fatalf("标签=%q，期望 \"10099 → 9929\"", label)
	}
}
