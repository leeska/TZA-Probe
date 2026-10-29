package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/storage"
	"github.com/leeska/TZA-Probe/web"
)

func TestBlockCheckAPIRequiresAdmin(t *testing.T) {
	s, err := storage.Open(filepath.Join(t.TempDir(), "block.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	token := model.Secret()
	handler := (&API{Store: s, AdminToken: token, Assets: web.Handler()}).Handler()
	call := func(method, path string, payload any, auth bool) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(method, "http://localhost/api/v1"+path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if auth {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		r := httptest.NewRecorder()
		handler.ServeHTTP(r, req)
		return r
	}
	if r := call("GET", "/admin/block-checks", nil, false); r.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", r.Code)
	}
	if r := call("GET", "/admin/block-checks", nil, true); r.Code != 200 || r.Body.String() != "[]\n" {
		t.Fatalf("empty: %d %s", r.Code, r.Body)
	}
	var ids []string
	for _, name := range []string{"mainland", "overseas"} {
		id, _, err := s.SaveNode(context.Background(), model.Node{Name: name, Billing: model.Billing{ResetAt: "2026-10-01T00:00:00Z", TrafficMode: "sum"}}, true)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	task := model.Task{Name: "IP test", Type: model.TaskBlock, Enabled: true, NodeIDs: ids, Block: &model.BlockSpec{Host: "1.1.1.1", Port: 443, Family: "ipv4", Interval: 60, MainlandNodeIDs: ids[:1], OverseasNodeIDs: ids[1:]}}
	if r := call("POST", "/admin/tasks", task, true); r.Code != 200 {
		t.Fatalf("create: %d %s", r.Code, r.Body)
	}
	r := call("GET", "/admin/block-checks", nil, true)
	var views []model.BlockCheckView
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &views) != nil || len(views) != 1 || views[0].Status != "unknown" || len(views[0].Evidence) != 2 {
		t.Fatalf("checks: %d %s", r.Code, r.Body)
	}
	task.Block.Host = "127.0.0.1"
	if r := call("POST", "/admin/tasks", task, true); r.Code != 400 {
		t.Fatalf("invalid target: %d %s", r.Code, r.Body)
	}
}
