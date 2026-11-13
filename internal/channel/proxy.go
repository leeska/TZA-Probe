package channel

import "io"

// The proxy capability: the Server installs a kernel binary on the node,
// writes its configuration and keeps it running as a systemd service. Every
// request answers with one JSON line after the stream is accepted.
const (
	CapabilityProxy = "proxy"
	// KindProxyStatus answers ProxyStatus.
	KindProxyStatus = "proxy.status"
	// KindProxyInstall is followed by exactly Size raw bytes of the binary;
	// answers ProxyResult.
	KindProxyInstall = "proxy.install"
	// KindProxyApply is followed by exactly Size bytes of configuration (the
	// request line itself is kept small); the Agent checks it with the kernel,
	// then writes it and (re)starts the service. Answers ProxyResult.
	KindProxyApply = "proxy.apply"
	// KindProxyRemove stops the service and removes its configuration (the
	// binary stays); answers ProxyResult.
	KindProxyRemove = "proxy.remove"
	// KindProxyStats reads and resets the per-user traffic counters of a
	// running kernel (xray only); answers ProxyStats.
	KindProxyStats = "proxy.stats"
	// MaxKernelBinary bounds an installed binary.
	MaxKernelBinary = 128 << 20
	// MaxProxyConfig bounds a configuration.
	MaxProxyConfig = 4 << 20
)

type KernelState struct {
	Version      string `json:"version,omitempty"`
	SHA256       string `json:"sha256,omitempty"`
	ConfigSHA256 string `json:"config_sha256,omitempty"`
	Active       bool   `json:"active"`
	Error        string `json:"error,omitempty"`
}

type ProxyStatus struct {
	Arch    string                 `json:"arch"`
	Systemd bool                   `json:"systemd"`
	Kernels map[string]KernelState `json:"kernels"`
}

type ProxyInstallParams struct {
	Kernel  string `json:"kernel"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
}

type ProxyApplyParams struct {
	Kernel       string `json:"kernel"`
	ConfigSHA256 string `json:"config_sha256"`
	Size         int64  `json:"size"`
}

type ProxyRemoveParams struct {
	Kernel string `json:"kernel"`
}

type ProxyStatsParams struct {
	Kernel string `json:"kernel"`
}

// UserTraffic is bytes counted for one user since the previous read.
type UserTraffic struct {
	Up   int64 `json:"up"`
	Down int64 `json:"down"`
}

// ProxyStats maps user id → traffic since the previous read.
type ProxyStats struct {
	Users map[string]UserTraffic `json:"users"`
	Error string                 `json:"error,omitempty"`
}

type ProxyResult struct {
	Error  string `json:"error,omitempty"`
	Output string `json:"output,omitempty"`
}

// WriteLine and ReadLine expose the one-line JSON framing the proxy
// requests answer with.
func WriteLine(w io.Writer, v any) error { return writeLine(w, v) }
func ReadLine(r io.Reader, v any) error  { return readLine(r, v) }
