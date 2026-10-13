package model

import (
	"encoding/json"
	"errors"
	"math"
	"slices"
)

// Alert rule kinds. A node takes at most one rule of each kind: when several
// cover it, the narrowest scope wins (see EffectiveAlerts).
const (
	AlertOffline  = "offline"  // 离线：HoldSeconds 是判定离线后再等的秒数
	AlertRenewal  = "renewal"  // 续费：Threshold 是到期前多少天
	AlertTraffic  = "traffic"  // 流量：Threshold 是本周期用量占额度的百分比
	AlertThrottle = "throttle" // 达量降速：按线路自己的降速规则，无参数
	AlertCPU      = "cpu"      // CPU：Threshold 百分比，持续 HoldSeconds 秒
	AlertMemory   = "memory"   // 内存：Threshold 百分比，持续 HoldSeconds 秒
)

// AlertKinds is every rule kind, in the order the panel lists them.
var AlertKinds = []string{AlertOffline, AlertRenewal, AlertTraffic, AlertThrottle, AlertCPU, AlertMemory}

// AlertLabels names each kind in error messages.
var AlertLabels = map[string]string{
	AlertOffline: "离线", AlertRenewal: "续费", AlertTraffic: "流量",
	AlertThrottle: "达量降速", AlertCPU: "CPU", AlertMemory: "内存",
}

// Rule scopes, from widest to narrowest. For one kind a node follows the
// narrowest rule that covers it: 指定节点 beats 分组 beats 全部节点.
const (
	ScopeAll   = "all"   // 全部节点（含以后新加的）
	ScopeGroup = "group" // Groups 里列出的分组；"" 是未分组（面板上叫「默认」）
	ScopeNodes = "nodes" // NodeIDs 里列出的节点
)

// MaxAlertRules bounds the rule list; a fleet needs a handful per kind.
const MaxAlertRules = 200

// MaxRuleList bounds each of a rule's groups / node_ids / exclude_node_ids.
const MaxRuleList = 500

// AlertRule is one alert rule and the nodes it covers. Which of Threshold /
// HoldSeconds a kind takes is listed on the kind constants; RepeatSeconds
// re-sends a still-active alert every that many seconds (0 sends it once).
// Exclude takes nodes out of an all / group rule; a node-scoped rule lists its
// nodes directly and has none.
type AlertRule struct {
	ID            string   `json:"id,omitempty"`
	Kind          string   `json:"kind"`
	Threshold     float64  `json:"threshold,omitempty"`
	HoldSeconds   int      `json:"hold_seconds,omitempty"`
	RepeatSeconds int      `json:"repeat_seconds,omitempty"`
	Scope         string   `json:"scope,omitempty"`
	Groups        []string `json:"groups,omitempty"`
	NodeIDs       []string `json:"node_ids,omitempty"`
	Exclude       []string `json:"exclude_node_ids,omitempty"`
}

// Alerts is the rules in effect on one node, at most one per kind. It encodes
// as [] when empty, never null.
type Alerts []AlertRule

func (a Alerts) MarshalJSON() ([]byte, error) {
	if a == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]AlertRule(a))
}

// Rule returns the rule of that kind, false when the node has none (it is off).
func (a Alerts) Rule(kind string) (AlertRule, bool) {
	for _, r := range a {
		if r.Kind == kind {
			return r, true
		}
	}
	return AlertRule{}, false
}

// rank is how narrowly the rule covers the node: 0 not at all, then 1 全部,
// 2 分组, 3 指定节点.
func (r AlertRule) rank(n Node) int {
	switch r.Scope {
	case ScopeNodes:
		if slices.Contains(r.NodeIDs, n.ID) {
			return 3
		}
		return 0
	case ScopeGroup:
		if slices.Contains(r.Groups, n.Group) && !slices.Contains(r.Exclude, n.ID) {
			return 2
		}
	case ScopeAll:
		if !slices.Contains(r.Exclude, n.ID) {
			return 1
		}
	}
	return 0
}

// EffectiveAlerts picks, for each kind, the narrowest rule covering the node.
// The result keeps each rule's id and scope (so the panel can say where it
// comes from) but not its node and group lists.
func EffectiveAlerts(rules []AlertRule, n Node) Alerts {
	out := Alerts{}
	for _, kind := range AlertKinds {
		best, bestRank := AlertRule{}, 0
		for _, r := range rules {
			if rank := r.rank(n); r.Kind == kind && rank > bestRank {
				best, bestRank = r, rank
			}
		}
		if bestRank > 0 {
			best.Groups, best.NodeIDs, best.Exclude = nil, nil, nil
			out = append(out, best)
		}
	}
	return out
}

// Overlaps reports the first other rule that would compete with r on the same
// footing: same kind and same scope covering a common node. Different scopes
// never compete — the narrower one wins — so this is what keeps "one rule per
// kind per node" unambiguous. name turns a node id into what the panel shows
// (nil: the id itself); the message goes to the administrator as is.
func (r AlertRule) Overlaps(others []AlertRule, name func(string) string) error {
	if name == nil {
		name = func(id string) string { return id }
	}
	for _, o := range others {
		if o.ID == r.ID || o.Kind != r.Kind || o.Scope != r.Scope {
			continue
		}
		label := AlertLabels[r.Kind]
		switch r.Scope {
		case ScopeAll:
			return errors.New("已有一条作用于全部节点的" + label + "规则，请直接修改它")
		case ScopeGroup:
			for _, g := range r.Groups {
				if slices.Contains(o.Groups, g) {
					return errors.New("分组「" + groupName(g) + "」已有一条" + label + "规则")
				}
			}
		case ScopeNodes:
			for _, id := range r.NodeIDs {
				if slices.Contains(o.NodeIDs, id) {
					return errors.New("节点「" + name(id) + "」已单独设置了一条" + label + "规则")
				}
			}
		}
	}
	return nil
}

func groupName(g string) string {
	if g == "" {
		return "默认"
	}
	return g
}

// Validate checks the rule's own parameters and the shape of its scope. Whether
// the listed nodes exist and whether it overlaps another rule is the store's
// job (it needs the other rows).
func (r AlertRule) Validate() error {
	if err := r.validateParams(); err != nil {
		return err
	}
	unique := func(list []string, limit int) bool {
		seen := map[string]bool{}
		for _, v := range list {
			if seen[v] || len(v) > limit {
				return false
			}
			seen[v] = true
		}
		return len(list) <= MaxRuleList
	}
	if !unique(r.Groups, 100) || !unique(r.NodeIDs, 64) || !unique(r.Exclude, 64) {
		return errors.New("分组或节点列表有重复、过长的项")
	}
	switch r.Scope {
	case ScopeAll:
		if len(r.Groups) > 0 || len(r.NodeIDs) > 0 {
			return errors.New("作用于全部节点的规则不需要分组或节点列表")
		}
	case ScopeGroup:
		if len(r.Groups) == 0 {
			return errors.New("按分组的规则须至少选一个分组")
		}
		if len(r.NodeIDs) > 0 {
			return errors.New("按分组的规则不需要节点列表；要去掉个别节点请用排除列表")
		}
	case ScopeNodes:
		if len(r.NodeIDs) == 0 {
			return errors.New("指定节点的规则须至少选一个节点")
		}
		if len(r.Groups) > 0 || len(r.Exclude) > 0 {
			return errors.New("指定节点的规则不需要分组或排除列表")
		}
	default:
		return errors.New("规则范围须为 all / group / nodes")
	}
	return nil
}

func (r AlertRule) validateParams() error {
	if r.RepeatSeconds < 0 || r.RepeatSeconds > 604800 || (r.RepeatSeconds > 0 && r.RepeatSeconds < 300) {
		return errors.New("重复提醒间隔须为 0 或 300–604800 秒")
	}
	if !finite(r.Threshold) {
		return errors.New("提醒阈值无效")
	}
	switch r.Kind {
	case AlertOffline:
		if r.Threshold != 0 || r.HoldSeconds < 0 || r.HoldSeconds > 86400 {
			return errors.New("离线提醒只取等待秒数（0–86400）")
		}
	case AlertRenewal:
		if r.HoldSeconds != 0 || r.Threshold < 0 || r.Threshold > 365 || r.Threshold != math.Trunc(r.Threshold) {
			return errors.New("续费提醒的提前天数须为 0–365 的整数")
		}
	case AlertTraffic:
		if r.HoldSeconds != 0 || r.Threshold < 1 || r.Threshold > 100 {
			return errors.New("流量提醒的阈值须为 1–100%")
		}
	case AlertThrottle:
		if r.Threshold != 0 || r.HoldSeconds != 0 {
			return errors.New("达量降速提醒没有阈值，按线路的降速规则触发")
		}
	case AlertCPU, AlertMemory:
		if r.Threshold < 1 || r.Threshold > 100 || r.HoldSeconds < 30 || r.HoldSeconds > 86400 {
			return errors.New("CPU / 内存提醒的阈值须为 1–100%，持续时间 30–86400 秒")
		}
	default:
		return errors.New("未知的提醒规则：" + r.Kind)
	}
	return nil
}
