package model

import "errors"

// AgentStatus contains bounded diagnostic codes, never raw OS/HTTP errors,
// interface names, paths or credentials. Heartbeats carry no metric values.
type AgentStatus struct {
	State        string   `json:"state"`
	Reason       string   `json:"reason,omitempty"`
	Version      string   `json:"version"`
	OS           string   `json:"os"`
	Arch         string   `json:"arch"`
	Distro       string   `json:"distro,omitempty"`
	Cores        int      `json:"cpu_cores,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

func (s AgentStatus) Validate() error {
	switch s.State {
	case "starting", "collecting", "reporting", "ok", "collect_failed", "collect_timeout", "report_failed":
	default:
		return errors.New("invalid agent state")
	}
	switch s.Reason {
	case "", "cpu", "memory", "disk", "network", "interfaces", "uptime", "boot_time", "invalid", "unknown", "timeout", "transport":
	default:
		return errors.New("invalid diagnostic reason")
	}
	if len(s.Version) > 40 || len(s.OS) > 100 || len(s.Arch) > 30 || len(s.Distro) > 60 || s.Cores < 0 || s.Cores > 4096 || len(s.Capabilities) > len(AllCapabilities) {
		return errors.New("invalid agent metadata")
	}
	for _, capability := range s.Capabilities {
		if !KnownCapability(capability) {
			return errors.New("invalid capability")
		}
	}
	return nil
}

type Heartbeat struct {
	Protocol int         `json:"protocol"`
	Status   AgentStatus `json:"status"`
}

// AgentMetrics is metadata for upgrade compatibility, not a metric sample.
func (v NodeView) AgentMetrics() *Metrics {
	if v.Metrics != nil {
		return v.Metrics
	}
	if v.AgentStatus == nil {
		return nil
	}
	s := v.AgentStatus
	return &Metrics{Version: s.Version, OS: s.OS, Arch: s.Arch, Distro: s.Distro, Cores: s.Cores, Capabilities: s.Capabilities}
}
