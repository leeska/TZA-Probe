package model

import (
	"encoding/json"
	"errors"
	"math"
)

// Alert rule kinds. Each kind appears at most once in a node's rules; a rule
// that is in the list is on.
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

// AlertRule is one alert rule. Which of Threshold / HoldSeconds a kind takes is
// listed on the kind constants; RepeatSeconds re-sends a still-active alert
// every that many seconds (0 sends it once).
type AlertRule struct {
	Kind          string  `json:"kind"`
	Threshold     float64 `json:"threshold,omitempty"`
	HoldSeconds   int     `json:"hold_seconds,omitempty"`
	RepeatSeconds int     `json:"repeat_seconds,omitempty"`
}

// Alerts is a node's rule list. It encodes as [] when empty, never null.
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

func (a Alerts) Validate() error {
	if len(a) > len(AlertKinds) {
		return errors.New("提醒规则过多")
	}
	seen := map[string]bool{}
	for _, r := range a {
		if seen[r.Kind] {
			return errors.New("同一种提醒规则只能有一条：" + r.Kind)
		}
		seen[r.Kind] = true
		if err := r.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (r AlertRule) Validate() error {
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
		if r.HoldSeconds != 0 || r.Threshold <= 0 || r.Threshold > 100 {
			return errors.New("流量提醒的阈值须为 1–100%")
		}
	case AlertThrottle:
		if r.Threshold != 0 || r.HoldSeconds != 0 {
			return errors.New("达量降速提醒没有阈值，按线路的降速规则触发")
		}
	case AlertCPU, AlertMemory:
		if r.Threshold <= 0 || r.Threshold > 100 || r.HoldSeconds < 30 || r.HoldSeconds > 86400 {
			return errors.New("CPU / 内存提醒的阈值须为 1–100%，持续时间 30–86400 秒")
		}
	default:
		return errors.New("未知的提醒规则：" + r.Kind)
	}
	return nil
}
