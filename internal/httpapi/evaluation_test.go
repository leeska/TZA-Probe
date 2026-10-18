package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/storage"
	"github.com/leeska/TZA-Probe/web"
)

func TestCurrentAlertPauseIsLiveStateAndHistoryIsImmutable(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	id, _, err := s.SaveNode(ctx, model.Node{Name: "private-node", Billing: model.Billing{ResetAt: "2026-10-01T00:00:00Z", TrafficMode: "sum"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	n := model.Node{ID: id, Name: "private-node"}
	text := func(active, repeat bool) string { return "CPU snapshot" }
	if err = s.Transition(ctx, n, model.AlertCPU, text, true, 0, 0, now); err != nil {
		t.Fatal(err)
	}
	if err = s.PauseResourceAlert(ctx, id, model.AlertCPU); err != nil {
		t.Fatal(err)
	}
	a := &API{Store: s, AdminToken: model.Secret(), Assets: web.Handler()}
	h := a.Handler()
	login := httptest.NewRecorder()
	a.startSession(login, httptest.NewRequest("POST", "https://probe.example/api/v1/login", nil), now)
	cookie := login.Result().Cookies()[0]
	call := func(path string, session bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "https://probe.example"+path, nil)
		if session {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	read := func(path string) []model.Event {
		t.Helper()
		w := call(path, true)
		if w.Code != 200 {
			t.Fatalf("read events: %d %s", w.Code, w.Body.String())
		}
		var events []model.Event
		if err := json.Unmarshal(w.Body.Bytes(), &events); err != nil {
			t.Fatal(err)
		}
		return events
	}
	current := "/api/v1/admin/events?active=1&kind=cpu&node_id=" + id
	events := read(current)
	if len(events) != 1 || !events[0].EvaluationPaused || !events[0].Active {
		t.Fatalf("paused response %+v", events)
	}
	w := call("/api/v1/admin/events", true)
	if w.Code != 200 || strings.Contains(w.Body.String(), "evaluation_paused") {
		t.Fatalf("history gained mutable state %s", w.Body.String())
	}
	w = call(current, false)
	if w.Code != 401 || strings.Contains(w.Body.String(), "CPU snapshot") {
		t.Fatal("anonymous event access")
	}
	if err = s.Transition(ctx, n, model.AlertCPU, text, true, 0, 0, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	events = read(current)
	if len(events) != 1 || events[0].EvaluationPaused {
		t.Fatalf("resume response %+v", events)
	}
	if err = s.Transition(ctx, n, model.AlertCPU, text, false, 0, 0, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if events = read(current); len(events) != 0 {
		t.Fatalf("recovered alert remained current %+v", events)
	}
}
