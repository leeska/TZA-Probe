package notify

import (
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
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
		{now.Add(60 * time.Hour), "节点将在 3 天后到期（2026-10-14 00:00 UTC），请检查续费"},
		{now.Add(-6 * time.Hour), "节点今天到期（2026-10-11 06:00 UTC），请检查续费"},
		{now.Add(-30 * time.Hour), "节点已过期 1 天（2026-10-10 06:00 UTC 到期），请检查续费"},
	}
	for _, c := range cases {
		if got := renewalMessage(c.expiry, now); got != c.want {
			t.Fatalf("renewalMessage(%v) = %q, want %q", c.expiry, got, c.want)
		}
	}
}
