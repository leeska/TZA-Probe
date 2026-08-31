package jsonrpc

import (
	"context"

	"github.com/komari-monitor/komari/database/auditlog"
	"github.com/komari-monitor/komari/internal/config"
	"github.com/komari-monitor/komari/internal/probe"
	"github.com/komari-monitor/komari/pkg/rpc"
)

func init() {
	RegisterWithGroupAndMeta("getCarrierRouteOptions", rpc.RoleAdmin, adminGetCarrierRouteOptions, &rpc.MethodMeta{
		Name:    "admin:getCarrierRouteOptions",
		Summary: "Get the embedded probe catalogue and independent latency/route settings",
		Returns: "{ enabled: boolean, interval_seconds: number, ping_interval_seconds: number, options: CatalogOption[], selections: CarrierRouteSelection[], ping_selections: CarrierRouteSelection[], route_tasks: CarrierRouteTask[] }",
	})
	RegisterWithGroupAndMeta("setCarrierRouteSelections", rpc.RoleAdmin, adminSetCarrierRouteSelections, &rpc.MethodMeta{
		Name:    "admin:setCarrierRouteSelections",
		Summary: "Replace carrier route region/carrier/IP-family selections",
		Returns: "{ selections: CarrierRouteSelection[] }",
	})
	RegisterWithGroupAndMeta("setCarrierPingSelections", rpc.RoleAdmin, adminSetCarrierPingSelections, &rpc.MethodMeta{
		Name:    "admin:setCarrierPingSelections",
		Summary: "Replace managed carrier latency region/carrier/IP-family selections",
		Returns: "{ selections: CarrierRouteSelection[] }",
	})
	RegisterWithGroupAndMeta("setCarrierRouteTasks", rpc.RoleAdmin, adminSetCarrierRouteTasks, &rpc.MethodMeta{
		Name:    "admin:setCarrierRouteTasks",
		Summary: "Replace independently assigned carrier route tasks",
		Returns: "{ tasks: CarrierRouteTask[] }",
	})
}

func adminGetCarrierRouteOptions(_ context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	enabled, _ := config.GetAs[bool](probe.CarrierRouteEnabledKey, false)
	interval, _ := config.GetAs[int](probe.CarrierRouteIntervalKey, probe.DefaultIntervalSeconds())
	pingInterval, _ := config.GetAs[int](probe.CarrierPingIntervalSecondsKey, probe.DefaultPingIntervalSeconds())
	return map[string]any{
		"enabled":                  enabled,
		"interval_seconds":         probe.NormalizeIntervalSeconds(interval),
		"minimum_interval_seconds": probe.MinimumIntervalSeconds(),
		"options":                  probe.CatalogOptions(),
		"selections":               probe.CurrentSelections(),
		"ping_interval_seconds":    probe.NormalizePingIntervalSeconds(pingInterval),
		"ping_selections":          probe.CurrentPingSelections(),
		"source":                   "embedded",
		"route_tasks":              probe.CurrentRouteTasks(),
	}, nil
}

func adminSetCarrierRouteTasks(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params struct {
		Tasks []probe.CarrierRouteTask `json:"tasks"`
	}
	if err := req.BindParams(&params); err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, "Invalid carrier route tasks: "+err.Error(), nil)
	}
	tasks, err := probe.NormalizeRouteTasks(params.Tasks)
	if err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}
	if err := config.Set(probe.CarrierRouteTasksKey, tasks); err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to save carrier route tasks: "+err.Error(), nil)
	}
	if err := probe.RegisterSchedule(); err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to reload carrier route tasks: "+err.Error(), nil)
	}
	actor, ip := auditActor(ctx)
	auditlog.Log(ip, actor, "update carrier route tasks", "info")
	return map[string]any{"tasks": tasks}, nil
}

func adminSetCarrierPingSelections(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params map[string]any
	if err := req.BindParams(&params); err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, "Invalid carrier ping selections: "+err.Error(), nil)
	}
	raw, ok := params["selections"]
	if !ok {
		return nil, rpc.MakeError(rpc.InvalidParams, "selections is required", nil)
	}
	selections, err := probe.NormalizeSelections(raw)
	if err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, "Invalid carrier ping selections: "+err.Error(), nil)
	}
	if err := config.Set(probe.CarrierPingSelectionsKey, selections); err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to save carrier ping selections: "+err.Error(), nil)
	}
	if err := probe.SyncManagedPingTasks(); err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to sync carrier ping tasks: "+err.Error(), nil)
	}
	actor, ip := auditActor(ctx)
	auditlog.Log(ip, actor, "update carrier ping selections", "info")
	return map[string]any{"selections": selections}, nil
}

func adminSetCarrierRouteSelections(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params map[string]any
	if err := req.BindParams(&params); err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, "Invalid carrier route selections: "+err.Error(), nil)
	}
	raw, ok := params["selections"]
	if !ok {
		return nil, rpc.MakeError(rpc.InvalidParams, "selections is required", nil)
	}
	selections, err := probe.NormalizeSelections(raw)
	if err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, "Invalid carrier route selections: "+err.Error(), nil)
	}
	if err := config.Set(probe.CarrierRouteSelectionsKey, selections); err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to save carrier route selections: "+err.Error(), nil)
	}
	if err := probe.RegisterSchedule(); err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to reload carrier route schedule: "+err.Error(), nil)
	}
	actor, ip := auditActor(ctx)
	auditlog.Log(ip, actor, "update carrier route selections", "info")
	return map[string]any{"selections": selections}, nil
}
