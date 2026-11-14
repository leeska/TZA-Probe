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

	"github.com/leeska/TZA-ControlCenter/internal/model"
)

// Runner executes one job. Route tasks run through Embedded unless the operator
// points Nexttrace at a binary, in which case that binary is used instead.
type Runner struct {
	Nexttrace string
	Embedded  *Embedded
	Registry  *Registry
}

func (r Runner) Run(ctx context.Context, j model.Job) model.Result {
	if r.Registry != nil {
		return r.Registry.Run(ctx, j)
	}
	return r.DefaultRegistry().Run(ctx, j)
}

// DefaultRegistry builds the built-in executor registry for TCP, Route, and Unlock.
func (r Runner) DefaultRegistry() *Registry {
	return NewRegistry(
		TCPExecutor{},
		RouteExecutor{Nexttrace: r.Nexttrace, Embedded: r.Embedded},
		UnlockExecutor{},
	)
}

// execTrace runs an operator-installed NextTrace binary and parses --raw output.
// No shell and no caller-supplied flags are involved.
func execTrace(ctx context.Context, binary string, j model.Job, ip string) traceOutcome {
	family := "-4"
	if j.Task.Family == "ipv6" {
		family = "-6"
	}
	cmd := exec.CommandContext(ctx, binary, "--raw", "-n", "-C", "-M", "-q", "1", "--parallel-requests", "2", "--max-attempts", "1", "-m", "30", "--timeout", "1500", "-T", "-p", strconv.Itoa(j.Task.Port), family, ip)
	// Limit memory independently of command behavior. CommandContext bounds runtime.
	output := &limitedBuffer{remaining: 128 << 10}
	cmd.Stdout = output
	stderr := &limitedBuffer{remaining: 8192}
	cmd.Stderr = stderr
	cmd.WaitDelay = 2 * time.Second
	_ = cmd.Run()
	hops, received, latency := parse(output.data, ip)
	out := traceOutcome{Hops: hops, Received: received, Latency: latency}
	if received > 0 {
		return out
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || len(hops) > 0 {
		out.Reason = "timeout"
		return out
	}
	out.Reason = "tool_failed"
	lower := strings.ToLower(string(stderr.data))
	if strings.Contains(lower, "operation not permitted") || strings.Contains(lower, "permission denied") {
		out.Reason = "permission"
	}
	return out
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
		h := model.Hop{TTL: ttl, Address: model.MaskAddress(address.String()), ASN: asnOrCarrierRange(model.NormalizeASN(f[4]), address.String()), RTT: ms}
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

// unlockBudget caps one round of unlock checks. A round is a couple of dozen
// HTTPS requests, each with its own deadline inside the package; this is the
// outer bound so one hung service cannot hold the job forever.
const unlockBudget = 4 * time.Minute
