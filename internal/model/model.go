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
	Load           bool    `json:"load"`
	CPUPercent     float64 `json:"cpu_percent"`
	MemoryPercent  float64 `json:"memory_percent"`
	HoldSeconds    int     `json:"hold_seconds"`
}
type Node struct {
	TrafficAdjustment int64  `json:"traffic_adjustment"`
	AdjustmentCycle   int64  `json:"adjustment_cycle"`
	ID                string `json:"id"`
	Name              string `json:"name"`
	Group             string `json:"group"`
	Region            string `json:"region"`
	Order             int    `json:"order"`
	Public            bool   `json:"public"`
	Representative    string `json:"representative"`
	// LatencyRegion picks which region's TCP latency the public card shows.
	// Empty falls back to Representative, then to every region.
	LatencyRegion   string   `json:"latency_region"`
	LatencyCarriers []string `json:"latency_carriers,omitempty"`
	TrafficLimit    int64    `json:"traffic_limit"`
	TrafficMode     string   `json:"traffic_mode"`
	// BillingAt is the cycle anchor: its day and time of day repeat every month
	// (RFC3339, second precision). See Cycle.
	BillingAt   string  `json:"billing_at"`
	Price       float64 `json:"price"`
	Currency    string  `json:"currency"`
	PricePeriod string  `json:"price_period"`
	ExpiresAt   string  `json:"expires_at"`
	Alerts      Alerts  `json:"alerts"`
	CreatedAt   int64   `json:"created_at"`
}

func (n Node) Validate() error {
	if strings.TrimSpace(n.Name) == "" || len(n.Name) > 100 || len(n.Group) > 100 || len(n.Region) > 60 || len(n.Representative) > 60 || len(n.LatencyRegion) > 60 {
		return errors.New("名称必填且不超过 100 字节；分组、地区过长")
	}
	seenCarriers := map[string]bool{}
	for _, carrier := range n.LatencyCarriers {
		if (carrier != "电信" && carrier != "联通" && carrier != "移动" && carrier != "其他") || seenCarriers[carrier] {
			return errors.New("延迟展示运营商无效或重复")
		}
		seenCarriers[carrier] = true
	}
	if _, err := parseUTC(n.BillingAt); err != nil {
		return errors.New("重置时间须为 RFC3339（精确到秒）")
	}
	if n.TrafficLimit < 0 || n.Price < 0 || !finite(n.Price) || len(n.Currency) > 12 || len(n.PricePeriod) > 30 {
		return errors.New("计费配置无效")
	}
	if n.TrafficMode != "sum" && n.TrafficMode != "max" && n.TrafficMode != "in" && n.TrafficMode != "out" {
		return errors.New("流量方式须为 sum/max/in/out")
	}
	if n.ExpiresAt != "" {
		if _, err := parseUTC(n.ExpiresAt); err != nil {
			return errors.New("到期时间须为 RFC3339（精确到秒）")
		}
	}
	a := n.Alerts
	if a.OfflineSeconds < 0 || a.OfflineSeconds > 86400 || a.RepeatSeconds < 0 || a.RepeatSeconds > 604800 || (a.RepeatSeconds > 0 && a.RepeatSeconds < 300) || n.TrafficAdjustment < -1e16 || n.TrafficAdjustment > 1e16 {
		return errors.New("离线等待须为 0–86400 秒，重复间隔为 0 或 300–604800 秒，用量校正超出范围")
	}
	if a.RenewalDays < 0 || a.RenewalDays > 365 || !percent(a.TrafficPercent) || !percent(a.CPUPercent) || !percent(a.MemoryPercent) || a.HoldSeconds < 0 || a.HoldSeconds > 86400 {
		return errors.New("告警阈值须在 0–100，持续时间须在 0–86400 秒")
	}
	if (a.Traffic && a.TrafficPercent == 0) || (a.Load && (a.CPUPercent == 0 || a.MemoryPercent == 0 || a.HoldSeconds < 30)) {
		return errors.New("启用告警需设置有效阈值；负载持续时间至少 30 秒")
	}
	return nil
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
	Version     string  `json:"version"`
	// IPFamilies is the set of usable public address families the Agent
	// reported for this host: the host has an address of that family and the
	// kernel has a route for it. nil means the Agent has not reported them yet;
	// an empty non-nil slice means the host can probe nothing.
	IPFamilies []string `json:"ip_families"`
}

func (m Metrics) Validate() error {
	if len(m.CounterID) > 64 || !percent(m.CPU) || m.MemoryTotal == 0 || m.MemoryUsed > m.MemoryTotal || m.DiskUsed > m.DiskTotal || !finite(m.RateIn) || !finite(m.RateOut) || m.RateIn < 0 || m.RateOut < 0 || len(m.OS) > 100 || len(m.Arch) > 30 || len(m.Version) > 40 || m.NetIn > math.MaxInt64 || m.NetOut > math.MaxInt64 {
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
	return nil
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
}
type Target struct {
	Host    string `json:"host"`
	Port    int    `json:"port"`
	Family  string `json:"family"`
	Region  string `json:"region"`
	Carrier string `json:"carrier"`
}

type Task struct {
	Checks        []string `json:"checks,omitempty"`
	Targets       []Target `json:"targets,omitempty"`
	RouteInterval int      `json:"route_interval,omitempty"`
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Kind          string   `json:"kind"`
	Host          string   `json:"host"`
	Port          int      `json:"port"`
	Family        string   `json:"family"`
	Region        string   `json:"region"`
	Carrier       string   `json:"carrier"`
	Interval      int      `json:"interval"`
	Enabled       bool     `json:"enabled"`
	NodeIDs       []string `json:"node_ids"`
}

// Leaves resolves a management task into bounded, independently scheduled probes.
func (t Task) Leaves() []Task {
	if len(t.Checks) == 0 && len(t.Targets) == 0 {
		return []Task{t}
	}
	out := []Task{}
	for _, target := range t.Targets {
		for _, kind := range t.Checks {
			leaf := t
			leaf.Checks = nil
			leaf.Targets = nil
			leaf.RouteInterval = 0
			leaf.Kind = kind
			leaf.Host = target.Host
			leaf.Port = target.Port
			leaf.Family = target.Family
			leaf.Region = target.Region
			leaf.Carrier = target.Carrier
			if kind == "route" {
				leaf.Interval = t.RouteInterval
			}
			out = append(out, leaf)
		}
	}
	return out
}
func (t Task) ProbeKey() string {
	return fmt.Sprintf("%s|%s|%s|%s|%d|%s|%s", t.ID, t.Kind, t.Family, t.Host, t.Port, t.Region, t.Carrier)
}
func (t Task) Validate() error {
	if len(t.Checks) > 0 || len(t.Targets) > 0 {
		if len(t.Checks) < 1 || len(t.Checks) > 2 || len(t.Targets) < 1 || len(t.Targets) > 200 {
			return errors.New("请选择检测内容和目标（最多 200 个地址）")
		}
		for _, check := range t.Checks {
			// 名字写错时直接指出是哪一项，否则错误会由叶子任务报成「任务类型须为 tcp 或 route」。
			if check != "tcp" && check != "route" {
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
		return nil
	}
	if t.Kind != "tcp" && t.Kind != "route" {
		return errors.New("任务类型须为 tcp 或 route")
	}
	min := 30
	if t.Kind == "route" {
		min = 900
	}
	if t.Interval < min || t.Interval > 86400 || t.Port < 1 || t.Port > 65535 || len(t.Name) == 0 || len(t.Name) > 100 || len(t.Region) > 60 {
		return errors.New("名称、端口或间隔无效：TCP 最短 30 秒，回程最短 900 秒")
	}
	if t.Family != "ipv4" && t.Family != "ipv6" {
		return errors.New("IP 协议须为 ipv4 或 ipv6")
	}
	if t.Kind == "route" && (t.Region == "" || (t.Carrier != "电信" && t.Carrier != "联通" && t.Carrier != "移动" && t.Carrier != "其他")) {
		return errors.New("回程任务需指定地区和运营商（含其他）")
	}
	if len(t.Carrier) > 20 || len(t.Host) == 0 || len(t.Host) > 253 || strings.TrimSpace(t.Host) != t.Host {
		return errors.New("目标无效")
	}
	if ip := net.ParseIP(t.Host); ip != nil {
		if (t.Family == "ipv4") != (ip.To4() != nil) {
			return errors.New("目标与 IP 协议不匹配")
		}
	} else {
		for _, label := range strings.Split(t.Host, ".") {
			if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return errors.New("域名无效")
			}
			for _, c := range label {
				if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
					return errors.New("目标须为 IP 或 ASCII 域名")
				}
			}
		}
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

type Job struct {
	ID   string `json:"id"`
	Task Task   `json:"task"`
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
	if r.Status == "ok" && (r.Received == 0 || r.Latency == nil) {
		return errors.New("successful result requires a response and latency")
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
	Task   Task   `json:"task"`
	At     int64  `json:"at"`
	Result Result `json:"result"`
	Stale  bool   `json:"stale"`
}

// Redact returns the observation as the public contract exposes it: the bound
// node ids drop out, hop addresses keep only their masked prefix, and a stale or
// failed probe reports no route instead of the label of an older, successful run.
// The hop slice is masked in place, which is what the shared caller already owned.
func (o Observation) Redact() Observation {
	o.Task.NodeIDs = nil
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

type TaskAction struct {
	Action  string   `json:"action"`
	NodeIDs []string `json:"node_ids"`
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
