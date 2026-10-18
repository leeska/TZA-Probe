package storage

import (
	"context"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

func TestQuietPausedResourceAlertsDoNotWrite(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id, _ := makeNode(t, s)
	changes := func() int64 {
		t.Helper()
		var n int64
		if err := s.db.QueryRow("SELECT total_changes()").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := changes()
	if err := s.PauseResourceAlert(ctx, id, model.AlertCPU); err != nil {
		t.Fatal(err)
	}
	if changes() != before {
		t.Fatal("unknown metrics created quiet alert state")
	}
	n := model.Node{ID: id, Name: "quiet"}
	now := time.Now()
	for _, active := range []bool{false, true} {
		hold := 30
		if active {
			hold = 0
		}
		if err := s.Transition(ctx, n, model.AlertCPU, nil, true, hold, 0, now); err != nil {
			t.Fatal(err)
		}
		before = changes()
		if err := s.PauseResourceAlert(ctx, id, model.AlertCPU); err != nil {
			t.Fatal(err)
		}
		if changes() != before+1 {
			t.Fatal("pause did not clear the observed interval once")
		}
		before = changes()
		for i := 0; i < 3; i++ {
			if err := s.PauseResourceAlert(ctx, id, model.AlertCPU); err != nil {
				t.Fatal(err)
			}
		}
		if changes() != before {
			t.Fatal("paused alerts kept committing on every tick")
		}
		var since int64
		var got bool
		if err := s.db.QueryRow("SELECT since,active FROM alert_state WHERE node_id=? AND kind=?", id, model.AlertCPU).Scan(&since, &got); err != nil || since != 0 || got != active {
			t.Fatalf("pause changed active state: %d %v %v", since, got, err)
		}
	}
	if err := s.PauseResourceAlert(ctx, id, model.AlertOffline); err == nil {
		t.Fatal("paused a non-resource alert")
	}
}
