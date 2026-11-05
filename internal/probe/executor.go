package probe

import (
	"context"
	"net"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/unlock"
)

// Executor runs one built-in or registered probe kind under a bounded context.
type Executor interface {
	Kind() string
	Run(context.Context, model.Job) model.Result
}

// Registry dispatches a validated Job to the Executor registered for Task.Kind.
type Registry struct {
	mu        sync.RWMutex
	executors map[string]Executor
}

// NewRegistry builds a Registry from the given executors.
func NewRegistry(executors ...Executor) *Registry {
	r := &Registry{executors: make(map[string]Executor, len(executors))}
	for _, ex := range executors {
		r.Register(ex)
	}
	return r
}

// Register installs or replaces the executor for ex.Kind().
func (r *Registry) Register(ex Executor) {
	if ex == nil || ex.Kind() == "" {
		return
	}
	r.mu.Lock()
	if r.executors == nil {
		r.executors = map[string]Executor{}
	}
	r.executors[ex.Kind()] = ex
	r.mu.Unlock()
}

// Lookup returns the executor registered for kind.
func (r *Registry) Lookup(kind string) (Executor, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	ex, ok := r.executors[kind]
	r.mu.RUnlock()
	return ex, ok
}

// Kinds returns the registered probe kinds in deterministic order.
func (r *Registry) Kinds() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	out := make([]string, 0, len(r.executors))
	for k := range r.executors {
		out = append(out, k)
	}
	r.mu.RUnlock()
	sort.Strings(out)
	return out
}

// Run validates j.Task and dispatches it to the matching Executor.
func (r *Registry) Run(ctx context.Context, j model.Job) model.Result {
	failed := model.Result{JobID: j.ID, Status: "failed", Route: "未知", Hops: []model.Hop{}}
	if err := j.Task.Validate(); err != nil {
		return failed
	}
	ex, ok := r.Lookup(j.Task.Kind)
	if !ok {
		failed.Status = "unavailable"
		failed.Reason = "tool_missing"
		return failed
	}
	return ex.Run(ctx, j)
}

// TCPExecutor measures TCP handshake latency over three attempts.
type TCPExecutor struct{}

func (TCPExecutor) Kind() string { return "tcp" }

func (TCPExecutor) Run(ctx context.Context, j model.Job) model.Result {
	result := model.Result{JobID: j.ID, Status: "failed", Route: "未知", Hops: []model.Hop{}}
	if err := j.Task.Validate(); err != nil || j.Task.Kind != "tcp" {
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	ip, tcpNet, reason := resolveTargetIP(ctx, j.Task)
	if reason != "" {
		result.Reason = reason
		return result
	}
	result.Sent = 3
	var total float64
	for i := 0; i < 3; i++ {
		start := time.Now()
		dialer := net.Dialer{Timeout: 3 * time.Second}
		conn, err := dialer.DialContext(ctx, tcpNet, net.JoinHostPort(ip, strconv.Itoa(j.Task.Port)))
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

// RouteExecutor measures return-path hops and backbone classification.
type RouteExecutor struct {
	Nexttrace string
	Embedded  *Embedded
}

func (RouteExecutor) Kind() string { return model.RouteKind }

func (e RouteExecutor) Run(ctx context.Context, j model.Job) model.Result {
	result := model.Result{JobID: j.ID, Status: "failed", Route: "未知", Hops: []model.Hop{}}
	if err := j.Task.Validate(); err != nil || j.Task.Kind != model.RouteKind {
		return result
	}
	if e.Nexttrace == "" && e.Embedded == nil {
		result.Status = "unavailable"
		result.Reason = "tool_missing"
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	ip, _, reason := resolveTargetIP(ctx, j.Task)
	if reason != "" {
		result.Reason = reason
		return result
	}
	outcome := e.traceRoute(ctx, j, ip)
	result.Hops, result.Sent = outcome.Hops, 1
	result.Received, result.Latency = outcome.Received, outcome.Latency
	switch {
	case outcome.Received > 0:
		result.Status = "ok"
		result.Route = model.RouteLabel(result.Hops)
	case outcome.Reason == "timeout":
		result.Status = "timeout"
		result.Reason = "timeout"
	case outcome.Reason == "tool_missing":
		result.Status = "unavailable"
		result.Reason = "tool_missing"
	default:
		result.Status = "failed"
		result.Reason = outcome.Reason
	}
	return result
}

func (e RouteExecutor) traceRoute(ctx context.Context, j model.Job, ip string) traceOutcome {
	if e.Nexttrace != "" {
		return execTrace(ctx, e.Nexttrace, j, ip)
	}
	return e.Embedded.Trace(ctx, net.ParseIP(ip), j.Task.Port)
}

// UnlockExecutor executes the built-in streaming/AI unlock catalog from the host's egress.
type UnlockExecutor struct{}

func (UnlockExecutor) Kind() string { return model.UnlockKind }

func (UnlockExecutor) Run(ctx context.Context, j model.Job) model.Result {
	result := model.Result{JobID: j.ID, Status: "failed", Route: "未知", Hops: []model.Hop{}}
	if err := j.Task.Validate(); err != nil || j.Task.Kind != model.UnlockKind {
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, unlockBudget)
	defer cancel()
	family := j.Task.Family
	if family == "" {
		family = "ipv4"
	}
	results := unlock.Run(ctx, unlock.Options{Family: family})
	if len(results) == 0 {
		result.Reason = "tool_failed"
		return result
	}
	result.Status = "ok"
	result.Unlocks = results
	return result
}

func resolveTargetIP(ctx context.Context, task model.Probe) (ip string, tcpNet string, reason string) {
	network := "ip4"
	tcpNet = "tcp4"
	if task.Family == "ipv6" {
		network = "ip6"
		tcpNet = "tcp6"
	}
	addresses, err := net.DefaultResolver.LookupIP(ctx, network, task.Host)
	if err != nil || len(addresses) == 0 {
		return "", tcpNet, "dns"
	}
	if syntheticAddress(addresses[0]) {
		return "", tcpNet, "synthetic_dns"
	}
	return addresses[0].String(), tcpNet, ""
}
