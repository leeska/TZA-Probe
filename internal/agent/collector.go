package agent

import (
	"context"
	"errors"
	"net"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
	gnet "github.com/shirou/gopsutil/v4/net"
)

// SystemCollector uses explicit interfaces when configured; otherwise all
// non-loopback interfaces. A host with bridges/tunnels should configure its WAN NICs.
type SystemCollector struct {
	Interfaces      []string
	Mount           string
	lastIn, lastOut uint64
	lastAt          time.Time
	lastCounterID   string
}

func (c *SystemCollector) Collect(ctx context.Context) (model.Metrics, error) {
	m := model.Metrics{OS: runtime.GOOS, Arch: runtime.GOARCH, Distro: Distro(), Cores: runtime.NumCPU(), Version: model.Version, Capabilities: []string{model.CapabilityUnlock, model.CapabilityParallelJobs}}
	cp, err := cpu.PercentWithContext(ctx, time.Second, false)
	if err != nil || len(cp) == 0 {
		return m, errors.New("CPU collection failed")
	}
	m.CPU = cp[0]
	memory, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return m, err
	}
	m.MemoryTotal = memory.Total
	m.MemoryUsed = memory.Used
	mount := c.Mount
	if mount == "" {
		mount = "/"
	}
	d, err := disk.UsageWithContext(ctx, mount)
	if err != nil {
		return m, err
	}
	m.DiskTotal = d.Total
	m.DiskUsed = d.Used
	m.Uptime, err = host.UptimeWithContext(ctx)
	if err != nil {
		return m, err
	}
	counters, err := gnet.IOCountersWithContext(ctx, true)
	if err != nil {
		return m, err
	}
	selected := map[string]bool{}
	for _, name := range c.Interfaces {
		selected[name] = true
	}
	names := []string{}
	for _, counter := range counters {
		if len(selected) > 0 && !selected[counter.Name] {
			continue
		}
		if len(selected) == 0 && (counter.Name == "lo" || counter.Name == "lo0") {
			continue
		}
		names = append(names, counter.Name)
		m.NetIn += counter.BytesRecv
		m.NetOut += counter.BytesSent
	}

	if len(selected) > 0 && len(names) != len(selected) {
		return m, errors.New("one or more configured network interfaces are unavailable")
	}
	boot, err := host.BootTimeWithContext(ctx)
	if err != nil {
		return m, err
	}
	sort.Strings(names)
	m.CounterID = model.Hash(strconv.FormatUint(boot, 10) + "/" + strings.Join(names, ","))
	now := time.Now()
	if !c.lastAt.IsZero() && c.lastCounterID == m.CounterID {
		seconds := now.Sub(c.lastAt).Seconds()
		if m.NetIn >= c.lastIn {
			m.RateIn = float64(m.NetIn-c.lastIn) / seconds
		}
		if m.NetOut >= c.lastOut {
			m.RateOut = float64(m.NetOut-c.lastOut) / seconds
		}
	}
	c.lastIn, c.lastOut, c.lastAt = m.NetIn, m.NetOut, now
	c.lastCounterID = m.CounterID
	m.IPFamilies = localIPFamilies()
	return m, m.Validate()
}

// localIPFamilies reports which public address families this host can use for
// outbound probes. Two conditions must hold: a public address of that family
// exists, and the kernel can actually route to it. An address alone proves
// nothing — a host whose IPv4 lives on a tunnel interface that is down, or that
// only lends its IPv4 to a userspace proxy, keeps the address while every IPv4
// dial fails immediately with "network is unreachable".
func localIPFamilies() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	return routableFamilies(ipFamilies(addrs))
}

// routableFamilies drops every family the kernel currently has no route for.
func routableFamilies(families []string) []string {
	out := make([]string, 0, len(families))
	for _, family := range families {
		if familyRoutable(family) {
			out = append(out, family)
		}
	}
	return out
}

// familyRoutable asks the kernel for a route and a source address in this
// family: a connected UDP socket resolves both without sending a packet, and
// the destination is from the documentation range (RFC 5737 / RFC 3849), so it
// can never reach a real host. No route means the family is not offered for
// probing at all, instead of reporting it and collecting 100% failures.
const (
	documentationV4 = "192.0.2.1:9"
	documentationV6 = "[2001:db8::1]:9"
)

func familyRoutable(family string) bool {
	network, target := "udp4", documentationV4
	if family == "ipv6" {
		network, target = "udp6", documentationV6
	}
	return routeLookup(network, target) == nil
}

// routeLookup is the route-lookup seam; tests replace it, production dials.
var routeLookup = func(network, address string) error {
	conn, err := net.Dial(network, address)
	if err != nil {
		return err
	}
	return conn.Close()
}

// ipFamilies classifies interface addresses into candidate probe families.
// Loopback, link-local and unspecified addresses are not usable; private IPv4
// is kept because NAT'd hosts still have a working IPv4 path once a route
// exists, while IPv6 unique-local addresses are excluded because they cannot
// reach public probe targets. Candidates still have to pass routableFamilies.
func ipFamilies(addrs []net.Addr) []string {
	seen := map[string]bool{}
	for _, addr := range addrs {
		var ip net.IP
		switch value := addr.(type) {
		case *net.IPNet:
			ip = value.IP
		case *net.IPAddr:
			ip = value.IP
		}
		if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() {
			continue
		}
		if ip4 := ip.To4(); ip4 != nil {
			seen["ipv4"] = true
			continue
		}
		ip16 := ip.To16()
		if ip16 == nil || ip16.IsPrivate() {
			continue
		}
		seen["ipv6"] = true
	}
	out := make([]string, 0, 2)
	for _, family := range []string{"ipv4", "ipv6"} {
		if seen[family] {
			out = append(out, family)
		}
	}
	return out
}
