package agent

import (
	"context"
	"log/slog"
	"time"

	"github.com/leeska/TZA-Probe/internal/ipsentinel"
)

type ExecutorSync interface{ Sync(context.Context) error }

func (a Agent) syncExecutor(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		callContext, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := a.ExecutorSync.Sync(callContext)
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.Warn("executor synchronization or command unavailable; durable state retained")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (t *HTTPTransport) ExecutorCursor(ctx context.Context) (ipsentinel.SyncReceipt, error) {
	var receipt ipsentinel.SyncReceipt
	err := t.request(ctx, "GET", "/api/v1/agent/executors/ipsentinel", nil, &receipt)
	return receipt, err
}

func (t *HTTPTransport) SaveExecutorRecords(ctx context.Context, report ipsentinel.SyncReport) (ipsentinel.SyncReceipt, error) {
	var receipt ipsentinel.SyncReceipt
	err := t.request(ctx, "POST", "/api/v1/agent/executors/ipsentinel/records", report, &receipt)
	return receipt, err
}

func (t *HTTPTransport) ExecutorAction(ctx context.Context, hello ipsentinel.Hello) (ipsentinel.ExecutorAction, error) {
	var action ipsentinel.ExecutorAction
	err := t.request(ctx, "POST", "/api/v1/agent/executors/ipsentinel/actions", hello, &action)
	return action, err
}

func (t *HTTPTransport) FinishExecutorAction(ctx context.Context, result ipsentinel.ActionResult) error {
	return t.request(ctx, "POST", "/api/v1/agent/executors/ipsentinel/action-results", result, nil)
}
