package notify

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/storage"
)

// 按额度降速的告警文案要写清楚「是额度用满、之后限速多少」，不能套用窗口均值的句子
// （那条线路根本没有窗口这回事）。
func TestThrottleRuleOnQuota(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(filepath.Join(t.TempDir(), "quota.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Date(2026, 9, 18, 12, 30, 0, 0, time.UTC)
	node := model.Node{Name: "hk-dmit", Billing: model.Billing{ResetAt: billingAt(now), TrafficMode: "max", TrafficLimit: 1_000_000_000}, Line: model.Line{Mbps: 4000, ThrottleMbps: 50, ThrottleOn: "quota"}}
	addRules(t, s, model.AlertRule{Kind: model.AlertThrottle})
	_, token, err := s.SaveNode(ctx, node, true)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := s.Authenticate(ctx, token)
	m := model.Metrics{MemoryTotal: 100, NetIn: 1000, Uptime: 100}
	if err = s.Report(ctx, id, m, now.Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	// 取较大方向 1.2 GB > 1 GB 额度：用满，该通知。
	m.NetIn += 1_200_000_000
	m.Uptime += 120
	if err = s.Report(ctx, id, m, now); err != nil {
		t.Fatal(err)
	}
	engine := Engine{Repository: s, Sender: Transport{}}
	if err = engine.Tick(ctx, now); err != nil {
		t.Fatal(err)
	}
	events, err := s.Events(ctx, model.EventQuery{})
	if err != nil || len(events) != 1 || events[0].Kind != "throttle" || !events[0].Active {
		t.Fatalf("expected one 降速事件: %+v %v", events, err)
	}
	if !strings.Contains(events[0].Message, "1.0 GB") || !strings.Contains(events[0].Message, "50 Mbit/s") {
		t.Fatalf("message without the quota numbers: %q", events[0].Message)
	}
	if strings.Contains(events[0].Message, "小时平均带宽") {
		t.Fatalf("按额度降速的线路不该报窗口均值：%q", events[0].Message)
	}
}

// 达量降速告警：阈值来自节点（ThrottleMbps），判据是存储算出的窗口平均带宽。
// 与其它规则一样，只有开了 throttle 规则才提醒，且触发/恢复各通知一次。
func TestThrottleRuleTracksWindowAverage(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(filepath.Join(t.TempDir(), "throttle.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Date(2026, 9, 18, 12, 30, 0, 0, time.UTC)
	node := model.Node{Name: "de-node", Billing: model.Billing{ResetAt: billingAt(now), TrafficMode: "sum"}, Line: model.Line{Mbps: 1000, ThrottleMbps: 1, ThrottleHours: 1}}
	addRules(t, s, model.AlertRule{Kind: model.AlertThrottle})
	_, token, err := s.SaveNode(ctx, node, true)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := s.Authenticate(ctx, token)
	m := model.Metrics{MemoryTotal: 100, NetIn: 1000, Uptime: 100}
	if err = s.Report(ctx, id, m, now.Add(-90*time.Minute)); err != nil {
		t.Fatal(err)
	}
	// 5400 秒里双向 6.75 亿字节 = 1 Mbit/s，正好达到 1 Mbit/s 阈值。
	m.NetIn += 600_000_000
	m.NetOut += 75_000_000
	m.Uptime += 5400
	if err = s.Report(ctx, id, m, now); err != nil {
		t.Fatal(err)
	}
	engine := Engine{Repository: s, Sender: Transport{}}
	if err = engine.Tick(ctx, now); err != nil {
		t.Fatal(err)
	}
	events, err := s.Events(ctx, model.EventQuery{})
	if err != nil || len(events) != 1 || events[0].Kind != "throttle" || !events[0].Active {
		t.Fatalf("expected one 降速事件: %+v %v", events, err)
	}
	if !strings.Contains(events[0].Message, "1.0 Mbit/s") || !strings.Contains(events[0].Message, "触发阈值：1 Mbit/s") {
		t.Fatalf("message without the numbers: %q", events[0].Message)
	}

	// 把阈值抬到 2 Mbit/s：同一份数据不再达到，恢复一次。
	views, err := s.Nodes(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	node = views[0].Node
	node.Line.ThrottleMbps = 2
	if _, _, err = s.SaveNode(ctx, node, false); err != nil {
		t.Fatal(err)
	}
	if err = engine.Tick(ctx, now.Add(15*time.Second)); err != nil {
		t.Fatal(err)
	}
	events, err = s.Events(ctx, model.EventQuery{})
	if err != nil || len(events) != 2 || events[0].Active {
		t.Fatalf("expected one recovery: %+v %v", events, err)
	}
}
