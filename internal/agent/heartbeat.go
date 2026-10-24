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
	sequence   int64
}

// Callers hold mu. Sending the same diagnostic again retains its generation;
// only a new state or a new metric report advances it.
func (h *agentHealth) next() model.AgentState {
	h.sequence++
	return model.AgentState{Session: h.status.Session, Sequence: h.sequence}
}

// prepareReport reserves a generation without clearing the previous failure.
// While this request is in flight, heartbeats must still describe that older
// failure rather than claim a new fault after this sample.
func (h *agentHealth) prepareReport(m *model.Metrics) {
	h.mu.Lock()
	defer h.mu.Unlock()
	m.AgentState = h.next()
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
		h.status.AgentState = h.next()
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
		h.status.AgentState = h.next()
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
	h.status.AgentState = h.next()
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
	if !h.started.IsZero() && time.Since(h.started) >= timeout && h.status.State != "collect_timeout" {
		h.status.AgentState = h.next()
		h.status.State, h.status.Reason = "collect_timeout", "timeout"
	}
	status := h.status
	status.Capabilities = append([]string{}, status.Capabilities...)
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
	return &agentHealth{sequence: 1, status: model.AgentStatus{AgentState: model.AgentState{Session: model.Secret(), Sequence: 1}, State: "starting", Version: model.Version, OS: runtime.GOOS, Arch: runtime.GOARCH, Cores: runtime.NumCPU(), Distro: Distro(), Capabilities: caps}}
}
