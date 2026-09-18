// Package probe executes a closed set of network measurements. No shell,
// downloadable code, server-supplied executable, or arbitrary flags are accepted.
package probe

import (
	"context"
	"errors"
	"math"
	"net"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

type Runner struct{ Nexttrace string }

func (r Runner) Run(ctx context.Context, j model.Job) model.Result {
	result := model.Result{JobID: j.ID, Status: "failed", Route: "未知", Hops: []model.Hop{}}
	if err := j.Task.Validate(); err != nil {
		return result
	}
	if j.Task.Kind == "route" && r.Nexttrace == "" {
		result.Status = "unavailable"
		result.Reason = "tool_missing"
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	network := "ip4"
	tcp := "tcp4"
	if j.Task.Family == "ipv6" {
		network = "ip6"
		tcp = "tcp6"
	}
	addresses, err := net.DefaultResolver.LookupIP(ctx, network, j.Task.Host)
	if err != nil || len(addresses) == 0 {
		result.Reason = "dns"
		return result
	}
	ip := addresses[0].String()
	// Fake-IP DNS proxies commonly synthesize RFC2544 benchmarking addresses.
	// A connection/trace to that proxy is not a measurement of the configured host.
	if syntheticAddress(addresses[0]) {
		result.Reason = "synthetic_dns"
		return result
	}
	if j.Task.Kind == "tcp" {
		result.Sent = 3
		var total float64
		for i := 0; i < 3; i++ {
			start := time.Now()
			dialer := net.Dialer{Timeout: 3 * time.Second}
			conn, err := dialer.DialContext(ctx, tcp, net.JoinHostPort(ip, strconv.Itoa(j.Task.Port)))
			if err == nil {
				conn.Close()
				result.Received++
				total += float64(time.Since(start).Microseconds()) / 1000
			}
		}
		if result.Received > 0 {
			ms := total / float64(result.Received)
			result.Latency = &ms
			result.Status = "ok"
		} else {
			result.Status = "timeout"
			result.Reason = "connect_failed"
		}
		return result
	}

	family := "-4"
	if j.Task.Family == "ipv6" {
		family = "-6"
	}
	cmd := exec.CommandContext(ctx, r.Nexttrace, "--raw", "-n", "-C", "-M", "-q", "1", "--parallel-requests", "2", "--max-attempts", "1", "-m", "24", "--timeout", "1500", "-T", "-p", strconv.Itoa(j.Task.Port), family, ip)
	// Limit memory independently of command behavior. CommandContext bounds runtime.
	output := &limitedBuffer{remaining: 128 << 10}
	cmd.Stdout = output
	stderr := &limitedBuffer{remaining: 8192}
	cmd.Stderr = stderr
	cmd.WaitDelay = 2 * time.Second
	_ = cmd.Run()
	result.Hops, result.Received, result.Latency = parse(output.data, ip)
	result.Sent = 1
	if result.Received > 0 {
		result.Status = "ok"
		result.Route = model.RouteLabel(result.Hops)
	} else if errors.Is(ctx.Err(), context.DeadlineExceeded) || len(result.Hops) > 0 {
		result.Status = "timeout"
		result.Reason = "timeout"
	} else {
		result.Status = "failed"
		result.Reason = "tool_failed"
		if strings.Contains(strings.ToLower(string(stderr.data)), "operation not permitted") || strings.Contains(strings.ToLower(string(stderr.data)), "permission denied") {
			result.Reason = "permission"
		}
	}
	return result
}

type limitedBuffer struct {
	data      []byte
	remaining int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	take := min(n, b.remaining)
	b.data = append(b.data, p[:take]...)
	b.remaining -= take
	return n, nil
}
func parse(data []byte, target string) ([]model.Hop, int, *float64) {
	hops := map[int]model.Hop{}
	reached := 0
	var latency *float64
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Split(strings.TrimSpace(line), "|")
		if len(f) < 5 {
			continue
		}
		ttl, err := strconv.Atoi(strings.TrimSpace(f[0]))
		if err != nil || ttl < 1 || ttl > 30 {
			continue
		}
		address := net.ParseIP(strings.Trim(strings.TrimSpace(f[1]), "[]()"))
		if address == nil {
			if _, ok := hops[ttl]; !ok {
				hops[ttl] = model.Hop{TTL: ttl, Address: "*"}
			}
			continue
		}
		ms, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(f[3]), "ms")), 64)
		if err != nil || math.IsNaN(ms) || math.IsInf(ms, 0) || ms < 0 || ms > 60000 {
			ms = 0
		}
		h := model.Hop{TTL: ttl, Address: model.MaskAddress(address.String()), ASN: model.NormalizeASN(f[4]), RTT: ms}
		if previous, ok := hops[ttl]; !ok || previous.Address == "*" || ms < previous.RTT {
			hops[ttl] = h
		}
		if address.Equal(net.ParseIP(target)) {
			reached = 1
			v := ms
			latency = &v
		}
	}
	out := []model.Hop{}
	for _, h := range hops {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TTL < out[j].TTL })
	return out, reached, latency
}

func syntheticAddress(ip net.IP) bool {
	v := ip.To4()
	return v != nil && v[0] == 198 && (v[1] == 18 || v[1] == 19)
}
