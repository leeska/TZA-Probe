package agent

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

// Optional so adapters written for the original report/jobs protocol keep
// working. Heartbeats do not contain a fallback copy of previous metrics.
type HeartbeatTransport interface {
	Heartbeat(context.Context, model.AgentStatus) (model.ReportReply, error)
}

type collectionError struct {
	kind string
	err  error
}

func (e *collectionError) Error() string             { return e.kind + " collection failed: " + e.err.Error() }
func (e *collectionError) Unwrap() error             { return e.err }
func collectionFailure(kind string, err error) error { return &collectionError{kind: kind, err: err} }

type agentHealth struct {
	mu         sync.Mutex
	status     model.AgentStatus
	started    time.Time
	lastReport time.Time
}

func (h *agentHealth) begin() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.started = time.Now()
	// Keep the last failure visible during the next collection attempt. With
	// equal report/heartbeat intervals the heartbeat may always hit Collect;
	// replacing the failure here would hide the diagnostic indefinitely.
	switch h.status.State {
	case "collect_failed", "collect_timeout", "report_failed":
	default:
		h.status.State = "collecting"
		h.status.Reason = ""
	}
}

func (h *agentHealth) collected(m model.Metrics, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if m.Version != "" {
		h.status.Version = m.Version
	}
	if m.OS != "" {
		h.status.OS = m.OS
	}
	if m.Arch != "" {
		h.status.Arch = m.Arch
	}
	if m.Distro != "" {
		h.status.Distro = m.Distro
	}
	if m.Cores > 0 {
		h.status.Cores = m.Cores
	}
	if m.Capabilities != nil {
		h.status.Capabilities = append([]string{}, m.Capabilities...)
	}
	h.started = time.Time{}
	switch h.status.State {
	case "collect_failed", "collect_timeout", "report_failed":
		// Only an acknowledged valid report resolves a previous failure.
	default:
		h.status.State = "reporting"
		h.status.Reason = ""
	}
	if err != nil {
		h.status.State = "collect_failed"
		h.status.Reason = "unknown"
		var failure *collectionError
		if errors.As(err, &failure) {
			h.status.Reason = failure.kind
		}
		if errors.Is(err, context.DeadlineExceeded) {
			h.status.State = "collect_timeout"
			h.status.Reason = "timeout"
		}
	}
}

func (h *agentHealth) reported(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err != nil {
		h.status.State = "report_failed"
		h.status.Reason = "transport"
		return
	}
	h.status.State = "ok"
	h.status.Reason = ""
	h.lastReport = time.Now()
}

func (h *agentHealth) snapshot(timeout time.Duration) (model.AgentStatus, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	status := h.status
	status.Capabilities = append([]string{}, status.Capabilities...)
	if !h.started.IsZero() && time.Since(h.started) >= timeout {
		status.State = "collect_timeout"
		status.Reason = "timeout"
	}
	// Successful metric reports already prove liveness. Extra contact is
	// needed when collecting/reporting has failed or stopped progressing.
	skip := (status.State == "ok" || status.State == "collecting" || status.State == "reporting") && !h.lastReport.IsZero() && time.Since(h.lastReport) < 10*time.Second
	return status, skip
}

func initialHealth(upgrader Upgrader) *agentHealth {
	caps := []string{model.CapabilityUnlock, model.CapabilityParallelJobs}
	if upgrader != nil {
		caps = append(caps, model.CapabilitySelfUpdate)
	}
	return &agentHealth{status: model.AgentStatus{State: "starting", Version: model.Version, OS: runtime.GOOS, Arch: runtime.GOARCH, Cores: runtime.NumCPU(), Distro: Distro(), Capabilities: caps}}
}
