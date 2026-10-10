package probe

import (
	"context"
	"errors"
	"net"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/nxtrace/NTrace-core/ipgeo"
	"github.com/nxtrace/NTrace-core/trace"

	"github.com/leeska/TZA-Probe/internal/model"
)

// Embedded runs the route trace inside the agent process through NTrace-core,
// the tracer that powers the NextTrace CLI. Operators no longer have to install
// a binary on every node: the same code that NextTrace ships runs here with the
// same fixed parameters the external call used.
//
// NTrace-core is GPL-3.0 while the rest of this repository is MIT; see NOTICE.
//
// defaultGeoProvider is the ASN/geo source used when the node config names
// none. The upstream default, "NextTrace-API", cannot work here: its v3
// transport needs the WebSocket connection the NextTrace CLI's own supervisor
// opens, and inside a long-running process that connection is nil, so every
// lookup fails in microseconds and the whole path comes back without an ASN.
// Two live nodes shipped that way and resolved 0 of 14336 and 0 of 7874 hop
// addresses over three days — every label on them fell back to the prefix
// table. IPAPI.com needs no key and answers in tens of milliseconds; on the
// nodes where it is configured, 60–75% of answering hops resolve an ASN.
const defaultGeoProvider = "IPAPI.com"

// EffectiveGeoProvider resolves the configured source to the one actually
// queried. An explicit provider (including "disable-geoip") is used verbatim.
// The tracer and the node's startup log both read the source through it, so
// what the log says is what the run queried.
func EffectiveGeoProvider(configured string) string {
	if strings.TrimSpace(configured) == "" {
		return defaultGeoProvider
	}
	return strings.TrimSpace(configured)
}

type Embedded struct {
	// GeoProvider selects the ASN/geo source for hop metadata, for example
	// "IPAPI.com" (the default), "NextTrace-API" or "disable-geoip".
	// Empty selects defaultGeoProvider.
	GeoProvider string
	// Method selects the probe protocol: "icmp" (default), "tcp" or "udp".
	// ICMP is what the NextTrace CLI uses when no method flag is given, and it
	// is the method that keeps returning TTL-exceeded hops on filtered access
	// networks. The operator can switch without touching code.
	Method string
}

func (e Embedded) traceMethod() trace.Method {
	switch strings.ToLower(strings.TrimSpace(e.Method)) {
	case "tcp":
		return trace.TCPTrace
	case "udp":
		return trace.UDPTrace
	default:
		return trace.ICMPTrace
	}
}

// traceBudget caps one embedded trace. The caller still owns the outer job
// deadline; this only keeps a trace that never reports a stop reason bounded.
const traceBudget = 45 * time.Second

// traceOutcome is what either tracer hands back to the job runner.
type traceOutcome struct {
	Hops     []model.Hop
	Received int
	Latency  *float64
	// Reason is empty when the destination answered, otherwise one of
	// timeout, permission, tool_failed.
	Reason string
}

// Trace resolves one route probe. The caller owns the context deadline.
func (e Embedded) Trace(ctx context.Context, ip net.IP, port int) traceOutcome {
	if ip == nil {
		return traceOutcome{Hops: []model.Hop{}, Reason: "dns"}
	}
	session := trace.CachedGeoSourceSession(ipgeo.GetSourceDescriptorSession(EffectiveGeoProvider(e.GeoProvider)))
	// Bound the run below the caller's job budget so a trace that never
	// reports its own stop reason still returns the hops it collected.
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) > traceBudget {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, traceBudget)
		defer cancel()
	}
	conf := trace.Config{
		Context: ctx,
		OSType:  osType(),
		// BeginHop must be at least 1. The tracer otherwise sends its first
		// probe with TTL 0, which is not a real hop and which Linux rejects
		// outright (setsockopt: invalid argument), failing the whole trace.
		BeginHop:        1,
		DstIP:           ip,
		DstPort:         port,
		// Long paths exist: a Tokyo probe reached a 四川 mobile target at TTL
		// 25, one past the old cap, so the trace never saw the destination and
		// the panel showed 未知. 30 covers those with room to spare.
		MaxHops:         30,
		NumMeasurements: 1,
		MaxAttempts:     1,
		// More probes in flight keeps a filtered path inside the budget: with
		// one measurement per hop, two parallel requests spent the whole 45s
		// and still stopped short of the destination.
		ParallelRequests:   6,
		Timeout:            1500 * time.Millisecond,
		DisableMPLS:        true,
		Lang:               "cn",
		IPGeoSource:        session.Source,
		RefreshIPGeoSource: session.Refresh,
	}
	res, err := trace.TracerouteWithContext(ctx, e.traceMethod(), conf)
	return mapTraceResult(res, err, ip)
}

// mapTraceResult converts an NTrace-core result into the project's wire model.
// Hop addresses are masked exactly like the external tool's output so the panel
// never publishes a full path.
func mapTraceResult(res *trace.Result, err error, target net.IP) traceOutcome {
	out := traceOutcome{Hops: []model.Hop{}}
	if res != nil {
		// NTrace-core indexes Hops by TTL-1 (updateHop uses ttl-1), so the
		// slice index is not the TTL the hop was observed at.
		for index, attempts := range res.Hops {
			if len(attempts) == 0 {
				continue
			}
			hop := winningAttempt(attempts)
			out.Hops = append(out.Hops, model.Hop{
				TTL:     index + 1,
				Address: hopAddress(hop),
				ASN:     asnOrCarrierRange(hopASN(hop), hopRawAddress(hop)),
				RTT:     hopRTT(hop),
			})
			// The tool's own stop reason is the primary signal, but a hop
			// that answers from the target address is proof on its own.
			if target != nil && hop.Success && hop.Address != nil && hop.Address.String() == target.String() {
				out.Received = 1
				ms := hopRTT(hop)
				out.Latency = &ms
			}
		}
		sort.Slice(out.Hops, func(i, j int) bool { return out.Hops[i].TTL < out.Hops[j].TTL })
		if out.Received == 0 && res.StopReason != nil && res.StopReason.Reason == trace.StopReasonDestination {
			out.Received = 1
			if hop, ok := destinationHop(res); ok {
				ms := hopRTT(hop)
				out.Latency = &ms
			}
		}
	}
	if out.Received > 0 {
		return out
	}
	switch {
	case errors.Is(err, syscall.EPERM), errors.Is(err, syscall.EACCES):
		out.Reason = "permission"
	case errors.Is(err, context.DeadlineExceeded), len(out.Hops) > 0:
		out.Reason = "timeout"
	default:
		out.Reason = "tool_failed"
	}
	return out
}

// winningAttempt prefers a successful probe and otherwise reports the first
// attempt so a timed-out hop still shows up as a masked star.
func winningAttempt(attempts []trace.Hop) trace.Hop {
	for _, hop := range attempts {
		if hop.Success && hop.Address != nil {
			return hop
		}
	}
	return attempts[0]
}

func destinationHop(res *trace.Result) (trace.Hop, bool) {
	// StopReason.Hop is a TTL; the slice is indexed by TTL-1.
	ttl := res.StopReason.Hop
	index := ttl - 1
	if index < 0 || index >= len(res.Hops) || len(res.Hops[index]) == 0 {
		return trace.Hop{}, false
	}
	return winningAttempt(res.Hops[index]), true
}

func hopRawAddress(hop trace.Hop) string {
	if hop.Address == nil {
		return ""
	}
	return hop.Address.String()
}

func hopAddress(hop trace.Hop) string {
	if hop.Address == nil {
		return "*"
	}
	if ip := net.ParseIP(hop.Address.String()); ip != nil {
		return model.MaskAddress(ip.String())
	}
	return "*"
}

func hopASN(hop trace.Hop) string {
	if hop.Geo == nil {
		return ""
	}
	return model.NormalizeASN(hop.Geo.Asnumber)
}

func hopRTT(hop trace.Hop) float64 {
	if hop.RTT <= 0 {
		return 0
	}
	return float64(hop.RTT.Microseconds()) / 1000
}

// osType mirrors the value the NextTrace CLI passes for the running platform.
func osType() int {
	switch runtime.GOOS {
	case "darwin":
		return 1
	case "windows":
		return 2
	default:
		return 3
	}
}
