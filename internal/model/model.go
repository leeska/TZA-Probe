// Package model defines the versioned wire contract and pure domain rules.
// It has no dependency on HTTP, SQL, collectors, or notification transports.
package model

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net"
	"strings"
	"time"
)

var ErrNotFound = errors.New("not found")
var ErrConflict = errors.New("conflict")
var ErrInvalid = errors.New("invalid input")

// conflictError is an ErrConflict that says which conflict it is: the 409 body
// carries Error() as is, so the panel and MCP can tell the admin what to do
// instead of one catch-all line.
type conflictError struct{ msg string }

func (e conflictError) Error() string        { return e.msg }
func (e conflictError) Is(target error) bool { return target == ErrConflict }

// Conflict returns an error that matches ErrConflict and reads as msg.
func Conflict(msg string) error { return conflictError{msg} }

// invalidError is an ErrInvalid that reads as its own message (a 400 body).
type invalidError struct{ msg string }

func (e invalidError) Error() string        { return e.msg }
func (e invalidError) Is(target error) bool { return target == ErrInvalid }

// Invalid returns an error that matches ErrInvalid and reads as msg.
func Invalid(msg string) error { return invalidError{msg} }

// notFoundError is an ErrNotFound that names what was missing.
type notFoundError struct{ msg string }

func (e notFoundError) Error() string        { return e.msg }
func (e notFoundError) Is(target error) bool { return target == ErrNotFound }

// NotFound returns an error that matches ErrNotFound and reads as msg.
func NotFound(msg string) error { return notFoundError{msg} }

// Version is overridden at release time with -ldflags "-X …/internal/model.Version=v2.0.0".
var Version = "2.0.0-dev"

const Protocol = 1
const OfflineAfter = 45 * time.Second

type Alerts struct {
	OfflineSeconds int     `json:"offline_seconds"`
	RepeatSeconds  int     `json:"repeat_seconds"`
	Offline        bool    `json:"offline"`
	Renewal        bool    `json:"renewal"`
	RenewalDays    int     `json:"renewal_days"`
	Traffic        bool    `json:"traffic"`
	TrafficPercent float64 `json:"traffic_percent"`
	// Throttle warns when the average transfer rate over the throttle window
	// reaches the line's throttle threshold (see Node.ThrottleMbps).
	Throttle      bool    `json:"throttle"`
	Load          bool    `json:"load"`
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryPercent float64 `json:"memory_percent"`
	HoldSeconds   int     `json:"hold_seconds"`
}

// Node is a host's configuration as the panel and MCP edit it, grouped by
// concern: what the public card shows (Display), what the line costs and how
// its cycle counts (Billing), the line's own bandwidth rule (Line), and when to
// alert (Alerts). Server-kept state — creation time and the cycle correction —
// is not part of it (see storage).
type Node struct {
	ID      string      `json:"id"`
	Name    string      `json:"name"`
	Group   string      `json:"group"`
	Region  string      `json:"region"`
	Order   int         `json:"order"`
	Public  bool        `json:"public"`
	Display NodeDisplay `json:"display"`
	Billing Billing     `json:"billing"`
	Line    Line        `json:"line"`
	Alerts  Alerts      `json:"alerts"`
}

// NodeDisplay picks which probe results the public card shows. Both regions
// must match the target regions of the tasks bound to the node.
type NodeDisplay struct {
	// RouteRegion is the region whose route results the card shows; empty
	// matches every region.
	RouteRegion string `json:"route_region"`
	// LatencyRegion picks which region's TCP latency the card shows. Empty
	// falls back to RouteRegion, then to every region.
	LatencyRegion string `json:"latency_region"`
	// LatencyCarriers narrows the latency cells; empty shows every carrier.
	LatencyCarriers []string `json:"latency_carriers,omitempty"`
	// RouteCarriers narrows the route cells the same way. Route cells exist for
	// the three domestic carriers only, so 其他 is not a choice here.
	RouteCarriers []string `json:"route_carriers,omitempty"`
}

// Billing is the line's price and its traffic cycle.
type Billing struct {
	// ResetAt is the cycle anchor: its day and time of day repeat every month
	// (RFC3339, second precision). See Cycle.
	ResetAt      string `json:"reset_at"`
	TrafficLimit int64  `json:"traffic_limit"`
	TrafficMode  string `json:"traffic_mode"`
	// Free marks a machine that costs nothing: the card says 免费 instead of a
	// price, and a save drops price and period (see Normalized). Without it a
	// zero price reads as 未设置价格.
	Free        bool    `json:"free,omitempty"`
	Price       float64 `json:"price"`
	Currency    string  `json:"currency"`
	PricePeriod string  `json:"price_period"`
	ExpiresAt   string  `json:"expires_at"`
}

// Line is the line's bandwidth and its own throttling rule. Two shapes are
// common:
//   - 按窗口平均带宽（德国商家）：1 Gbit/s 端口、近 24 小时平均超过 100 Mbit/s
//     即临时降到 100 Mbit/s → ThrottleOn=bandwidth（默认）+ ThrottleHours；
//   - 按周期额度（DMIT T1 这类）：1000 GB（出/入取高值）@ 4 Gbps 突发，用满后
//     不计量但降到 50/100 Mbit/s → ThrottleOn=quota + Mbps 记突发峰值。
//
// ThrottleMbps=0 means "not declared": the panel then shows no bandwidth row and
// keeps no alert for it.
type Line struct {
	Mbps          int    `json:"mbps"`
	ThrottleMbps  int    `json:"throttle_mbps"`
	ThrottleHours int    `json:"throttle_hours"`
	ThrottleOn    string `json:"throttle_on"`
}

func (n Node) Validate() error {
	d := n.Display
	if strings.TrimSpace(n.Name) == "" || len(n.Name) > 100 || len(n.Group) > 100 || len(n.Region) > 60 || len(d.RouteRegion) > 60 || len(d.LatencyRegion) > 60 {
		return errors.New("名称必填且不超过 100 字节；分组、地区过长")
	}
	seenCarriers := map[string]bool{}
	for _, carrier := range d.LatencyCarriers {
		if (carrier != "电信" && carrier != "联通" && carrier != "移动" && carrier != "其他") || seenCarriers[carrier] {
			return errors.New("延迟展示运营商无效或重复")
		}
		seenCarriers[carrier] = true
	}
	seenCarriers = map[string]bool{}
	for _, carrier := range d.RouteCarriers {
		if (carrier != "电信" && carrier != "联通" && carrier != "移动") || seenCarriers[carrier] {
			return errors.New("回程展示运营商须为电信、联通、移动，且不能重复")
		}
		seenCarriers[carrier] = true
	}
	if err := n.Billing.Validate(); err != nil {
		return err
	}
	if err := n.Line.Validate(); err != nil {
		return err
	}
	if n.Line.ThrottleOn == "quota" && n.Billing.TrafficLimit <= 0 {
		return errors.New("按周期额度降速需先填写流量额度")
	}
	if err := n.Alerts.Validate(); err != nil {
		return err
	}
	if n.Alerts.Throttle && n.Line.ThrottleMbps == 0 {
		return errors.New("启用降速告警需先填写降速阈值")
	}
	return nil
}

func (b Billing) Validate() error {
	if _, err := parseUTC(b.ResetAt); err != nil {
		return errors.New("重置时间须为 RFC3339（精确到秒）")
	}
	if b.TrafficLimit < 0 || b.Price < 0 || !finite(b.Price) || len(b.Currency) > 12 || len(b.PricePeriod) > 30 {
		return errors.New("计费配置无效")
	}
	if strings.Contains(b.PricePeriod, "免费") {
		return errors.New("价格周期不能写「免费」，免费机器请勾选免费（free）")
	}
	if b.TrafficMode != "sum" && b.TrafficMode != "max" && b.TrafficMode != "in" && b.TrafficMode != "out" {
		return errors.New("流量方式须为 sum/max/in/out")
	}
	if b.ExpiresAt != "" {
		if _, err := parseUTC(b.ExpiresAt); err != nil {
			return errors.New("到期时间须为 RFC3339（精确到秒）")
		}
	}
	return nil
}

func (l Line) Validate() error {
	if l.Mbps < 0 || l.Mbps > MaxLineMbps || l.ThrottleMbps < 0 || l.ThrottleMbps > MaxLineMbps {
		return errors.New("带宽须为 0–1000000 Mbit/s")
	}
	if l.ThrottleHours < 0 || l.ThrottleHours > MaxThrottleHours {
		return errors.New("降速统计窗口须为 0–168 小时")
	}
	if l.ThrottleMbps > 0 && l.Mbps > 0 && l.ThrottleMbps > l.Mbps {
		return errors.New("降速阈值不能高于线路带宽")
	}
	if l.ThrottleOn != "" && l.ThrottleOn != "bandwidth" && l.ThrottleOn != "quota" {
		return errors.New("降速触发条件须为 bandwidth 或 quota")
	}
	if l.ThrottleOn == "quota" && l.ThrottleMbps <= 0 {
		return errors.New("按周期额度降速需填写用满后的速率")
	}
	return nil
}

func (a Alerts) Validate() error {
	if a.OfflineSeconds < 0 || a.OfflineSeconds > 86400 || a.RepeatSeconds < 0 || a.RepeatSeconds > 604800 || (a.RepeatSeconds > 0 && a.RepeatSeconds < 300) {
		return errors.New("离线等待须为 0–86400 秒，重复间隔为 0 或 300–604800 秒")
	}
	if a.RenewalDays < 0 || a.RenewalDays > 365 || !percent(a.TrafficPercent) || !percent(a.CPUPercent) || !percent(a.MemoryPercent) || a.HoldSeconds < 0 || a.HoldSeconds > 86400 {
		return errors.New("告警阈值须在 0–100，持续时间须在 0–86400 秒")
	}
	if (a.Traffic && a.TrafficPercent == 0) || (a.Load && (a.CPUPercent == 0 || a.MemoryPercent == 0 || a.HoldSeconds < 30)) {
		return errors.New("启用告警需设置有效阈值；负载持续时间至少 30 秒")
	}
	return nil
}

// Normalized is the node as it is stored: a free machine keeps no price or
// price period, whatever the form or a partial MCP edit still carried.
func (n Node) Normalized() Node {
	// 卡片地区要与任务目标的地区逐字相同才对得上，首尾空白是最常见的静默失配。
	n.Display.RouteRegion = strings.TrimSpace(n.Display.RouteRegion)
	n.Display.LatencyRegion = strings.TrimSpace(n.Display.LatencyRegion)
	if n.Billing.Free {
		n.Billing.Price, n.Billing.PricePeriod = 0, ""
	}
	return n
}

// 达量降速（bandwidth-based throttling）的边界：阈值与线路带宽都以 Mbit/s 记，
// 窗口以小时记（默认 24，与德国商家常见口径一致）。
const (
	DefaultThrottleHours = 24
	MaxThrottleHours     = 168
	MaxLineMbps          = 1000000
)

// WindowHours is the throttle window actually used: 0 falls back to the default.
func (l Line) WindowHours() int {
	if l.ThrottleHours <= 0 {
		return DefaultThrottleHours
	}
	return l.ThrottleHours
}

// ThrottlesOnQuota reports whether this line's rule is the cycle volume running
// out (unmetered afterwards but rate-capped) instead of the window average
// reaching a bandwidth threshold.
func (l Line) ThrottlesOnQuota() bool { return l.ThrottleOn == "quota" }

// ThrottleActive reports whether the line's own rule is in effect right now.
// The two shapes read different facts: the bandwidth rule compares the window
// average, the quota rule the cycle total — both are already on the view, so a
// node with no hourly buckets can still be throttled by volume.
func (v NodeView) ThrottleActive() bool {
	if v.Line.ThrottleMbps <= 0 {
		return false
	}
	if v.Line.ThrottlesOnQuota() {
		return v.Billing.TrafficLimit > 0 && v.TrafficUsed >= v.Billing.TrafficLimit
	}
	return v.AvgBits >= float64(v.Line.ThrottleMbps)*1e6
}

// HumanBytes renders a byte figure the way the notes do (decimal, 3 significant
// digits at most) for alert messages such as「本周期用量已达 1.00 TB 额度」.
func HumanBytes(n int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	value := float64(n)
	i := 0
	for value >= 1000 && i < len(units)-1 {
		value /= 1000
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}
	if value < 100 {
		return fmt.Sprintf("%.1f %s", value, units[i])
	}
	return fmt.Sprintf("%.0f %s", value, units[i])
}

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

var AllCapabilities = []string{CapabilityUnlock, CapabilityParallelJobs}

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
type NodeView struct {
	Node
	Online      bool     `json:"online"`
	LastSeen    int64    `json:"last_seen"`
	Metrics     *Metrics `json:"metrics"`
	IPFamilies  []string `json:"ip_families"`
	CycleStart  int64    `json:"cycle_start"`
	CycleEnd    int64    `json:"cycle_end"`
	TrafficUsed int64    `json:"traffic_used"`
	// AvgBits / CoveredHours / Throttled describe the throttle window: the average
	// transfer rate over that window (bits per second, computed the same way as
	// TrafficUsed, i.e. by the node's traffic_mode), how many hours of it the host
	// actually reported, and whether the line's rule is in effect.
	AvgBits      float64 `json:"avg_bps"`
	CoveredHours int     `json:"covered_hours"`
	WindowHours  int     `json:"window_hours"`
	Throttled    bool    `json:"throttled"`
	// ProbeTargets lists what this host is actually told to probe: one entry per distinct
	// kind / carrier / family / region among the enabled tasks bound to it. The public card
	// draws a latency or backhaul cell only for an entry it finds here, so a host left out of
	// every task shows no empty cells at all. It comes from the task definitions rather than
	// from stored jobs because a job only exists once the agent leases: derived from jobs, the
	// card would paint short and grow a poll later.
	ProbeTargets []ProbeTarget `json:"probe_targets,omitempty"`
	// Unlock is the latest per-service verdict of this machine's own egress, nil
	// when it has never run the check. The panel draws the score from it and the
	// detail panel the per-service list.
	Unlock *UnlockSummary `json:"unlock,omitempty"`
	// CreatedAt is server-kept (the offline alert waits for a new node's first
	// report); it never leaves the Server.
	CreatedAt int64 `json:"-"`
}

// Redact is the view an anonymous visitor gets: alert rules stay with the
// administrator.
func (v NodeView) Redact() NodeView {
	v.Alerts = Alerts{}
	return v
}

// ProbeTarget is one probe entry a node is bound to, without the address: the card only needs
// to know which carrier / family / region cell exists (see NodeView.ProbeTargets).
type ProbeTarget struct {
	Kind    string `json:"kind"`
	Carrier string `json:"carrier"`
	Family  string `json:"family"`
	Region  string `json:"region"`
}
type Target struct {
	Host    string `json:"host"`
	Port    int    `json:"port"`
	Family  string `json:"family"`
	Region  string `json:"region"`
	Carrier string `json:"carrier"`
}

// Task kinds: a probe task runs TCP latency and/or route checks against a
// target list; an unlock task checks the built-in service catalogue from each
// bound node's own egress and has no targets.
const (
	TaskProbe  = "probe"
	TaskUnlock = "unlock"
)

// Task is a management task as the panel and MCP edit it: a tagged union whose
// Type says which of Probe / Unlock carries the definition. The scheduler never
// runs a Task directly; Leaves expands it into independently scheduled Probes.
type Task struct {
	ID      string      `json:"id"`
	Name    string      `json:"name"`
	Type    string      `json:"type"`
	Enabled bool        `json:"enabled"`
	NodeIDs []string    `json:"node_ids"`
	Probe   *ProbeSpec  `json:"probe,omitempty"`
	Unlock  *UnlockSpec `json:"unlock,omitempty"`
}

// ProbeSpec is a probe task's definition: every check runs against every
// target. Interval is the TCP latency period, RouteInterval the route period;
// each only matters when its check is selected.
type ProbeSpec struct {
	Checks        []string `json:"checks"`
	Targets       []Target `json:"targets"`
	Interval      int      `json:"interval,omitempty"`
	RouteInterval int      `json:"route_interval,omitempty"`
}

// UnlockSpec is an unlock task's definition. An empty Family lets the Server
// pick per node: IPv4, or IPv6 on a host without IPv4 (see storage.Lease).
type UnlockSpec struct {
	Interval int    `json:"interval"`
	Family   string `json:"family,omitempty"`
}

// Probe is one independently scheduled check: what an Agent leases (Job.Task)
// and what an observation reports. Its JSON is the Agent wire contract of
// protocol 1 — the same fields the single-probe task had — so Agents already in
// the field decode and validate it unchanged. ID is the parent task's id.
type Probe struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Family   string `json:"family"`
	Region   string `json:"region"`
	Carrier  string `json:"carrier"`
	Interval int    `json:"interval"`
}

// Leaves resolves a task into its probes: checks × targets for a probe task,
// a single catalogue round for an unlock task.
func (t Task) Leaves() []Probe {
	switch {
	case t.Type == TaskUnlock && t.Unlock != nil:
		return []Probe{{ID: t.ID, Name: t.Name, Kind: UnlockKind, Family: t.Unlock.Family, Interval: t.Unlock.Interval}}
	case t.Type == TaskProbe && t.Probe != nil:
		out := []Probe{}
		for _, target := range t.Probe.Targets {
			for _, kind := range t.Probe.Checks {
				leaf := Probe{ID: t.ID, Name: t.Name, Kind: kind, Host: target.Host, Port: target.Port,
					Family: target.Family, Region: target.Region, Carrier: target.Carrier, Interval: t.Probe.Interval}
				if kind == RouteKind {
					leaf.Interval = t.Probe.RouteInterval
				}
				out = append(out, leaf)
			}
		}
		return out
	}
	return nil
}

// ProbeKey identifies one probe of one task across leases and results; the
// latest table, retries and history are keyed by it.
func (p Probe) ProbeKey() string {
	return fmt.Sprintf("%s|%s|%s|%s|%d|%s|%s", p.ID, p.Kind, p.Family, p.Host, p.Port, p.Region, p.Carrier)
}

// ProbeKeys is the set of probe keys a task schedules: two definitions with the
// same set probe the same things, whatever else (name, intervals, bindings)
// changed between them.
func (t Task) ProbeKeys() map[string]bool {
	out := map[string]bool{}
	for _, leaf := range t.Leaves() {
		out[leaf.ProbeKey()] = true
	}
	return out
}

func (t Task) Validate() error {
	if strings.TrimSpace(t.Name) == "" || len(t.Name) > 100 {
		return errors.New("任务名称必填且不超过 100 字节")
	}
	switch t.Type {
	case TaskProbe:
		if t.Probe == nil || t.Unlock != nil {
			return errors.New("网络探测任务须给 probe（检测内容与目标），不能带 unlock")
		}
		p := t.Probe
		if len(p.Checks) < 1 || len(p.Checks) > 2 || len(p.Targets) < 1 || len(p.Targets) > 200 {
			return errors.New("请选择检测内容和目标（最多 200 个地址）")
		}
		for _, check := range p.Checks {
			if check != "tcp" && check != RouteKind {
				return errors.New("检测内容须为 tcp（延迟）或 route（回程）")
			}
		}
		seen := map[string]bool{}
		for _, leaf := range t.Leaves() {
			if seen[leaf.ProbeKey()] {
				return errors.New("检测内容或目标重复")
			}
			seen[leaf.ProbeKey()] = true
			if err := leaf.Validate(); err != nil {
				return err
			}
		}
	case TaskUnlock:
		if t.Unlock == nil || t.Probe != nil {
			return errors.New("解锁检测任务须给 unlock（间隔与协议），不能带 probe")
		}
		if err := t.Leaves()[0].Validate(); err != nil {
			return err
		}
	default:
		return errors.New("任务类型须为 probe（网络探测）或 unlock（解锁检测）")
	}
	if len(t.NodeIDs) > 1000 {
		return errors.New("绑定节点过多")
	}
	seen := map[string]bool{}
	for _, id := range t.NodeIDs {
		if id == "" || seen[id] {
			return errors.New("节点绑定重复或为空")
		}
		seen[id] = true
	}
	return nil
}

// Validate checks one probe. The Agent runs it on every job it leases, so the
// rules here are also what a lease must satisfy on the wire.
func (p Probe) Validate() error {
	if len(p.Name) == 0 || len(p.Name) > 100 {
		return errors.New("任务名称必填且不超过 100 字节")
	}
	switch p.Kind {
	case UnlockKind:
		// 解锁检测是按「这台机器的出口」逐项发起的 HTTP 检测，没有目标地址，
		// 也不需要端口；协议族留空即自动：走 IPv4，纯 IPv6 机器由 Lease 改派 IPv6。
		if p.Interval < UnlockMinInterval || p.Interval > 86400 {
			return errors.New("解锁检测的间隔无效：最短 1 小时")
		}
		if p.Family != "" && p.Family != "ipv4" && p.Family != "ipv6" {
			return errors.New("IP 协议须为 ipv4 或 ipv6（留空即自动）")
		}
		return nil
	case "tcp", RouteKind:
	default:
		return errors.New("检测类型须为 tcp、route 或 unlock")
	}
	min := 30
	if p.Kind == RouteKind {
		min = 900
	}
	if p.Interval < min || p.Interval > 86400 || p.Port < 1 || p.Port > 65535 || len(p.Region) > 60 {
		return errors.New("端口或间隔无效：TCP 最短 30 秒，回程最短 900 秒")
	}
	if p.Family != "ipv4" && p.Family != "ipv6" {
		return errors.New("IP 协议须为 ipv4 或 ipv6")
	}
	if p.Kind == RouteKind && (p.Region == "" || (p.Carrier != "电信" && p.Carrier != "联通" && p.Carrier != "移动" && p.Carrier != "其他")) {
		return errors.New("回程任务需指定地区和运营商（含其他）")
	}
	if len(p.Carrier) > 20 || len(p.Host) == 0 || len(p.Host) > 253 || strings.TrimSpace(p.Host) != p.Host {
		return errors.New("目标无效")
	}
	if ip := net.ParseIP(p.Host); ip != nil {
		if (p.Family == "ipv4") != (ip.To4() != nil) {
			return errors.New("目标与 IP 协议不匹配")
		}
		return nil
	}
	for _, label := range strings.Split(p.Host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return errors.New("域名无效")
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return errors.New("目标须为 IP 或 ASCII 域名")
			}
		}
	}
	return nil
}

type Job struct {
	ID   string `json:"id"`
	Task Probe  `json:"task"`
}
type Hop struct {
	TTL     int     `json:"ttl"`
	Address string  `json:"address"`
	ASN     string  `json:"asn"`
	RTT     float64 `json:"rtt"`
}
type Result struct {
	Reason   string   `json:"reason,omitempty"`
	JobID    string   `json:"job_id"`
	Status   string   `json:"status"`
	Latency  *float64 `json:"latency"`
	Sent     int      `json:"sent"`
	Received int      `json:"received"`
	Route    string   `json:"route"`
	Hops     []Hop    `json:"hops"`
	// Unlocks is the per-service verdict of an unlock job: what this node's own
	// egress can reach, which region each service answered for, and why not.
	Unlocks []UnlockResult `json:"unlocks,omitempty"`
}

func (r Result) Validate() error {
	switch r.Reason {
	case "", "dns", "permission", "tool_missing", "tool_failed", "timeout", "connect_failed", "synthetic_dns":
	default:
		return errors.New("invalid result reason")
	}

	if r.Status != "ok" && r.Status != "timeout" && r.Status != "unavailable" && r.Status != "failed" {
		return errors.New("invalid result status")
	}
	// A latency probe proves itself with a response and a round trip; an unlock
	// job has neither, it proves itself with the verdicts it carries.
	if r.Status == "ok" && len(r.Unlocks) == 0 && (r.Received == 0 || r.Latency == nil) {
		return errors.New("successful result requires a response and latency")
	}
	if len(r.Unlocks) > len(UnlockCatalog) {
		return errors.New("invalid unlock result")
	}
	for _, u := range r.Unlocks {
		if !KnownUnlockService(u.Service) || !ValidUnlockStatus(u.Status) || len(u.Region) > 40 || len(u.Detail) > 80 {
			return errors.New("invalid unlock result")
		}
	}
	if r.Sent < 0 || r.Sent > 5 || r.Received < 0 || r.Received > r.Sent || len(r.Hops) > 30 || len(r.Route) > 160 {
		return errors.New("invalid result")
	}
	if r.Latency != nil && (!finite(*r.Latency) || *r.Latency < 0 || *r.Latency > 60000) {
		return errors.New("invalid latency")
	}
	for _, h := range r.Hops {
		if h.TTL < 1 || h.TTL > 30 || len(h.Address) > 100 || len(h.ASN) > 20 || !finite(h.RTT) || h.RTT < 0 || h.RTT > 60000 {
			return errors.New("invalid hop")
		}
	}
	return nil
}

type Observation struct {
	Task   Probe  `json:"task"`
	At     int64  `json:"at"`
	Result Result `json:"result"`
	Stale  bool   `json:"stale"`
}

// Redact returns the observation as the public contract exposes it: hop
// addresses keep only their masked prefix, and a stale or
// failed probe reports no route instead of the label of an older, successful run.
// The hop slice is masked in place, which is what the shared caller already owned.
func (o Observation) Redact() Observation {
	for i := range o.Result.Hops {
		hop := &o.Result.Hops[i]
		hop.Address = PublicAddress(hop.Address)
		hop.ASN = NormalizeASN(hop.ASN)
	}
	if o.Stale || o.Result.Status != "ok" {
		o.Result.Route = "未知"
	}
	return o
}

// EventDeliveryWindow is how long an alert stays worth sending. Events pile up
// while no channel is configured; setting one up later must not replay weeks
// of offline and recovery notices, so older events are left undelivered.
const EventDeliveryWindow = 24 * time.Hour

type Event struct {
	LastError string `json:"last_error"`
	ID        int64  `json:"id"`
	NodeID    string `json:"node_id"`
	NodeName  string `json:"node_name"`
	Kind      string `json:"kind"`
	Message   string `json:"message"`
	Active    bool   `json:"active"`
	At        int64  `json:"at"`
	Delivered bool   `json:"delivered"`
	Attempts  int    `json:"attempts"`
}

// EventQuery pages the event log: newest first, ids below Before (0 = from the
// newest), optionally one node or one kind.
type EventQuery struct {
	Before int64
	Limit  int
	NodeID string
	Kind   string
}

const (
	DefaultEventPage = 100
	MaxEventPage     = 500
)

// Normalize clamps the page size so a caller cannot ask for the whole log.
func (q EventQuery) Normalize() EventQuery {
	if q.Limit <= 0 {
		q.Limit = DefaultEventPage
	}
	q.Limit = min(q.Limit, MaxEventPage)
	q.Before = max(q.Before, 0)
	return q
}

func Secret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func Hash(s string) string   { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func finite(v float64) bool  { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func percent(v float64) bool { return finite(v) && v >= 0 && v <= 100 }

// parseUTC accepts second-precision timestamps and returns them in UTC.
func parseUTC(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

// Cycle uses UTC monthly boundaries anchored at an instant: the anchor's day and
// time of day repeat every month, clamping days such as the 31st in February.
// The anchor keeps its seconds, so a cycle can start at e.g. 12:30:45 UTC.
func Cycle(now time.Time, anchor time.Time) (time.Time, time.Time) {
	now, anchor = now.UTC(), anchor.UTC()
	shift := func(months int) time.Time {
		year, month := anchor.Year(), int(anchor.Month())-1+months
		year += month / 12
		month = ((month % 12) + 12) % 12
		last := time.Date(year, time.Month(month+1)+1, 0, 0, 0, 0, 0, time.UTC).Day()
		return time.Date(year, time.Month(month+1), min(anchor.Day(), last),
			anchor.Hour(), anchor.Minute(), anchor.Second(), 0, time.UTC)
	}
	// Months between the anchor month and now, corrected by one when the day
	// clamp pushes the boundary past now.
	index := (now.Year()-anchor.Year())*12 + int(now.Month()) - int(anchor.Month())
	if shift(index).After(now) {
		index--
	}
	return shift(index), shift(index + 1)
}

// BillingAnchor parses a stored cycle anchor; the zero value falls back to the
// first of the current month so a config without one still bills monthly.
func BillingAnchor(at string, now time.Time) time.Time {
	parsed, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return time.Date(now.UTC().Year(), now.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	return parsed.UTC()
}
func Traffic(in, out int64, mode string) int64 {
	switch mode {
	case "in":
		return in
	case "out":
		return out
	case "max":
		return max(in, out)
	default:
		return in + out
	}
}

type NetworkSample struct {
	At      int64    `json:"at"`
	Latency *float64 `json:"latency"`
	Failure *float64 `json:"failure"`
}
type NetworkSeries struct {
	Name    string          `json:"name"`
	Family  string          `json:"family"`
	Host    string          `json:"host"`
	Port    int             `json:"port"`
	Region  string          `json:"region"`
	Carrier string          `json:"carrier"`
	Samples []NetworkSample `json:"samples"`
}

// LatencySample is one finished TCP probe. The public card draws each probe as
// its own cell, so the cell colours describe individual measurements instead of
// a single aggregated gauge. Latency is nil when that probe failed.
type LatencySample struct {
	NodeID   string   `json:"node_id"`
	Carrier  string   `json:"carrier"`
	Region   string   `json:"region"`
	Family   string   `json:"family"`
	At       int64    `json:"at"`
	Latency  *float64 `json:"latency"`
	Sent     int      `json:"sent"`
	Received int      `json:"received"`
}

// UnlockKind is the task kind that runs the streaming / AI / exchange unlock
// checks from a node's own egress. It has no target: the service list is the
// built-in catalogue.
const UnlockKind = "unlock"

// RouteKind 是回程（traceroute 分类）任务的 kind：面板上每个运营商一行结论，
// 一条没测出骨干证据的 trace 就会把那一行写成「未知」。见 storage 的补测逻辑。
const RouteKind = "route"

// UnlockMinInterval is the shortest unlock interval. One round is a couple of
// dozen HTTPS requests from the node's own IP, so this is deliberately an order
// of magnitude coarser than a latency probe.
const UnlockMinInterval = 3600

// Unlock verdicts. yes/no are the plain answers, originals_only is Netflix's
// region-limited catalogue (the IP is recognised, the full library is not),
// banned is an outright block of the address, failed is "the check itself could
// not complete" and must never be read as a verdict about the IP.
const (
	UnlockYes           = "yes"
	UnlockNo            = "no"
	UnlockOriginalsOnly = "originals_only"
	UnlockBanned        = "banned"
	UnlockFailed        = "failed"
)

// UnlockResult is one service's verdict from one node.
type UnlockResult struct {
	Service string `json:"service"`
	Status  string `json:"status"`
	Region  string `json:"region,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// Unlocked reports whether the verdict counts as unlocked. originals_only
// counts: 妙妙屋 scores Netflix's limited catalogue as a hit too, so the two
// panels agree on the same machine.
func (u UnlockResult) Unlocked() bool {
	return u.Status == UnlockYes || u.Status == UnlockOriginalsOnly
}

// UnlockSummary is what the panel needs: the score, the catalogue size and the
// per-service verdicts behind it.
type UnlockSummary struct {
	Services []UnlockResult `json:"services"`
	Score    int            `json:"score"`
	Total    int            `json:"total"`
	TestedAt int64          `json:"tested_at"`
}

// SummarizeUnlocks fills in the score. Total is the catalogue size (24), not
// the number of services the node reported: a node that answered 20 of 24 is
// scored out of 24, exactly like the panel it was ported from.
func SummarizeUnlocks(services []UnlockResult, testedAt int64) *UnlockSummary {
	if len(services) == 0 {
		return nil
	}
	sum := &UnlockSummary{Services: services, Total: len(UnlockCatalog), TestedAt: testedAt}
	for _, u := range services {
		if u.Unlocked() {
			sum.Score++
		}
	}
	return sum
}

func ValidUnlockStatus(status string) bool {
	switch status {
	case UnlockYes, UnlockNo, UnlockOriginalsOnly, UnlockBanned, UnlockFailed:
		return true
	}
	return false
}

type TaskRun struct {
	NodeID   string `json:"node_id"`
	NodeName string `json:"node_name"`
	Kind     string `json:"kind"`
	Family   string `json:"family"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Region   string `json:"region"`
	Carrier  string `json:"carrier"`
	Issued   int64  `json:"issued"`
	Finished int64  `json:"finished"`
	Status   string `json:"status"`
	Reason   string `json:"reason"`
}
