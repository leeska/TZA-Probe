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

// 告警文字会原样发到 Telegram / Webhook，所以要说清楚：从没接入不是「已离线」，
// 续费提醒要给出剩余 / 已过期天数与到期日期。
func TestAlertMessages(t *testing.T) {
	if got := offlineMessage(model.NodeView{}); got == "节点已离线" {
		t.Fatalf("从未上报的节点不该说已离线: %q", got)
	}
	if got := offlineMessage(model.NodeView{LastSeen: 1}); got != "节点已离线" {
		t.Fatalf("上报过又断开的节点应提示已离线: %q", got)
	}
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		expiry time.Time
		want   string
	}{
		{now.Add(60 * time.Hour), "到期时间：2026-10-14 00:00:00 UTC\n剩余时间：2天12小时"},
		{now.Add(-6 * time.Hour), "到期时间：2026-10-11 06:00:00 UTC\n已过期：6小时"},
		{now.Add(-30 * time.Hour), "到期时间：2026-10-10 06:00:00 UTC\n已过期：1天6小时"},
	}
	for _, c := range cases {
		if got := renewalMessage(c.expiry, now); got != c.want {
			t.Fatalf("renewalMessage(%v) = %q, want %q", c.expiry, got, c.want)
		}
	}
}

func TestAlertDetailsDescribeObservedValues(t *testing.T) {
	now := time.Date(2026, 10, 13, 15, 30, 0, 0, time.UTC)
	n := model.NodeView{
		Node:   model.Node{Name: "node", Billing: model.Billing{TrafficLimit: 1_000_000_000, TrafficMode: "max", ExpiresAt: now.Add(-6 * time.Hour).Format(time.RFC3339), Price: 10, Currency: "USD", PricePeriod: "月"}, Line: model.Line{ThrottleMbps: 50, ThrottleOn: "quota"}},
		Online: true, LastSeen: now.Add(-10 * time.Second).Unix(), Metrics: &model.Metrics{CPU: 95.2, MemoryUsed: 900_000_000, MemoryTotal: 1_000_000_000},
		TrafficUsed: 1_200_000_000, CycleStart: now.Add(-24 * time.Hour).Unix(), CycleEnd: now.Add(30 * 24 * time.Hour).Unix(), Throttled: true,
	}
	for _, tc := range []struct {
		kind string
		want []string
	}{
		{model.AlertCPU, []string{"95.2%", "≥ 85.5%", "5分钟", "指标采样：2026-10-13 15:29:50 UTC"}},
		{model.AlertMemory, []string{"90.0%", "900 MB / 1.0 GB", "≥ 85.5%", "5分钟"}},
		{model.AlertTraffic, []string{"1.2 GB", "1.0 GB", "120.0%", "85.5%", "出入取较大值", "计费周期："}},
		{model.AlertThrottle, []string{"1.2 GB", "100%", "50 Mbit/s", "实际限速需向商家确认"}},
		{model.AlertRenewal, []string{"已过期：6小时", "到期时间：2026-10-13 09:30:00 UTC", "10 USD / 月", "提醒窗口：到期前 7 天"}},
	} {
		rule := model.AlertRule{Kind: tc.kind, Threshold: 85.5, HoldSeconds: 300}
		if tc.kind == model.AlertTraffic {
			rule.HoldSeconds = 0
		}
		if tc.kind == model.AlertThrottle {
			rule.Threshold, rule.HoldSeconds = 0, 0
		}
		if tc.kind == model.AlertRenewal {
			rule.Threshold, rule.HoldSeconds = 7, 0
		}
		on, message := evaluate(n, rule, now)
		got := message()
		if !on {
			t.Fatalf("%s: expected trigger", tc.kind)
		}
		for _, want := range tc.want {
			if !strings.Contains(got, want) {
				t.Fatalf("%s missing %q: %s", tc.kind, want, got)
			}
		}
	}
	n.Online = false
	n.LastSeen = now.Add(-2 * time.Minute).Unix()
	_, message := evaluate(n, model.AlertRule{Kind: model.AlertOffline, HoldSeconds: 60}, now)
	got := message()
	for _, want := range []string{"最后上报：", "未上报时长：2分钟", "45秒", "1分钟"} {
		if !strings.Contains(got, want) {
			t.Fatalf("offline missing %q: %s", want, got)
		}
	}
	n.LastSeen = 0
	n.CreatedAt = now.Add(-20 * time.Minute).Unix()
	_, message = evaluate(n, model.AlertRule{Kind: model.AlertOffline}, now)
	got = message()
	if !strings.Contains(got, "从未上报") || !strings.Contains(got, "添加时间：") || strings.Contains(got, "1970-") {
		t.Fatal(got)
	}
}

// Recovery must retain the value and cause at the event, not a later delivery retry.
func TestRecoveryEventsKeepDetailsAndCause(t *testing.T) {
	for _, cause := range []string{"normal", "offline", "rule_removed"} {
		t.Run(cause, func(t *testing.T) {
			ctx := context.Background()
			s, err := storage.Open(filepath.Join(t.TempDir(), "events.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			now := time.Date(2026, 10, 13, 15, 30, 0, 0, time.UTC)
			rule, err := s.SaveAlertRule(ctx, model.AlertRule{Kind: model.AlertCPU, Threshold: 90, HoldSeconds: 30, Scope: model.ScopeAll}, true)
			if err != nil {
				t.Fatal(err)
			}
			id, _, err := s.SaveNode(ctx, model.Node{Name: "cpu-node", Group: "HK", Region: "香港", Billing: model.Billing{ResetAt: billingAt(now), TrafficMode: "sum"}}, true)
			if err != nil {
				t.Fatal(err)
			}
			m := model.Metrics{CPU: 98.5, MemoryUsed: 50, MemoryTotal: 100}
			if err = s.Report(ctx, id, m, now); err != nil {
				t.Fatal(err)
			}
			engine := Engine{Repository: s}
			for _, at := range []time.Time{now, now.Add(30 * time.Second)} {
				if err = engine.Tick(ctx, at); err != nil {
					t.Fatal(err)
				}
			}
			recoveredAt := now.Add(40 * time.Second)
			switch cause {
			case "normal":
				m.CPU = 10.5
				if err = s.Report(ctx, id, m, recoveredAt); err != nil {
					t.Fatal(err)
				}
			case "offline":
				recoveredAt = now.Add(60 * time.Second)
			case "rule_removed":
				if err = s.DeleteAlertRule(ctx, rule); err != nil {
					t.Fatal(err)
				}
			}
			if err = engine.Tick(ctx, recoveredAt); err != nil {
				t.Fatal(err)
			}
			if cause == "offline" {
				firing, err := s.Events(ctx, model.EventQuery{Active: true})
				if err != nil || len(firing) != 1 || !firing[0].EvaluationPaused {
					t.Fatalf("offline must pause the active alarm: %+v %v", firing, err)
				}
				all, err := s.Events(ctx, model.EventQuery{})
				if err != nil || len(all) != 1 {
					t.Fatalf("unavailable metrics emitted recovery: %+v %v", all, err)
				}
				// A new normal sample, rather than loss of observations, is the
				// evidence that permits a recovery event.
				recoveredAt = recoveredAt.Add(10 * time.Second)
				m.CPU = 10.5
				if err = s.Report(ctx, id, m, recoveredAt); err != nil {
					t.Fatal(err)
				}
				if err = engine.Tick(ctx, recoveredAt); err != nil {
					t.Fatal(err)
				}
			}
			events, err := s.Events(ctx, model.EventQuery{})
			if err != nil || len(events) != 2 {
				t.Fatalf("events=%+v err=%v", events, err)
			}
			if events[0].Active || !events[1].Active {
				t.Fatal(events)
			}
			for _, event := range events {
				if !strings.Contains(event.Message, "分组：HK") || !strings.Contains(event.Message, "地区：香港") {
					t.Fatal(event.Message)
				}
			}
			if !strings.Contains(events[1].Message, "规则来源：全部节点") || !strings.Contains(events[1].Message, "重复提醒：关闭") {
				t.Fatal(events[1].Message)
			}
			if cause == "normal" {
				m.CPU = 40
				if err = s.Report(ctx, id, m, recoveredAt.Add(3*time.Second)); err != nil {
					t.Fatal(err)
				}
				pending, err := s.PendingEvents(ctx, recoveredAt.Add(3*time.Second))
				if err != nil || len(pending) != 2 || !strings.Contains(pending[1].Message, "当前 CPU：10.5%") {
					t.Fatal("delivery queue lost event snapshot", pending, err)
				}
			}
			got := events[0].Message
			if cause != "offline" && !strings.Contains(got, "本次条件持续：") {
				t.Fatal(got)
			}
			switch cause {
			case "normal":
				if !strings.Contains(got, "当前 CPU：10.5%") || !strings.Contains(got, "当前阈值：90%") {
					t.Fatal(got)
				}
			case "offline":
				if !strings.Contains(got, "当前 CPU：10.5%") || strings.Contains(got, "本次条件持续：") {
					t.Fatal(got)
				}
			case "rule_removed":
				if !strings.Contains(got, "规则已关闭、删除或不再覆盖") || !strings.Contains(got, "当前 CPU：98.5%") {
					t.Fatal(got)
				}
			}
		})
	}
}

func TestTelegramMessageHasReadableSections(t *testing.T) {
	e := model.Event{ID: 123, NodeID: "node-id", NodeName: "<node>\nwrong-line", Kind: model.AlertCPU, Active: true, At: 1791905400, Message: "当前 CPU：95.2%\n触发条件：CPU ≥ 90%，持续至少 5分钟"}
	got := telegramMessage(e)
	for _, want := range []string{"🔴 告警触发\n类型：CPU 使用率", "节点：<node> wrong-line\n节点 ID：node-id", "\n\n当前 CPU：", "\n\n事件时间：", "UTC", "事件编号：#123"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q: %s", want, got)
		}
	}
	got = telegramMessage(model.Event{Kind: "test", Message: "通知连接测试", At: e.At})
	if strings.Contains(got, "节点：") || strings.Contains(got, "事件编号：") || !strings.Contains(got, "通道：Telegram") {
		t.Fatal(got)
	}
}

// The repeat marker belongs in the body, after the node's group / region.
func TestRepeatReminderFollowsNodeContext(t *testing.T) {
	now := time.Date(2026, 10, 13, 15, 30, 0, 0, time.UTC)
	n := model.NodeView{Node: model.Node{Group: "HK"}, Online: true, LastSeen: now.Unix(), Metrics: &model.Metrics{CPU: 95}}
	rule := model.AlertRule{Kind: model.AlertCPU, Threshold: 90, RepeatSeconds: 1800, Scope: model.ScopeAll}
	on, message := evaluate(n, rule, now)
	got := eventText(n, rule, true, message, now)(true, true)
	if !on || !strings.HasPrefix(got, "分组：HK\n\n提醒状态：异常仍持续（重复提醒）\n当前 CPU：95.0%") {
		t.Fatal(got)
	}
	if got = eventText(n, rule, true, message, now)(true, false); strings.Contains(got, "重复提醒）") {
		t.Fatal(got)
	}
}

func TestDetailsWithoutOptionalFields(t *testing.T) {
	now := time.Date(2026, 10, 13, 15, 30, 0, 0, time.UTC)
	n := model.NodeView{Node: model.Node{Billing: model.Billing{ExpiresAt: now.Add(48 * time.Hour).Format(time.RFC3339), Price: 10, PricePeriod: "月"}}}
	if got := renewalDetails(n, now); !strings.Contains(got, "续费参考：10 / 月") {
		t.Fatal(got)
	}
	// 降速声明清掉后解除，不写像真阈值的 0 Mbit/s。
	got := recoveryMessage(n, model.AlertRule{Kind: model.AlertThrottle}, false, now)
	if strings.Contains(got, "0 Mbit/s") || !strings.Contains(got, "未声明降速条件") {
		t.Fatal(got)
	}
}

func TestTelegramMessageFitsLimit(t *testing.T) {
	e := model.Event{ID: 9, NodeName: "node", Kind: model.AlertCPU, Active: true, At: 1791905400, Message: strings.Repeat("🔥数据", 3000)}
	got := telegramMessage(e)
	if n := utf16Len(got); n > telegramLimit {
		t.Fatalf("message is %d UTF-16 units", n)
	}
	if !strings.Contains(got, "内容过长已截断") || !strings.HasSuffix(got, "事件编号：#9") {
		t.Fatal(got[len(got)-200:])
	}
	e.Message = "短正文"
	if got = telegramMessage(e); strings.Contains(got, "截断") {
		t.Fatal(got)
	}
}
