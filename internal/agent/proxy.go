package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/leeska/TZA-Probe/internal/channel"
)

// ProxyHost runs proxy kernels on this machine for the Server: it installs
// the binaries the Server sends, checks and writes their configuration and
// keeps one systemd service per kernel (tza-proxy-<kernel>). The service runs
// as the system user tza-proxy with only the right to bind low ports; the
// configuration holds the users' credentials, so it is root:tza-proxy 0640.
// (No DynamicUser / LoadCredential: those need systemd 247+, and Ubuntu
// 20.04 still ships 245.)
type ProxyHost struct {
	// Root prefixes every path (tests); "" is the real filesystem.
	Root string
	// Run runs a command and returns its combined output (tests replace it).
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)
	// Systemd reports whether systemd manages this machine.
	Systemd func() bool
	mu      sync.Mutex
}

var kernelName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
var kernelVersion = regexp.MustCompile(`^[0-9A-Za-z.+-]{1,40}$`)

func NewProxyHost() *ProxyHost {
	return &ProxyHost{
		Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.WaitDelay = 2 * time.Second
			return cmd.CombinedOutput()
		},
		Systemd: func() bool { _, err := os.Stat("/run/systemd/system"); return err == nil },
	}
}

func (h *ProxyHost) path(parts ...string) string {
	return filepath.Join(append([]string{"/" + strings.TrimPrefix(h.Root, "/")}, parts...)...)
}
func (h *ProxyHost) kernelDir() string { return h.path("usr/local/lib/tza-probe/kernels") }
func (h *ProxyHost) configDir() string { return h.path("etc/tza-probe/proxy") }
func (h *ProxyHost) unitPath(k string) string {
	return h.path("etc/systemd/system", "tza-proxy-"+k+".service")
}
func (h *ProxyHost) configPath(k string) string { return filepath.Join(h.configDir(), k+".json") }
func (h *ProxyHost) statePath() string          { return filepath.Join(h.configDir(), "state.json") }
func (h *ProxyHost) binaryPath(k, v string) string {
	return filepath.Join(h.kernelDir(), k, v, k)
}

func (h *ProxyHost) loadState() map[string]channel.KernelState {
	state := map[string]channel.KernelState{}
	if raw, err := os.ReadFile(h.statePath()); err == nil {
		json.Unmarshal(raw, &state)
	}
	return state
}

func (h *ProxyHost) saveState(state map[string]channel.KernelState) error {
	if err := os.MkdirAll(h.configDir(), 0700); err != nil {
		return err
	}
	for k, s := range state {
		s.Active, s.Error = false, ""
		state[k] = s
	}
	raw, _ := json.MarshalIndent(state, "", "  ")
	return writeFileAtomic(h.statePath(), raw, 0600)
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".partial"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Handlers are the proxy capability's channel requests.
func (h *ProxyHost) Handlers() map[string]channel.Handler {
	return map[string]channel.Handler{
		channel.KindProxyStatus:  h.status,
		channel.KindProxyInstall: h.install,
		channel.KindProxyApply:   h.apply,
		channel.KindProxyRemove:  h.remove,
	}
}

func (h *ProxyHost) status(ctx context.Context, _ json.RawMessage, conn net.Conn) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.loadState()
	systemd := h.Systemd()
	for k, s := range state {
		if s.ConfigSHA256 != "" && systemd {
			out, err := h.Run(ctx, "systemctl", "is-active", "tza-proxy-"+k)
			s.Active = err == nil && strings.TrimSpace(string(out)) == "active"
			if !s.Active {
				s.Error = "服务未在运行：" + strings.TrimSpace(string(out))
			}
		}
		state[k] = s
	}
	return channel.WriteLine(conn, channel.ProxyStatus{Arch: runtime.GOARCH, Systemd: systemd, Kernels: state})
}

func reply(conn net.Conn, err error, output []byte) error {
	r := channel.ProxyResult{}
	if err != nil {
		r.Error = err.Error()
	}
	if len(output) > 4096 {
		output = output[len(output)-4096:]
	}
	r.Output = strings.TrimSpace(string(output))
	return channel.WriteLine(conn, r)
}

func (h *ProxyHost) install(ctx context.Context, raw json.RawMessage, conn net.Conn) error {
	var p channel.ProxyInstallParams
	if err := json.Unmarshal(raw, &p); err != nil || !kernelName.MatchString(p.Kernel) || !kernelVersion.MatchString(p.Version) || p.Size <= 0 || p.Size > channel.MaxKernelBinary || len(p.SHA256) != 64 {
		return reply(conn, errors.New("安装请求无效"), nil)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	path := h.binaryPath(p.Kernel, p.Version)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return reply(conn, err, nil)
	}
	tmp := path + ".partial"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0755)
	if err != nil {
		return reply(conn, err, nil)
	}
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(conn, p.Size))
	f.Close()
	if err == nil && n != p.Size {
		err = errors.New("内核传输中断")
	}
	if err == nil && hex.EncodeToString(hash.Sum(nil)) != p.SHA256 {
		err = errors.New("内核校验失败（SHA-256 不符）")
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
		return reply(conn, err, nil)
	}
	state := h.loadState()
	s := state[p.Kernel]
	s.Version, s.SHA256 = p.Version, p.SHA256
	state[p.Kernel] = s
	return reply(conn, h.saveState(state), nil)
}

// checkArgs is how each kernel validates a configuration file without
// starting.
func checkArgs(kernel, file string) []string {
	if kernel == "xray" {
		return []string{"run", "-test", "-config", file}
	}
	return []string{"check", "-c", file}
}

func runArgs(kernel, config string) string {
	if kernel == "xray" {
		return "run -config " + config
	}
	return "run -c " + config
}

// ProxyUser runs the kernels.
const ProxyUser = "tza-proxy"

func unit(kernel, binary, config string) string {
	return fmt.Sprintf(`# Managed by tza-agent: the Server's 代理 module writes this file.
[Unit]
Description=TZA Probe proxy (%[1]s)
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=%[2]s %[3]s
User=%[4]s
Group=%[4]s
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
LimitNOFILE=1048576
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
`, kernel, binary, runArgs(kernel, config), ProxyUser)
}

func (h *ProxyHost) apply(ctx context.Context, raw json.RawMessage, conn net.Conn) error {
	var p channel.ProxyApplyParams
	if err := json.Unmarshal(raw, &p); err != nil || !kernelName.MatchString(p.Kernel) || p.Size <= 0 || p.Size > channel.MaxProxyConfig {
		return reply(conn, errors.New("配置请求无效"), nil)
	}
	config, err := io.ReadAll(io.LimitReader(conn, p.Size))
	if err != nil || int64(len(config)) != p.Size {
		return reply(conn, errors.New("配置传输中断"), nil)
	}
	sum := sha256.Sum256(config)
	if hex.EncodeToString(sum[:]) != p.ConfigSHA256 || !json.Valid(config) {
		return reply(conn, errors.New("配置校验失败"), nil)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.Systemd() {
		return reply(conn, errors.New("这台机器没有 systemd，不能托管代理服务"), nil)
	}
	state := h.loadState()
	s := state[p.Kernel]
	if s.Version == "" {
		return reply(conn, errors.New("内核还没安装"), nil)
	}
	binary := h.binaryPath(p.Kernel, s.Version)
	if err := os.MkdirAll(h.configDir(), 0700); err != nil {
		return reply(conn, err, nil)
	}
	// xray picks the parser from the extension, so the candidate keeps .json.
	candidate := filepath.Join(h.configDir(), p.Kernel+".candidate.json")
	if err := writeFileAtomic(candidate, config, 0600); err != nil {
		return reply(conn, err, nil)
	}
	checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	out, err := h.Run(checkCtx, binary, checkArgs(p.Kernel, candidate)...)
	cancel()
	if err != nil {
		os.Remove(candidate)
		return reply(conn, errors.New("内核拒绝了这份配置"), out)
	}
	gid, err := h.ensureUser(ctx)
	if err != nil {
		os.Remove(candidate)
		return reply(conn, fmt.Errorf("创建系统用户 %s 失败：%w", ProxyUser, err), nil)
	}
	if err := os.Chown(candidate, 0, gid); err != nil && h.Root == "" {
		os.Remove(candidate)
		return reply(conn, err, nil)
	}
	if err := os.Chmod(candidate, 0640); err != nil {
		return reply(conn, err, nil)
	}
	if err := os.Rename(candidate, h.configPath(p.Kernel)); err != nil {
		return reply(conn, err, nil)
	}
	if err := os.MkdirAll(filepath.Dir(h.unitPath(p.Kernel)), 0755); err != nil {
		return reply(conn, err, nil)
	}
	if err := writeFileAtomic(h.unitPath(p.Kernel), []byte(unit(p.Kernel, binary, h.configPath(p.Kernel))), 0644); err != nil {
		return reply(conn, err, nil)
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", "tza-proxy-" + p.Kernel}, {"restart", "tza-proxy-" + p.Kernel}} {
		if out, err := h.Run(ctx, "systemctl", args...); err != nil {
			return reply(conn, fmt.Errorf("systemctl %s 失败", args[0]), out)
		}
	}
	s.ConfigSHA256 = p.ConfigSHA256
	state[p.Kernel] = s
	if err := h.saveState(state); err != nil {
		return reply(conn, err, nil)
	}
	// A kernel that dies on start shows up within a moment.
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
	}
	if out, err := h.Run(ctx, "systemctl", "is-active", "tza-proxy-"+p.Kernel); err != nil {
		logs, _ := h.Run(ctx, "journalctl", "-u", "tza-proxy-"+p.Kernel, "-n", "20", "--no-pager", "-o", "cat")
		return reply(conn, errors.New("服务启动后没有保持运行："+strings.TrimSpace(string(out))), logs)
	}
	return reply(conn, nil, nil)
}

// ensureUser creates the tza-proxy system user when missing and returns its
// group id. The configuration directory stays root-only except for the
// group's right to enter it.
func (h *ProxyHost) ensureUser(ctx context.Context) (int, error) {
	if _, err := h.Run(ctx, "id", "-u", ProxyUser); err != nil {
		if out, err := h.Run(ctx, "useradd", "--system", "--no-create-home", "--home-dir", "/nonexistent", "--shell", "/usr/sbin/nologin", "--user-group", ProxyUser); err != nil {
			return 0, fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
		}
	}
	out, err := h.Run(ctx, "id", "-g", ProxyUser)
	if err != nil {
		return 0, err
	}
	gid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("id -g %s: %q", ProxyUser, out)
	}
	if err := os.Chown(h.configDir(), 0, gid); err != nil && h.Root == "" {
		return 0, err
	}
	if err := os.Chmod(h.configDir(), 0750); err != nil {
		return 0, err
	}
	return gid, nil
}

func (h *ProxyHost) remove(ctx context.Context, raw json.RawMessage, conn net.Conn) error {
	var p channel.ProxyRemoveParams
	if err := json.Unmarshal(raw, &p); err != nil || !kernelName.MatchString(p.Kernel) {
		return reply(conn, errors.New("请求无效"), nil)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.Systemd() {
		h.Run(ctx, "systemctl", "disable", "--now", "tza-proxy-"+p.Kernel)
	}
	os.Remove(h.unitPath(p.Kernel))
	os.Remove(h.configPath(p.Kernel))
	if h.Systemd() {
		h.Run(ctx, "systemctl", "daemon-reload")
	}
	state := h.loadState()
	s := state[p.Kernel]
	s.ConfigSHA256 = ""
	state[p.Kernel] = s
	return reply(conn, h.saveState(state), nil)
}
