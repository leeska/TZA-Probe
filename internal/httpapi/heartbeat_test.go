package httpapi

import (
	"context"
	"encoding/json"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/storage"
	"github.com/leeska/TZA-Probe/web"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHeartbeatAuthenticationValidationAndIsolation(t *testing.T) {
	s, err := storage.Open(filepath.Join(t.TempDir(), "health.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	billing := model.Billing{ResetAt: "2026-09-01T00:00:00Z", TrafficMode: "sum"}
	id, token, err := s.SaveNode(ctx, model.Node{Name: "health", Billing: billing}, true)
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := s.SaveNode(ctx, model.Node{Name: "other", Billing: billing}, true)
	if err != nil {
		t.Fatal(err)
	}
	admin := model.Secret()
	h := (&API{Store: s, AdminToken: admin, Assets: web.Handler()}).Handler()
	body := `{"protocol":1,"status":{"state":"collect_failed","reason":"disk","os":"linux","arch":"amd64"}}`
	call := func(key, b string) int {
		r := httptest.NewRequest("POST", "/api/v1/agent/heartbeat", strings.NewReader(b))
		r.Header.Set("Authorization", "Bearer "+key)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	for _, key := range []string{"", admin, model.Secret()} {
		if code := call(key, body); code != 401 {
			t.Fatalf("credential isolation: %d", code)
		}
	}
	for _, b := range []string{strings.Replace(body, `"protocol":1`, `"protocol":99`, 1), strings.Replace(body, `"disk"`, `"/private/mount"`, 1), strings.Replace(body, `"collect_failed"`, `"arbitrary"`, 1)} {
		if code := call(token, b); code != 400 {
			t.Fatalf("validation: %d", code)
		}
	}
	if code := call(token, body); code != 204 {
		t.Fatalf("heartbeat: %d", code)
	}
	nodes, err := s.Nodes(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		switch n.ID {
		case id:
			if !n.Online || !n.MetricsStale || n.Metrics != nil {
				t.Fatalf("heartbeat %+v", n)
			}
			b, _ := json.Marshal(n.Live())
			if !strings.Contains(string(b), `"agent_status"`) {
				t.Fatal("diagnostics missing from live updates")
			}
		case other:
			if n.Online {
				t.Fatal("heartbeat crossed node identity")
			}
		}
	}
}
