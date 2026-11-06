package storage

import (
	"errors"
	"sync"
	"testing"
	"time"

	runtimepkg "github.com/leeska/TZA-Probe/internal/executorruntime"
	"github.com/leeska/TZA-Probe/internal/model"
)

func TestMarketRuntimeRequestScopeAndConcurrency(t *testing.T) {
	store := openTest(t)
	node, _ := makeNode(t, store)
	ctx := t.Context()
	enrollment := runtimeEnrollment()
	if _, err := store.CreateMarketRuntimeRequest(ctx, node, "0.4.0", enrollment.ScopeSHA256); err == nil {
		t.Fatal("unprovisioned node accepted")
	}
	if _, err := store.RuntimeExchange(ctx, node, runtimepkg.ChannelReport{Enrollment: enrollment}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateMarketRuntimeRequest(ctx, node, "0.4.0", ""); !errors.Is(err, model.ErrInvalid) {
		t.Fatal("missing scope accepted", err)
	}
	if _, err := store.CreateMarketRuntimeRequest(ctx, node, "0.4.0", string(bytes64('f'))); !errors.Is(err, model.ErrConflict) {
		t.Fatal("changed scope accepted", err)
	}
	if _, err := store.db.Exec("UPDATE runtime_channels SET seen_at=? WHERE node_id=?", time.Now().Unix()-46, node); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateMarketRuntimeRequest(ctx, node, "0.4.0", enrollment.ScopeSHA256); !errors.Is(err, model.ErrConflict) {
		t.Fatal("expired scope accepted", err)
	}
	if _, err := store.RuntimeExchange(ctx, node, runtimepkg.ChannelReport{Enrollment: enrollment}); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			request, err := store.CreateMarketRuntimeRequest(ctx, node, "0.4.0", enrollment.ScopeSHA256)
			if err == nil && (request.State != "requested" || request.Action != "stage" || request.ApprovedAt != 0 || request.Receipt != nil) {
				err = errors.New("request downloaded or approved itself")
			}
			results <- err
		}()
	}
	workers.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		switch {
		case err == nil:
			success++
		case errors.Is(err, model.ErrConflict):
			conflict++
		default:
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("concurrent request duplicated", success, conflict)
	}
}
