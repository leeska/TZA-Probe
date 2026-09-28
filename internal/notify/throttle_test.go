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

// 达量降速告警：阈值来自节点（ThrottleMbps），判据是存储算出的窗口平均带宽。
// 与其它规则一样，只有勾了 alerts.throttle 才提醒，且触发/恢复各通知一次。
func TestThrottleRuleTracksWindowAverage(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(filepath.Join(t.TempDir(), "throttle.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Date(2026, 9, 18, 12, 30, 0, 0, time.UTC)
	node := model.Node{Name: "de-netcup", BillingAt: billingAt(now), TrafficMode: "sum", LineMbps: 1000, ThrottleMbps: 1, ThrottleHours: 1, Alerts: model.Alerts{Throttle: true}}
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
	engine := Engine{Repository: s, Sender: Webhook{}}
	if err = engine.Tick(ctx, now); err != nil {
		t.Fatal(err)
	}
	events, err := s.Events(ctx, false, now.Add(time.Minute))
	if err != nil || len(events) != 1 || events[0].Kind != "throttle" || !events[0].Active {
		t.Fatalf("expected one 降速事件: %+v %v", events, err)
	}
	if !strings.Contains(events[0].Message, "1.0 Mbit/s") || !strings.Contains(events[0].Message, "阈值 1 Mbit/s") {
		t.Fatalf("message without the numbers: %q", events[0].Message)
	}

	// 把阈值抬到 2 Mbit/s：同一份数据不再达到，恢复一次。
	views, err := s.Nodes(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	node = views[0].Node
	node.ThrottleMbps = 2
	if _, _, err = s.SaveNode(ctx, node, false); err != nil {
		t.Fatal(err)
	}
	if err = engine.Tick(ctx, now.Add(15*time.Second)); err != nil {
		t.Fatal(err)
	}
	events, err = s.Events(ctx, false, now.Add(time.Minute))
	if err != nil || len(events) != 2 || events[0].Active {
		t.Fatalf("expected one recovery: %+v %v", events, err)
	}
}
