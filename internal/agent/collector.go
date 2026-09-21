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
	m := model.Metrics{OS: runtime.GOOS, Arch: runtime.GOARCH, Version: model.Version}
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
// outbound probes. Loopback, link-local and unspecified addresses are not
// usable; private IPv4 is kept because NAT'd hosts still have a working IPv4
// path. IPv6 unique-local addresses are excluded because they cannot reach
// public probe targets.
func localIPFamilies() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	return ipFamilies(addrs)
}

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
