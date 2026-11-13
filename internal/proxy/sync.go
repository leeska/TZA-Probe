package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/leeska/TZA-Probe/internal/channel"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/users"
)

// NodeLister is the part of the core store the module reads.
type NodeLister interface {
	Nodes(ctx context.Context, now time.Time) ([]model.NodeView, error)
}

// Opener opens a management channel stream (channel.Hub).
type Opener interface {
	Open(ctx context.Context, node, kind string, params any) (net.Conn, error)
	Status(node string) channel.Status
}

// KernelView is one kernel on one node as the panel shows it.
type KernelView struct {
	Version string `json:"version,omitempty"`
	Active  bool   `json:"active"`
	Synced  bool   `json:"synced"`
	Error   string `json:"error,omitempty"`
	// StatsError is why traffic could not be read this round (the service
	// itself may be fine).
	StatsError string `json:"stats_error,omitempty"`
}

// NodeView is a node's proxy state after the last sync.
type NodeView struct {
	ID        string                `json:"id"`
	Name      string                `json:"name"`
	Capable   bool                  `json:"capable"`
	Connected bool                  `json:"connected"`
	Arch      string                `json:"arch,omitempty"`
	Error     string                `json:"error,omitempty"`
	Kernels   map[string]KernelView `json:"kernels"`
	At        int64                 `json:"synced_at,omitempty"`
}

type syncState struct {
	mu    sync.Mutex
	run   sync.Mutex
	nodes map[string]NodeView
	kick  chan struct{}
}

// members are the users allowed on any inbound right now: active ones only,
// so a disabled, expired or exhausted user drops out on the next sync.
func (m *Module) members(ctx context.Context) ([]Member, error) {
	list, err := m.Users.List(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	out := []Member{}
	for _, u := range list {
		if u.Status(now) == users.StatusActive {
			out = append(out, Member{ID: u.ID, Name: u.Name, UUID: u.ProxyUUID, Key: u.ProxyKey})
		}
	}
	return out, nil
}

// Active reports whether a forward runs now: enabled, and its user (if any)
// exists and is active.
func (f Forward) Active(active map[string]bool) bool {
	return f.Enabled && (f.UserID == "" || active[f.UserID])
}

// desired renders every node's configurations: node → kernel → config. It
// also returns the forwards, for traffic accounting.
func (m *Module) desired(ctx context.Context) (map[string]map[string][]byte, []Forward, error) {
	inbounds, err := m.Store.List(ctx)
	if err != nil {
		return nil, nil, err
	}
	forwards, err := m.Store.Forwards(ctx)
	if err != nil {
		return nil, nil, err
	}
	members, err := m.members(ctx)
	if err != nil {
		return nil, nil, err
	}
	active := map[string]bool{}
	for _, mem := range members {
		active[mem.ID] = true
	}
	plans := map[string]map[string]*Plan{}
	plan := func(node, kernel string) *Plan {
		if plans[node] == nil {
			plans[node] = map[string]*Plan{}
		}
		if plans[node][kernel] == nil {
			plans[node][kernel] = &Plan{}
		}
		return plans[node][kernel]
	}
	for _, in := range inbounds {
		if in.Enabled {
			p := plan(in.NodeID, in.Kernel)
			p.Inbounds = append(p.Inbounds, in)
		}
	}
	for _, f := range forwards {
		if !f.Active(active) {
			continue
		}
		p := plan(f.NodeID, KernelXray)
		p.Entry = append(p.Entry, f)
		if f.Exit != nil {
			p := plan(f.Exit.NodeID, KernelXray)
			p.Exit = append(p.Exit, f)
		}
	}
	out := map[string]map[string][]byte{}
	for node, kernels := range plans {
		out[node] = map[string][]byte{}
		for kernel, p := range kernels {
			config, err := NodeConfig(kernel, *p, members)
			if err != nil {
				return nil, nil, err
			}
			out[node][kernel] = config
		}
	}
	return out, forwards, nil
}

// Kick asks Run to sync now.
func (m *Module) Kick() {
	select {
	case m.state.kick <- struct{}{}:
	default:
	}
}

// Run syncs every node now, after every Kick and once a minute.
func (m *Module) Run(ctx context.Context) {
	if m.Store == nil {
		return
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if err := m.Sync(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("proxy sync", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-m.state.kick:
		}
	}
}

// Sync brings every node with the proxy capability in line with the
// inbounds: installs the pinned kernel where missing, applies changed
// configurations and removes kernels a node no longer needs.
func (m *Module) Sync(ctx context.Context) error {
	m.state.run.Lock()
	defer m.state.run.Unlock()
	want, forwards, err := m.desired(ctx)
	if err != nil {
		return err
	}
	nodes, err := m.Nodes.Nodes(ctx, time.Now())
	if err != nil {
		return err
	}
	views := map[string]NodeView{}
	usage := map[string]users.Usage{}
	byInbound := map[string]users.Usage{} // inbound tag → bytes
	for _, n := range nodes {
		status := m.Hub.Status(n.ID)
		v := NodeView{ID: n.ID, Name: n.Name, Connected: status.Connected, Capable: slices.Contains(status.Capabilities, channel.CapabilityProxy), Kernels: map[string]KernelView{}}
		_, needed := want[n.ID]
		switch {
		case !v.Connected || !v.Capable:
			if needed {
				v.Error = "节点没有连接管理通道或未开启代理能力：在节点上重跑接入命令并加 --manage"
			}
		default:
			v.Error = m.syncNode(ctx, n.ID, want[n.ID], &v, usage, byInbound)
			v.At = time.Now().Unix()
		}
		views[n.ID] = v
	}
	m.state.mu.Lock()
	m.state.nodes = views
	m.state.mu.Unlock()
	// Forward traffic is counted at the entry inbound and goes to the
	// forward's own total and to its user.
	owner := map[string]string{}
	for _, f := range forwards {
		owner[f.ID] = f.UserID
	}
	perForward := map[string]users.Usage{}
	for tag, t := range byInbound {
		id, ok := ForwardFromTag(tag)
		if _, known := owner[id]; !ok || !known {
			continue
		}
		perForward[id] = t
		if user := owner[id]; user != "" {
			u := usage[user]
			u.Up += t.Up
			u.Down += t.Down
			usage[user] = u
		}
	}
	if err := m.Store.AddForwardTraffic(ctx, perForward); err != nil {
		return fmt.Errorf("记录转发流量：%w", err)
	}
	exhausted, err := m.Users.AddUsage(ctx, usage, time.Now())
	if err != nil {
		return fmt.Errorf("记录流量：%w", err)
	}
	if len(exhausted) > 0 {
		// Their access goes on the next round, not a minute later.
		slog.Info("proxy: users out of quota", "users", exhausted)
		m.Kick()
	}
	return nil
}

// collect reads and resets a running kernel's per-user counters into usage.
// It runs before anything restarts the kernel, which would drop them.
func (m *Module) collect(ctx context.Context, node, kernel string, usage, byInbound map[string]users.Usage) string {
	if kernel != KernelXray {
		return ""
	}
	var st channel.ProxyStats
	if err := m.call(ctx, node, channel.KindProxyStats, channel.ProxyStatsParams{Kernel: kernel}, nil, &st); err != nil {
		return "读取流量失败：" + err.Error()
	}
	if st.Error != "" {
		return st.Error
	}
	for id, t := range st.Users {
		u := usage[id]
		u.Up += t.Up
		u.Down += t.Down
		usage[id] = u
	}
	for tag, t := range st.Inbounds {
		u := byInbound[tag]
		u.Up += t.Up
		u.Down += t.Down
		byInbound[tag] = u
	}
	return ""
}

// Nodes is every node's state after the last sync.
func (m *Module) NodeViews() []NodeView {
	m.state.mu.Lock()
	defer m.state.mu.Unlock()
	out := make([]NodeView, 0, len(m.state.nodes))
	for _, v := range m.state.nodes {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (m *Module) syncNode(ctx context.Context, node string, want map[string][]byte, view *NodeView, usage, byInbound map[string]users.Usage) string {
	var status channel.ProxyStatus
	if err := m.call(ctx, node, channel.KindProxyStatus, nil, nil, &status); err != nil {
		return "读取代理状态失败：" + err.Error()
	}
	view.Arch = status.Arch
	if len(want) > 0 && !status.Systemd {
		return "这台机器没有 systemd，不能托管代理服务"
	}
	for _, kernel := range []string{KernelXray, KernelSingBox} {
		config, needed := want[kernel]
		current := status.Kernels[kernel]
		kv := KernelView{Version: current.Version, Active: current.Active}
		if current.Active && current.ConfigSHA256 != "" {
			kv.StatsError = m.collect(ctx, node, kernel, usage, byInbound)
		}
		if !needed {
			if current.ConfigSHA256 != "" {
				var r channel.ProxyResult
				if err := m.call(ctx, node, channel.KindProxyRemove, channel.ProxyRemoveParams{Kernel: kernel}, nil, &r); err != nil || r.Error != "" {
					kv.Error = firstError(err, r)
				}
				kv.Active = false
			}
			if current.Version != "" || kv.Error != "" {
				view.Kernels[kernel] = kv
			}
			continue
		}
		binary, err := m.Kernels.Get(ctx, kernel, status.Arch)
		if err != nil {
			kv.Error = err.Error()
			view.Kernels[kernel] = kv
			continue
		}
		if current.Version != binary.Version || current.SHA256 != binary.SHA256 {
			if err := m.install(ctx, node, binary); err != nil {
				kv.Error = "安装内核失败：" + err.Error()
				view.Kernels[kernel] = kv
				continue
			}
			kv.Version = binary.Version
			current.ConfigSHA256 = "" // a new binary is applied again
		}
		hash := Hash(config)
		if current.ConfigSHA256 != hash || !current.Active {
			var r channel.ProxyResult
			if err := m.call(ctx, node, channel.KindProxyApply, channel.ProxyApplyParams{Kernel: kernel, ConfigSHA256: hash, Size: int64(len(config))}, config, &r); err != nil || r.Error != "" {
				kv.Error = firstError(err, r)
				if r.Output != "" {
					kv.Error += "：" + r.Output
				}
				view.Kernels[kernel] = kv
				continue
			}
			kv.Active = true
		}
		kv.Synced = true
		view.Kernels[kernel] = kv
	}
	return ""
}

func firstError(err error, r channel.ProxyResult) string {
	if err != nil {
		return err.Error()
	}
	return r.Error
}

func (m *Module) install(ctx context.Context, node string, b Binary) error {
	f, err := os.Open(b.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	var r channel.ProxyResult
	if err := m.call(ctx, node, channel.KindProxyInstall, channel.ProxyInstallParams{Kernel: b.Kernel, Version: b.Version, SHA256: b.SHA256, Size: b.Size}, f, &r); err != nil {
		return err
	}
	if r.Error != "" {
		return errors.New(r.Error)
	}
	return nil
}

// call opens a request, writes body (bytes or a reader) after it and reads
// the one-line answer into out.
func (m *Module) call(ctx context.Context, node, kind string, params any, body any, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	conn, err := m.Hub.Open(ctx, node, kind, params)
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Minute))
	switch b := body.(type) {
	case []byte:
		_, err = conn.Write(b)
	case io.Reader:
		_, err = io.Copy(conn, b)
	case nil:
	default:
		err = fmt.Errorf("unsupported body %T", body)
	}
	if err != nil {
		return err
	}
	return channel.ReadLine(conn, out)
}
