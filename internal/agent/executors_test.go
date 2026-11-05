package agent

import (
	"context"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

type blockedExecutorSync struct{ entered chan struct{} }

func (syncer blockedExecutorSync) Sync(ctx context.Context) error {
	close(syncer.entered)
	<-ctx.Done()
	return ctx.Err()
}

func TestBlockedExecutorSyncDoesNotBlockOrdinaryJobs(t *testing.T) {
	transport := &fakeTransport{finished: map[string]bool{}, lease: []model.Job{{ID: "ordinary-tcp", Task: model.Probe{Kind: "tcp"}}}}
	syncer := blockedExecutorSync{entered: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		Agent{Collector: nopCollector{}, Transport: transport, Prober: blockingProber{}, ExecutorSync: syncer}.Run(ctx)
	}()
	defer func() { cancel(); <-done }()
	select {
	case <-syncer.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("sync loop never started")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		polls, finished := transport.snapshot()
		if polls > 0 && finished == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("executor sync blocked ordinary probes")
		}
		time.Sleep(time.Millisecond)
	}
}
