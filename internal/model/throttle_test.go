package model

import (
	"testing"
)

func TestThrottleFieldsValidate(t *testing.T) {
	base := func() Node {
		return Node{Name: "n", Billing: Billing{ResetAt: "2026-09-01T00:00:00Z", TrafficMode: "sum"}}
	}
	cases := []struct {
		name string
		edit func(*Node)
	}{
		{"不设线路规格", func(*Node) {}},
		{"德国线路：1 Gbit/s 端口、100 Mbit/s 阈值、24 小时窗口", func(n *Node) {
			n.Line.Mbps, n.Line.ThrottleMbps, n.Line.ThrottleHours = 1000, 100, 24
		}},
		{"只填阈值（窗口留 0 走默认 24）", func(n *Node) { n.Line.ThrottleMbps = 100 }},
		{"窗口上限 168 小时", func(n *Node) { n.Line.ThrottleHours = 168 }},
		{"启用降速告警且填了阈值", func(n *Node) { n.Line.ThrottleMbps = 100; n.Alerts.Throttle = true }},
		{"DMIT 那种：1000 GB 额度用满后不计量、限速 50 Mbit/s", func(n *Node) {
			n.Billing.TrafficLimit, n.Line.Mbps, n.Line.ThrottleMbps, n.Line.ThrottleOn = 1_000_000_000_000, 4000, 50, "quota"
		}},
		{"显式写 bandwidth 触发条件", func(n *Node) { n.Line.ThrottleMbps, n.Line.ThrottleOn = 100, "bandwidth" }},
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
		{"窗口越界", func(n *Node) { n.Line.ThrottleHours = 169 }},
		{"窗口为负", func(n *Node) { n.Line.ThrottleHours = -1 }},
		{"线路带宽为负", func(n *Node) { n.Line.Mbps = -1 }},
		{"阈值超过 1 Tbit/s", func(n *Node) { n.Line.ThrottleMbps = 1_000_001 }},
		{"阈值高于线路带宽", func(n *Node) { n.Line.Mbps, n.Line.ThrottleMbps = 1000, 2000 }},
		{"开了降速告警却没填阈值", func(n *Node) { n.Alerts.Throttle = true }},
		{"未知的降速触发条件", func(n *Node) { n.Line.ThrottleMbps, n.Line.ThrottleOn = 100, "volume" }},
		{"按额度降速但没填额度", func(n *Node) { n.Line.ThrottleMbps, n.Line.ThrottleOn = 50, "quota" }},
		{"按额度降速但没填速率", func(n *Node) { n.Billing.TrafficLimit, n.Line.ThrottleOn = 1_000_000_000_000, "quota" }},
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
	if got := (Line{}).WindowHours(); got != DefaultThrottleHours {
		t.Fatalf("默认窗口 = %d, want %d", got, DefaultThrottleHours)
	}
	if got := (Line{ThrottleHours: 6}).WindowHours(); got != 6 {
		t.Fatalf("窗口 = %d, want 6", got)
	}
}

// ThrottleActive 是两条规则的唯一判据（面板与告警都用它），按规则读不同的事实：
// 带宽口径读窗口均值，额度口径读本周期用量。
func TestThrottleActiveReadsTheRightFact(t *testing.T) {
	quota := NodeView{Node: Node{Billing: Billing{TrafficLimit: 1_000}, Line: Line{ThrottleMbps: 50, ThrottleOn: "quota"}}}
	quota.TrafficUsed = 999
	if quota.ThrottleActive() {
		t.Fatal("数额度没到额度不该算降速")
	}
	quota.TrafficUsed = 1_000
	if !quota.ThrottleActive() {
		t.Fatal("用满额度该算降速")
	}
	// 额度口径下窗口均值再高也不触发，带宽口径下额度再满也不触发。
	quota.AvgBits = 1e12
	quota.TrafficUsed = 0
	if quota.ThrottleActive() {
		t.Fatal("额度口径下不该看窗口均值")
	}
	bw := NodeView{Node: Node{Billing: Billing{TrafficLimit: 1}, Line: Line{ThrottleMbps: 100}}}
	bw.TrafficUsed = 1
	if bw.ThrottleActive() {
		t.Fatal("带宽口径下不该看本周期用量")
	}
	bw.AvgBits = 100e6
	if !bw.ThrottleActive() {
		t.Fatal("均值达到阈值该算降速")
	}
	if (NodeView{Node: Node{Line: Line{ThrottleOn: "quota"}}}).ThrottleActive() {
		t.Fatal("没填速率就没有降速这回事")
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		0:                 "0 B",
		999:               "999 B",
		1_000:             "1.0 KB",
		1_000_000:         "1.0 MB",
		812_000_000:       "812 MB",
		1_000_000_000:     "1.0 GB",
		1_000_000_000_000: "1.0 TB",
	}
	for in, want := range cases {
		if got := HumanBytes(in); got != want {
			t.Fatalf("HumanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}
