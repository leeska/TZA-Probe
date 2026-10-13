package model

import (
	"errors"
	"strings"
)

// Node is a host's configuration as the panel and MCP edit it, grouped by
// concern: what the public card shows (Display), what the line costs and how
// its cycle counts (Billing) and the line's own bandwidth rule (Line). Alert
// rules are stored on their own and cover nodes by scope (see AlertRule).
// Server-kept state — creation time and the cycle correction — is not part of
// it (see storage).
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

type NodeView struct {
	Node
	// Alerts is the rules in effect on this node, one per kind at most,
	// resolved from the rule list by scope (see EffectiveAlerts).
	Alerts      Alerts   `json:"alerts"`
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

// LiveView is the part of a node view that moves every second: what the
// public panel's one-second tick redraws. Everything else on NodeView changes
// only when a node or task is edited.
type LiveView struct {
	ID           string   `json:"id"`
	Online       bool     `json:"online"`
	LastSeen     int64    `json:"last_seen"`
	Metrics      *Metrics `json:"metrics"`
	TrafficUsed  int64    `json:"traffic_used"`
	AvgBits      float64  `json:"avg_bps"`
	CoveredHours int      `json:"covered_hours"`
	WindowHours  int      `json:"window_hours"`
	Throttled    bool     `json:"throttled"`
}

func (v NodeView) Live() LiveView {
	return LiveView{ID: v.ID, Online: v.Online, LastSeen: v.LastSeen, Metrics: v.Metrics, TrafficUsed: v.TrafficUsed,
		AvgBits: v.AvgBits, CoveredHours: v.CoveredHours, WindowHours: v.WindowHours, Throttled: v.Throttled}
}

// Redact is the view an anonymous visitor gets: alert rules stay with the
// administrator.
func (v NodeView) Redact() NodeView {
	v.Alerts = nil
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
