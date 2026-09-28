package model

import (
	"testing"
	"time"
)

func TestThrottleFieldsValidate(t *testing.T) {
	base := func() Node {
		return Node{Name: "n", BillingAt: "2026-09-01T00:00:00Z", TrafficMode: "sum"}
	}
	cases := []struct {
		name string
		edit func(*Node)
	}{
		{"不设线路规格", func(*Node) {}},
		{"德国线路：1 Gbit/s 端口、100 Mbit/s 阈值、24 小时窗口", func(n *Node) {
			n.LineMbps, n.ThrottleMbps, n.ThrottleHours = 1000, 100, 24
		}},
		{"只填阈值（窗口留 0 走默认 24）", func(n *Node) { n.ThrottleMbps = 100 }},
		{"窗口上限 168 小时", func(n *Node) { n.ThrottleHours = 168 }},
		{"启用降速告警且填了阈值", func(n *Node) { n.ThrottleMbps = 100; n.Alerts.Throttle = true }},
	}
	for _, c := range cases {
		n := base()
		c.edit(&n)
		if err := n.Validate(); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
	bad := []struct {
		name string
		edit func(*Node)
	}{
		{"窗口越界", func(n *Node) { n.ThrottleHours = 169 }},
		{"窗口为负", func(n *Node) { n.ThrottleHours = -1 }},
		{"线路带宽为负", func(n *Node) { n.LineMbps = -1 }},
		{"阈值超过 1 Tbit/s", func(n *Node) { n.ThrottleMbps = 1_000_001 }},
		{"阈值高于线路带宽", func(n *Node) { n.LineMbps, n.ThrottleMbps = 1000, 2000 }},
		{"开了降速告警却没填阈值", func(n *Node) { n.Alerts.Throttle = true }},
	}
	for _, c := range bad {
		n := base()
		c.edit(&n)
		if err := n.Validate(); err == nil {
			t.Fatalf("%s: 应该被拒", c.name)
		}
	}
}

func TestThrottleWindowDefaults(t *testing.T) {
	if got := (Node{}).WindowHours(); got != DefaultThrottleHours {
		t.Fatalf("默认窗口 = %d, want %d", got, DefaultThrottleHours)
	}
	if got := (Node{ThrottleHours: 6}).ThrottleWindow(); got != 6*time.Hour {
		t.Fatalf("窗口 = %v, want 6h", got)
	}
}
