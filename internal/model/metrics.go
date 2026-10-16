package model

import (
	"errors"
	"math"
)

type Metrics struct {
	CounterID   string  `json:"counter_id"`
	CPU         float64 `json:"cpu"`
	MemoryUsed  uint64  `json:"memory_used"`
	MemoryTotal uint64  `json:"memory_total"`
	DiskUsed    uint64  `json:"disk_used"`
	DiskTotal   uint64  `json:"disk_total"`
	NetIn       uint64  `json:"net_in"`
	NetOut      uint64  `json:"net_out"`
	RateIn      float64 `json:"rate_in"`
	RateOut     float64 `json:"rate_out"`
	Uptime      uint64  `json:"uptime"`
	OS          string  `json:"os"`
	Arch        string  `json:"arch"`
	// Distro is the Linux distribution and release the Agent identified from
	// /etc/os-release, e.g. "Debian 13" or "Alpine 3.21" — OS alone is
	// "linux" on every one of them. Empty means an older Agent that has not
	// reported it yet (or a system whose release file is unreadable).
	Distro string `json:"distro"`
	// Cores is the host's logical CPU count (runtime.NumCPU), shown as the CPU
	// metric's subtitle. 0 means an older Agent that has not reported it yet.
	Cores   int    `json:"cpu_cores"`
	Version string `json:"version"`
	// IPFamilies is the set of usable public address families the Agent
	// reported for this host: the host has an address of that family and the
	// kernel has a route for it. nil means the Agent has not reported them yet;
	// an empty non-nil slice means the host can probe nothing.
	IPFamilies []string `json:"ip_families"`
	// Capabilities is what the running Agent can execute beyond latency and
	// route probes, e.g. "unlock". nil means an Agent built before the field
	// existed: it is never handed a job it cannot run (see AllowsUnlock), which
	// is why the field is additive and why the Server ships first.
	Capabilities []string `json:"capabilities,omitempty"`
}

func (m Metrics) Validate() error {
	if len(m.CounterID) > 64 || !percent(m.CPU) || m.MemoryTotal == 0 || m.MemoryUsed > m.MemoryTotal || m.DiskUsed > m.DiskTotal || !finite(m.RateIn) || !finite(m.RateOut) || m.RateIn < 0 || m.RateOut < 0 || m.Cores < 0 || m.Cores > 4096 || len(m.OS) > 100 || len(m.Arch) > 30 || len(m.Distro) > 60 || len(m.Version) > 40 || m.NetIn > math.MaxInt64 || m.NetOut > math.MaxInt64 {
		return errors.New("invalid metrics")
	}
	if len(m.IPFamilies) > 2 {
		return errors.New("invalid metrics")
	}
	seen := map[string]bool{}
	for _, family := range m.IPFamilies {
		if (family != "ipv4" && family != "ipv6") || seen[family] {
			return errors.New("invalid metrics")
		}
		seen[family] = true
	}
	if len(m.Capabilities) > len(AllCapabilities) {
		return errors.New("invalid metrics")
	}
	for _, capability := range m.Capabilities {
		if !KnownCapability(capability) {
			return errors.New("invalid metrics")
		}
	}
	return nil
}

// CapabilityUnlock is reported by an Agent that can run the unlock checks.
const CapabilityUnlock = "unlock"

// CapabilityParallelJobs is reported by an Agent that runs every job it leases,
// each on its own, and keeps polling while they run. Older Agents run at most
// two jobs of a lease and wait for both before polling again, so the Server
// leases them the old way (see AllowsParallelJobs).
const CapabilityParallelJobs = "parallel_jobs"

// CapabilitySelfUpdate is reported by an Agent started with --self-update by a
// service unit that can restart it and swap its binary (the install script's
// systemd / OpenRC units). Only such an Agent is handed an upgrade in the
// report reply; an older one needs the install command run once by hand.
const CapabilitySelfUpdate = "self_update"

var AllCapabilities = []string{CapabilityUnlock, CapabilityParallelJobs, CapabilitySelfUpdate}

func KnownCapability(capability string) bool {
	for _, known := range AllCapabilities {
		if capability == known {
			return true
		}
	}
	return false
}

// AllowsUnlock reports whether this host may be handed an unlock job. Unlike
// AllowsFamily an unreported capability means "no": an Agent that never heard
// of the check would answer every service with failed, and the panel would show
// a machine full of red for no reason. The Server is deployed before the fleet,
// so a mixed fleet simply keeps its unlock cells empty until it upgrades.
func (m Metrics) AllowsUnlock() bool {
	for _, capability := range m.Capabilities {
		if capability == CapabilityUnlock {
			return true
		}
	}
	return false
}

// AllowsParallelJobs reports whether the host's Agent runs a whole lease at
// once (CapabilityParallelJobs). An unreported capability means "no": such an
// Agent drops every job past the second, and the dropped ones would sit in the
// panel as timeouts.
func (m Metrics) AllowsParallelJobs() bool {
	for _, capability := range m.Capabilities {
		if capability == CapabilityParallelJobs {
			return true
		}
	}
	return false
}

// AllowsSelfUpdate reports whether the host's Agent can upgrade itself when the
// report reply asks it to (CapabilitySelfUpdate).
func (m Metrics) AllowsSelfUpdate() bool {
	for _, capability := range m.Capabilities {
		if capability == CapabilitySelfUpdate {
			return true
		}
	}
	return false
}

// AllowsFamily reports whether the host can run probes for family. A host
// that has not reported address families yet stays backwards compatible and
// is allowed to run every configured probe.
func (m Metrics) AllowsFamily(family string) bool {
	if m.IPFamilies == nil {
		return true
	}
	for _, reported := range m.IPFamilies {
		if reported == family {
			return true
		}
	}
	return false
}

type Sample struct {
	At      int64   `json:"at"`
	Metrics Metrics `json:"metrics"`
}

type Report struct {
	Protocol int     `json:"protocol"`
	Metrics  Metrics `json:"metrics"`
}

// ReportReply is the Server's answer to a report. Usually there is nothing to
// say and the Server answers 204 with no body (which is all an older Agent
// expects); a body appears only to hand the Agent an instruction.
type ReportReply struct {
	// Upgrade asks the Agent to replace itself with the binary the Server
	// serves, then restart. Set only for an Agent that reported
	// CapabilitySelfUpdate.
	Upgrade *AgentUpgrade `json:"upgrade,omitempty"`
}

// AgentUpgrade names the binary to fetch: Path is a /dl/ path on the same
// Server and SHA256 its hex digest, checked before anything is replaced.
// Arch, KeyID and Sig are the operator's signature of (Arch, Version, SHA256)
// (see package agentsig); the Agent refuses an upgrade without a valid one.
type AgentUpgrade struct {
	Version string `json:"version"`
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Arch    string `json:"arch,omitempty"`
	KeyID   string `json:"key_id,omitempty"`
	Sig     string `json:"sig,omitempty"`
}
