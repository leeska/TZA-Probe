package service

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
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
	id, _, err := svc.CreateNode(ctx, model.Node{Name: "甲", Group: "HK", Alerts: model.Alerts{{Kind: model.AlertCPU, Threshold: 80, HoldSeconds: 60}}, Billing: model.Billing{ResetAt: "2026-09-01T00:00:00Z", TrafficMode: "sum"}})
	if err != nil {
		t.Fatal(err)
	}
	n, err := svc.PatchNode(ctx, id, json.RawMessage(`{"id":"other","public":true,"line":{"mbps":100}}`))
	if err != nil {
		t.Fatal(err)
	}
	if rule, on := n.Alerts.Rule(model.AlertCPU); n.ID != id || !n.Public || n.Group != "HK" || n.Line.Mbps != 100 || !on || rule.Threshold != 80 {
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

// A card region must be one a task target or the catalogue uses: anything else
// can never match and would leave the card silently empty. Whitespace is
// trimmed first, and a value the node already had stays editable.
func TestCardRegionsMustBeKnown(t *testing.T) {
	svc := open(t)
	ctx := context.Background()
	base := model.Node{Name: "甲", Billing: model.Billing{ResetAt: "2026-09-01T00:00:00Z", TrafficMode: "sum"}}

	// 目录里有的地区（上海）带空白也能存，存下的是去掉空白的值。
	n := base
	n.Display = model.NodeDisplay{RouteRegion: " 上海 "}
	id, _, err := svc.CreateNode(ctx, n)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.Node(ctx, id); got.Display.RouteRegion != "上海" {
		t.Fatalf("region not trimmed: %q", got.Display.RouteRegion)
	}
	// 目录和任务里都没有的地区：拒绝，并说明原因。
	n.Display = model.NodeDisplay{RouteRegion: "上海市"}
	if _, _, err = svc.CreateNode(ctx, n); !errors.Is(err, model.ErrInvalid) || !strings.Contains(err.Error(), "上海市") {
		t.Fatalf("unknown region: %v", err)
	}
	// 自定义任务的目标地区也算已知。
	if _, err = svc.CreateTask(ctx, model.Task{Name: "海外", Type: model.TaskProbe, Probe: &model.ProbeSpec{
		Checks: []string{"tcp"}, Interval: 60, Targets: []model.Target{{Host: "1.1.1.1", Port: 443, Family: "ipv4", Region: "火星", Carrier: "其他"}},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.PatchNode(ctx, id, json.RawMessage(`{"display":{"latency_region":"火星"}}`)); err != nil {
		t.Fatalf("task region rejected: %v", err)
	}
	// 已经存着的值即使不再有任务用它，也不挡住别的编辑。
	if err = svc.DeleteTask(ctx, mustTaskID(t, svc)); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.PatchNode(ctx, id, json.RawMessage(`{"group":"JP"}`)); err != nil {
		t.Fatalf("unchanged region blocked an edit: %v", err)
	}
	// 回程运营商只有三网可选。
	if _, err = svc.PatchNode(ctx, id, json.RawMessage(`{"display":{"route_carriers":["其他"]}}`)); !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("route carrier 其他 accepted: %v", err)
	}
	if n, err := svc.PatchNode(ctx, id, json.RawMessage(`{"display":{"route_carriers":["电信","联通"]}}`)); err != nil || len(n.Display.RouteCarriers) != 2 {
		t.Fatalf("route carriers: %+v %v", n.Display, err)
	}
}

func mustTaskID(t *testing.T, svc *Service) string {
	t.Helper()
	tasks, err := svc.Tasks(context.Background())
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks: %d %v", len(tasks), err)
	}
	return tasks[0].ID
}
