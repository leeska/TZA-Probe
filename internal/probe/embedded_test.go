package probe

import (
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nxtrace/NTrace-core/ipgeo"
	"github.com/nxtrace/NTrace-core/trace"

	"github.com/leeska/TZA-Probe/internal/model"
)

func TestMapTraceResultKeepsEvidenceAndMasksAddresses(t *testing.T) {
	// NTrace-core indexes Hops by TTL-1 and stores the TTL in StopReason.
	res := &trace.Result{
		Hops: [][]trace.Hop{
			{{Success: true, Address: &net.IPAddr{IP: net.ParseIP("10.29.1.1")}, TTL: 1, RTT: 900 * time.Microsecond}},
			{{Success: false, Error: errors.New("hop timeout"), TTL: 2}},
			{{Success: true, Address: &net.IPAddr{IP: net.ParseIP("59.43.245.1")}, TTL: 3, RTT: 120400 * time.Microsecond, Geo: &ipgeo.IPGeoData{Asnumber: "4809", Owner: "CN2"}}},
		},
		StopReason: &trace.StopReason{Hop: 3, Reason: trace.StopReasonDestination},
	}
	out := mapTraceResult(res, nil, net.ParseIP("59.43.245.1"))
	if out.Reason != "" || out.Received != 1 || out.Latency == nil {
		t.Fatalf("destination result: %+v", out)
	}
	if len(out.Hops) != 3 {
		t.Fatalf("hops: %+v", out.Hops)
	}
	if out.Hops[0].TTL != 1 || out.Hops[0].Address != "10.29.*.*" {
		t.Fatalf("masked hop: %+v", out.Hops[0])
	}
	if out.Hops[1].TTL != 2 || out.Hops[1].Address != "*" || out.Hops[1].RTT != 0 {
		t.Fatalf("timed out hop: %+v", out.Hops[1])
	}
	if out.Hops[2].TTL != 3 || out.Hops[2].ASN != "AS4809" || out.Hops[2].Address != "59.43.*.*" || out.Hops[2].RTT != 120.4 {
		t.Fatalf("cn2 hop: %+v", out.Hops[2])
	}
	if *out.Latency != 120.4 {
		t.Fatalf("latency: %v", *out.Latency)
	}
	if got := model.RouteLabel(out.Hops); got != "CN2GIA" {
		t.Fatalf("route label: %s", got)
	}
}

func TestMapTraceResultReasons(t *testing.T) {
	empty := mapTraceResult(&trace.Result{Hops: [][]trace.Hop{nil}}, nil, nil)
	if empty.Reason != "tool_failed" || empty.Hops == nil {
		t.Fatalf("empty result: %+v", empty)
	}
	if got := mapTraceResult(nil, nil, nil); got.Reason != "tool_failed" {
		t.Fatalf("nil result: %+v", got)
	}
	if got := mapTraceResult(nil, syscall.EPERM, nil); got.Reason != "permission" {
		t.Fatalf("permission: %+v", got)
	}
	if got := mapTraceResult(nil, context.DeadlineExceeded, nil); got.Reason != "timeout" {
		t.Fatalf("deadline: %+v", got)
	}
	partial := &trace.Result{Hops: [][]trace.Hop{{{Success: false, TTL: 1}}}}
	if got := mapTraceResult(partial, context.DeadlineExceeded, nil); got.Reason != "timeout" || len(got.Hops) != 1 || got.Hops[0].TTL != 1 {
		t.Fatalf("partial trace: %+v", got)
	}
}

// A destination that only shows up as the last answered hop still counts as
// reached, even when the tracer never wrote its own stop reason.
func TestMapTraceResultTreatsTargetHopAsArrival(t *testing.T) {
	res := &trace.Result{Hops: [][]trace.Hop{
		{{Success: false, TTL: 1}},
		{{Success: true, Address: &net.IPAddr{IP: net.ParseIP("203.0.113.9")}, TTL: 2, RTT: 12 * time.Millisecond}},
	}}
	out := mapTraceResult(res, context.DeadlineExceeded, net.ParseIP("203.0.113.9"))
	if out.Received != 1 || out.Latency == nil || *out.Latency != 12 || out.Reason != "" {
		t.Fatalf("target hop not treated as arrival: %+v", out)
	}
	if out.Hops[1].TTL != 2 {
		t.Fatalf("hop ttl: %+v", out.Hops[1])
	}
}

// The embedded tracer needs raw sockets, so this only runs as root.
func TestEmbeddedTracesLoopback(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("raw socket probe requires root")
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	p, _ := strconv.Atoi(port)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	out := (Embedded{GeoProvider: "disable-geoip"}).Trace(ctx, net.ParseIP("127.0.0.1"), p)
	if out.Received != 1 || len(out.Hops) == 0 || out.Latency == nil {
		t.Fatalf("loopback trace: %+v", out)
	}
	if out.Hops[0].Address != "127.0.*.*" {
		t.Fatalf("loopback hop must stay masked: %+v", out.Hops[0])
	}
}

// A route job with no tracer configured stays unavailable instead of silently
// reporting an empty measurement.
func TestRunnerWithoutTracerStaysUnavailable(t *testing.T) {
	task := model.Task{Name: "loopback", Kind: "route", Host: "127.0.0.1", Port: 80, Family: "ipv4", Region: "上海", Carrier: "电信", Interval: 900}
	got := (Runner{}).Run(context.Background(), model.Job{Task: task})
	if got.Status != "unavailable" || got.Reason != "tool_missing" {
		t.Fatalf("missing tracer: %+v", got)
	}
}

// ICMP is the default because it matches the NextTrace CLI and keeps returning
// TTL-exceeded hops on access networks that drop TCP probe replies.
func TestTraceMethodSelection(t *testing.T) {
	for input, want := range map[string]trace.Method{
		"": trace.ICMPTrace, "icmp": trace.ICMPTrace, "ICMP": trace.ICMPTrace,
		" icmp ": trace.ICMPTrace, "tcp": trace.TCPTrace, "udp": trace.UDPTrace,
		"nonsense": trace.ICMPTrace,
	} {
		if got := (Embedded{Method: input}).traceMethod(); got != want {
			t.Fatalf("%q: %v", input, got)
		}
	}
}

// TestEmbeddedLiveTrace is the on-node acceptance check for the embedded
// tracer. It only runs when explicitly asked, because it needs root and a host
// with a direct internet path:
//
//	sudo TZA_LIVE_TRACE_TARGET=1.1.1.1 \
//	  TZA_LIVE_TRACE_GEO=IPAPI.com go test ./internal/probe/ -run LiveTrace -v
//
// Do not run this on a host whose egress goes through a local proxy or
// Fake-IP resolver: the hops would describe the proxy, not the operator line.
func TestEmbeddedLiveTrace(t *testing.T) {
	target := os.Getenv("TZA_LIVE_TRACE_TARGET")
	if target == "" {
		t.Skip("set TZA_LIVE_TRACE_TARGET=<ip> to run a live trace")
	}
	if os.Geteuid() != 0 {
		t.Skip("raw socket probe requires root")
	}
	ip := net.ParseIP(target)
	if ip == nil || ip.To4() == nil {
		t.Fatalf("TZA_LIVE_TRACE_TARGET must be an IPv4 literal, got %q", target)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	method := os.Getenv("TZA_LIVE_TRACE_METHOD")
	if method == "" {
		method = "tcp"
	}
	out := (Embedded{GeoProvider: os.Getenv("TZA_LIVE_TRACE_GEO"), Method: method}).Trace(ctx, ip, 443)
	t.Logf("status: received=%d latency=%v reason=%q", out.Received, out.Latency, out.Reason)
	for _, h := range out.Hops {
		t.Logf("  %2d  %-16s %-10s %.1f ms", h.TTL, h.Address, h.ASN, h.RTT)
	}
	t.Logf("route label: %s", model.RouteLabel(out.Hops))

	if len(out.Hops) < 3 {
		t.Fatalf("live trace produced %d hops; expected a real path", len(out.Hops))
	}
	for _, h := range out.Hops {
		if h.Address == "198.18.*.*" || h.Address == "198.19.*.*" {
			t.Fatalf("hop %d is a Fake-IP address; this host is not a valid probe point", h.TTL)
		}
		if strings.HasPrefix(h.Address, "10.") || strings.HasPrefix(h.Address, "127.") {
			t.Fatalf("hop %d is private (%s); the trace never left the local network", h.TTL, h.Address)
		}
	}
	if out.Received != 1 {
		t.Fatalf("target %s did not answer: %+v", target, out)
	}
	last := out.Hops[len(out.Hops)-1]
	if last.Address != model.MaskAddress(ip.String()) {
		t.Fatalf("last hop is %s, expected the target %s", last.Address, model.MaskAddress(ip.String()))
	}
}
