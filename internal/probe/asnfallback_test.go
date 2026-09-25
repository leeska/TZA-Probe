package probe

import (
	"errors"
	"net"
	"testing"
	"time"

	"github.com/nxtrace/NTrace-core/ipgeo"
	"github.com/nxtrace/NTrace-core/trace"

	"github.com/leeska/TZA-Probe/internal/model"
)

func TestEffectiveGeoProvider(t *testing.T) {
	// An empty config used to mean the upstream default, whose v3 transport
	// cannot work in-process: every lookup failed and the path came back with
	// no ASN at all (two live nodes ran that way for three days).
	if got := EffectiveGeoProvider(""); got != "IPAPI.com" {
		t.Fatalf("empty: %q", got)
	}
	if got := EffectiveGeoProvider("  "); got != "IPAPI.com" {
		t.Fatalf("blank: %q", got)
	}
	// An explicit choice is never overridden, not even to disable lookups.
	for _, want := range []string{"IPAPI.com", "disable-geoip", "NextTrace-API", "IPInfo.io"} {
		if got := EffectiveGeoProvider(" " + want + " "); got != want {
			t.Fatalf("%s: %q", want, got)
		}
	}
}

// The recorded 重庆 联通 trace flipped between two labels from the same node and
// the same target, one hour apart (2026-09-25): at 08:26 the hop on the 9929
// line answered from a 210.14.x address the geo source left unresolved, at 09:26
// it answered from 218.105.x and resolved as AS9929. The rest of both paths —
// the 联通国际 entry, the timeouts, the 169 tail — was identical, so the flip
// was a missing ASN, not a different line.
//
// The addresses below sit in the blocks the registry attributes to CUII; the
// stored form of the unresolved one is masked to 210.14.*.*, which is why a
// prefix-table entry cannot name it.
func TestMapTraceResultNamesUnicomInternationalRangesWithoutGeoSource(t *testing.T) {
	recorded := [][]trace.Hop{
		{{Success: true, Address: &net.IPAddr{IP: net.ParseIP("45.78.1.1")}, TTL: 3, RTT: 900 * time.Microsecond, Geo: &ipgeo.IPGeoData{Asnumber: "25820"}}},
		{{Success: true, Address: &net.IPAddr{IP: net.ParseIP("162.219.90.5")}, TTL: 6, RTT: 900 * time.Microsecond, Geo: &ipgeo.IPGeoData{Asnumber: "10099"}}},
		{{Success: true, Address: &net.IPAddr{IP: net.ParseIP("210.14.195.13")}, TTL: 7, RTT: 900 * time.Microsecond}},
		{{Success: false, Error: errors.New("hop timeout"), TTL: 8}},
		{{Success: true, Address: &net.IPAddr{IP: net.ParseIP("210.14.195.14")}, TTL: 9, RTT: 900 * time.Microsecond}},
		{{Success: true, Address: &net.IPAddr{IP: net.ParseIP("219.158.16.1")}, TTL: 11, RTT: 900 * time.Microsecond, Geo: &ipgeo.IPGeoData{Asnumber: "4837"}}},
		{{Success: true, Address: &net.IPAddr{IP: net.ParseIP("58.144.112.204")}, TTL: 18, RTT: 900 * time.Microsecond, Geo: &ipgeo.IPGeoData{Asnumber: "4837"}}},
	}
	out := mapTraceResult(&trace.Result{Hops: recorded}, nil, net.ParseIP("58.144.112.204"))
	if len(out.Hops) != 7 {
		t.Fatalf("hops: %+v", out.Hops)
	}
	if out.Hops[2].ASN != "AS9929" || out.Hops[2].Address != "210.14.*.*" {
		t.Fatalf("unresolved 9929 hop must name CUII: %+v", out.Hops[2])
	}
	if got := model.RouteLabel(out.Hops); got != "10099 → 9929" {
		t.Fatalf("recorded 重庆 trace: %s", got)
	}
	// The same path with the resolved address keeps behaving as before.
	recorded[2] = []trace.Hop{{Success: true, Address: &net.IPAddr{IP: net.ParseIP("210.14.195.13")}, TTL: 7, RTT: 900 * time.Microsecond, Geo: &ipgeo.IPGeoData{Asnumber: "9929"}}}
	if got := model.RouteLabel(mapTraceResult(&trace.Result{Hops: recorded}, nil, net.ParseIP("58.144.112.204")).Hops); got != "10099 → 9929" {
		t.Fatalf("resolved 9929 hop: %s", got)
	}
}

// A hop that lands outside the carrier ranges stays unresolved: naming a line
// from an address that is not the carrier's would be worse than 未知. The
// 210.14.0.0/16 case is the reason the fallback exists at all — the registry
// splits it between PLDT and CUII.
func TestCarrierANSRangesOnlyNameTheirOwnBlocks(t *testing.T) {
	for addr, want := range map[string]string{
		"210.14.195.13":  "AS9929",
		"210.14.249.9":   "AS9929",
		"218.105.200.1":  "AS9929",
		"210.51.7.7":     "AS9929",
		"2408:8120:2::9": "AS9929",
		// The Unicom-registered, unannounced part of 210.14.0.0/16: these are
		// the addresses that answered as the CUII edge on the 重庆 trace while
		// no ASN source could name them.
		"210.14.178.234":   "AS9929",
		"210.14.165.37":    "AS9929",
		"210.14.178.106":   "AS9929",
		"210.14.191.255":   "AS9929",
		"118.26.144.7":     "AS10099",
		"118.26.158.7":     "AS10099",
		"162.219.33.7":     "AS10099",
		"202.77.22.7":      "AS10099",
		"203.160.70.7":     "AS10099",
		"103.239.176.7":    "AS10099",
		"2401:8a00:1:7::5": "AS10099",
		"210.14.1.5":       "",
		"210.14.31.255":    "",
		"210.14.128.9":     "",
		"210.14.150.9":     "",
		"210.14.159.255":   "",
		"118.26.16.7":      "",
		"59.43.245.1":      "",
		"203.0.113.9":      "",
		"2408:8001:2::1":   "",
	} {
		if got := carrierASN(net.ParseIP(addr)); got != want {
			t.Fatalf("%s: %q, want %q", addr, got, want)
		}
	}
	// An operator-installed NextTrace binary reports hops in the same raw form,
	// so that path needs the fallback too.
	hops, _, _ := parse([]byte("7|210.14.195.13||1.2|\n8|219.158.16.1||2.3|4837\n"), "58.144.112.204")
	if len(hops) != 2 || hops[0].ASN != "AS9929" || hops[0].Address != "210.14.*.*" || hops[1].ASN != "AS4837" {
		t.Fatalf("parsed hops: %+v", hops)
	}
}
