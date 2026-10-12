package service

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/notify"
	"github.com/leeska/TZA-Probe/internal/storage"
)

func open(t *testing.T) *Service {
	t.Helper()
	s, err := storage.Open(filepath.Join(t.TempDir(), "service.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &Service{Store: s}
}

// A partial edit keeps every field it does not mention, merges nested objects
// field by field, never takes the id from the patch and rejects misspellings.
func TestPatchNodeMerges(t *testing.T) {
	svc := open(t)
	ctx := context.Background()
	id, _, err := svc.CreateNode(ctx, model.Node{Name: "甲", Group: "HK", BillingAt: "2026-09-01T00:00:00Z", TrafficMode: "sum",
		Alerts: model.Alerts{Load: true, CPUPercent: 80, MemoryPercent: 80, HoldSeconds: 60}})
	if err != nil {
		t.Fatal(err)
	}
	n, err := svc.PatchNode(ctx, id, json.RawMessage(`{"id":"other","public":true,"alerts":{"offline":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	if n.ID != id || !n.Public || n.Group != "HK" || !n.Alerts.Offline || !n.Alerts.Load || n.Alerts.CPUPercent != 80 {
		t.Fatalf("merged node: %+v", n)
	}
	if _, err = svc.PatchNode(ctx, id, json.RawMessage(`{"pbulic":true}`)); !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("misspelled field: %v", err)
	}
	if _, err = svc.PatchNode(ctx, "missing", json.RawMessage(`{}`)); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("missing node: %v", err)
	}
	// 校验在服务层：按额度降速却没有额度，HTTP 与 MCP 拿到的是同一句 400。
	if _, err = svc.PatchNode(ctx, id, json.RawMessage(`{"throttle_on":"quota","throttle_mbps":100}`)); !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("invalid patch: %v", err)
	}
}

// The combined settings update validates everything first: a bad panel value
// must not leave the notification channel already changed.
func TestUpdateSettingsValidatesBeforeWriting(t *testing.T) {
	svc := open(t)
	ctx := context.Background()
	bad := "bogus"
	_, _, err := svc.UpdateSettings(ctx, &notify.Config{Channel: "webhook", Webhook: "https://example.com/hook"}, model.PanelPatch{DefaultSkin: &bad})
	if !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("bad skin: %v", err)
	}
	if config, _ := svc.Notification(ctx); config.Channel != "none" {
		t.Fatalf("channel changed by a rejected update: %+v", config)
	}
	skin := "press"
	config, panel, err := svc.UpdateSettings(ctx, &notify.Config{Webhook: "https://example.com/hook"}, model.PanelPatch{DefaultSkin: &skin})
	if err != nil || config.Channel != "webhook" || panel.DefaultSkin != "press" || panel.LatencyWindowMinutes != model.DefaultLatencyWindowMinutes {
		t.Fatalf("update: %+v %+v %v", config, panel, err)
	}
	if _, _, err = svc.UpdateSettings(ctx, nil, model.PanelPatch{}); !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("empty update: %v", err)
	}
	if _, err = svc.TestNotification(ctx); !errors.Is(err, ErrNoSender) {
		t.Fatalf("no sender: %v", err)
	}
}

func TestWindow(t *testing.T) {
	for _, c := range [][4]int{{0, 24, 720, 24}, {-5, 24, 720, 1}, {9999, 24, 720, 720}, {48, 24, 720, 48}} {
		if got := Window(c[0], c[1], c[2]); got != c[3] {
			t.Fatalf("Window(%d,%d,%d)=%d want %d", c[0], c[1], c[2], got, c[3])
		}
	}
}
