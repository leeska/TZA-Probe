package service

import (
	"context"
	"testing"

	"github.com/leeska/TZA-Probe/internal/notify"
)

// The channel the Engine delivers through is whatever the service stored: an
// empty table is "none", a saved channel reads back as saved (minus the other
// channel's credentials), and an older bare Webhook row still counts.
func TestNotificationConfigReadsStoredChannel(t *testing.T) {
	svc := open(t)
	ctx := context.Background()
	if c, err := svc.NotificationConfig(ctx); err != nil || c.Channel != "none" {
		t.Fatalf("empty: %+v %v", c, err)
	}
	if err := svc.Store.SetSetting(ctx, legacyWebhookKey, "https://example.com/old"); err != nil {
		t.Fatal(err)
	}
	if c, err := svc.NotificationConfig(ctx); err != nil || c.Channel != "webhook" || c.Webhook != "https://example.com/old" {
		t.Fatalf("bare webhook row: %+v %v", c, err)
	}
	token := "123456:abcdefghijklmnopqrstuvwxyz"
	if _, err := svc.ReplaceNotification(ctx, notify.Config{Channel: "telegram", Webhook: "https://example.com/x", BotToken: token, ChatID: "@probe_alerts"}); err != nil {
		t.Fatal(err)
	}
	c, err := svc.NotificationConfig(ctx)
	if err != nil || c.Channel != "telegram" || c.BotToken != token || c.ChatID != "@probe_alerts" || c.Webhook != "" {
		t.Fatalf("saved telegram: %+v %v", c, err)
	}
}
