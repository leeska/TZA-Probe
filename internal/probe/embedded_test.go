package probe

import (
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/nxtrace/NTrace-core/ipgeo"
	"github.com/nxtrace/NTrace-core/trace"

	"github.com/leeska/TZA-Probe/internal/model"
)

func TestMapTraceResultKeepsEvidenceAndMasksAddresses(t *testing.T) {
	res := &trace.Result{
		Hops: [][]trace.Hop{
			nil,
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
	if out.Hops[1].Address != "*" || out.Hops[1].RTT != 0 {
		t.Fatalf("timed out hop: %+v", out.Hops[1])
	}
	if out.Hops[2].ASN != "AS4809" || out.Hops[2].Address != "59.43.*.*" || out.Hops[2].RTT != 120.4 {
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
	partial := &trace.Result{Hops: [][]trace.Hop{nil, {{Success: false, TTL: 1}}}}
	if got := mapTraceResult(partial, context.DeadlineExceeded, nil); got.Reason != "timeout" || len(got.Hops) != 1 {
		t.Fatalf("partial trace: %+v", got)
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
