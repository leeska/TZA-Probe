package model

import (
	"errors"
	"fmt"
	"net"
	"strings"
)

type Target struct {
	Host    string `json:"host"`
	Port    int    `json:"port"`
	Family  string `json:"family"`
	Region  string `json:"region"`
	Carrier string `json:"carrier"`
	// Checks narrows the task's checks for this one target (e.g. latency only
	// for an overseas service whose route trace says nothing about the China
	// backbones). Empty means every check of the task; otherwise it must be a
	// non-empty subset of ProbeSpec.Checks.
	Checks []string `json:"checks,omitempty"`
}

// runs reports whether this target runs the given check of its task.
func (t Target) runs(kind string) bool {
	if len(t.Checks) == 0 {
		return true
	}
	for _, c := range t.Checks {
		if c == kind {
			return true
		}
	}
	return false
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
// target, unless a target narrows them with its own Checks. Interval is the TCP latency period, RouteInterval the route period;
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
				if !target.runs(kind) {
					continue
				}
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
		// A target's own checks may only narrow the task's; a non-empty list
		// therefore always keeps at least one check for that target.
		for _, target := range p.Targets {
			for _, check := range target.Checks {
				in := false
				for _, c := range p.Checks {
					in = in || c == check
				}
				if !in {
					return fmt.Errorf("目标 %s 的检测项 %q 不在任务的检测内容里", target.Host, check)
				}
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

// TaskHealth sums up one task's latest attempt per bound node and probe target
// (the rows TaskRuns lists one by one) for the task list.
type TaskHealth struct {
	OK      int   `json:"ok"`
	Failed  int   `json:"failed"`
	Pending int   `json:"pending"`
	Last    int64 `json:"last_finished,omitempty"`
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
