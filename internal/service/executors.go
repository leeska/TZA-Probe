package service

import (
	"context"
	"time"

	runtimepkg "github.com/leeska/TZA-Probe/internal/executorruntime"
	"github.com/leeska/TZA-Probe/internal/ipsentinel"
)

type ExecutorStore interface {
	BindExecutor(context.Context, string, ipsentinel.Binding) error
	UnbindExecutor(context.Context, string) error
	ExecutorCursor(context.Context, string) (ipsentinel.SyncReceipt, error)
	SaveExecutorRecords(context.Context, string, ipsentinel.SyncReport, time.Time) (ipsentinel.SyncReceipt, error)
	Executors(context.Context) ([]ipsentinel.View, error)
	ExecutorRecords(context.Context, string, int64) ([]ipsentinel.Record, error)
	CreateExecutorAction(context.Context, string, ipsentinel.ExecutorAction) error
	LeaseExecutorAction(context.Context, string, ipsentinel.Hello) (ipsentinel.ExecutorAction, error)
	FinishExecutorAction(context.Context, string, ipsentinel.ActionResult) error
	ExecutorActions(context.Context, string) ([]ipsentinel.ActionView, error)
	SaveExecutorSchedule(context.Context, string, ipsentinel.ScheduleInput) error
	ExecutorSchedule(context.Context, string) (ipsentinel.ScheduleView, error)
	DisableExecutorSchedule(context.Context, string, int64) error
}

type RuntimeStore interface {
	RuntimeChannels(context.Context) ([]runtimepkg.ChannelView, error)
	CreateMarketRuntimeRequest(context.Context, string, string, string) (runtimepkg.ChannelRequest, error)
	CreateRuntimeRequest(context.Context, string, runtimepkg.ChannelInput) (runtimepkg.ChannelRequest, error)
	ApproveRuntimeRequest(context.Context, string, string, runtimepkg.ChannelApproval) error
	CancelRuntimeRequest(context.Context, string, string) error
	RuntimeExchange(context.Context, string, runtimepkg.ChannelReport) (runtimepkg.ChannelReply, error)
}
