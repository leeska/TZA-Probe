package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/leeska/TZA-Probe/internal/catalog"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/notify"
	"github.com/leeska/TZA-Probe/internal/service"
)

// adminTools are the ones that change the fleet: they mirror what the panel
// does, so an AI client can add a host, edit billing, bind nodes to a task or
// point the alerts at a new webhook without a browser. They are destructive —
// the annotations say so — and every one of them needs the administrator key
// the panel uses.
var adminTools = []tool{
	{
		Name:        "create_node",
		Description: "新建节点并返回节点 id、一次性 Agent 密钥（只返回这一次）与接入命令。凡面板上能填的节点字段都可设。",
		InputSchema: nodeSchema(false),
		Annotations: annotations("Create node", false, true, false),
	},
	{
		Name:        "update_node",
		Description: "修改节点：只提交要改的字段，其余沿用当前值（先读后写，不会把没提到的字段清零）。",
		InputSchema: nodeSchema(true),
		Annotations: annotations("Update node", false, false, true),
	},
	{
		Name:        "delete_node",
		Description: "删除节点，连同它的令牌与绑定。",
		InputSchema: schema(map[string]any{
			"node_id": map[string]any{"type": "string", "description": "节点 id"},
		}, "node_id"),
		Annotations: annotations("Delete node", false, true, false),
	},
	{
		Name:        "rotate_node_token",
		Description: "轮换节点密钥（面板的「接入」按钮）：旧密钥立即失效，返回新密钥与接入命令，只返回这一次。",
		InputSchema: schema(map[string]any{
			"node_id": map[string]any{"type": "string", "description": "节点 id"},
		}, "node_id"),
		Annotations: annotations("Rotate node token", false, true, false),
	},
	{
		Name:        "correct_node_traffic",
		Description: "校正当前计费周期的已用流量（面板的「用量校正」）：后续增量继续累计，下周期自动失效。",
		InputSchema: schema(map[string]any{
			"node_id": map[string]any{"type": "string", "description": "节点 id"},
			"used":    map[string]any{"type": "integer", "description": "校正后的已用字节数，十进制"},
			"cycle":   map[string]any{"type": "integer", "description": "周期起点（秒级 Unix 时间戳），取节点当前的 cycle_start；与服务器不一致会被拒绝"},
		}, "node_id", "used", "cycle"),
		Annotations: annotations("Correct cycle traffic", false, false, false),
	},
	{
		Name:        "set_node_alerts",
		Description: "把同一份提醒规则写给一批节点（面板「告警与通知 → 规则总览」的批量设置）：整份替换这些节点的 alerts，一个事务，任何一台不合规（如没填降速阈值却开降速告警）整批不改并指出是哪台。",
		InputSchema: schema(map[string]any{
			"node_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "节点 id 列表"},
			"alerts":   alertsSchema(),
		}, "node_ids", "alerts"),
		Annotations: annotations("Set alert rules", false, false, true),
	},
	{
		Name:        "list_catalog",
		Description: "内置的离线目标目录（地区 / 运营商 / IP 协议 / 地址），新建任务时用来挑目标。可按地区、运营商、协议筛选。",
		InputSchema: schema(map[string]any{
			"region":  map[string]any{"type": "string", "description": "地区，如 上海 / 香港"},
			"carrier": map[string]any{"type": "string", "enum": []string{"电信", "联通", "移动", "其他"}, "description": "运营商"},
			"family":  map[string]any{"type": "string", "enum": []string{"ipv4", "ipv6"}, "description": "IP 协议"},
		}),
		Annotations: annotations("List probe catalog", true, false, true),
	},
	{
		Name:        "create_task",
		Description: "新建任务：type=probe（网络探测：probe 里给检测项 tcp 延迟 / route 回程、目标列表与间隔）或 type=unlock（解锁检测：unlock 里给间隔与协议），以及绑定节点。返回新任务 id。",
		InputSchema: taskSchema(false),
		Annotations: annotations("Create probe task", false, false, false),
	},
	{
		Name:        "update_task",
		Description: "修改任务：只提交要改的字段，其余沿用当前值（probe / unlock 按字段合并）；改了检测项或目标会清除该任务的旧结果，只改名称、间隔、启停或绑定则保留。",
		InputSchema: taskSchema(true),
		Annotations: annotations("Update probe task", false, false, true),
	},
	{
		Name:        "delete_task",
		Description: "删除探测任务及其结果。",
		InputSchema: schema(map[string]any{
			"task_id": map[string]any{"type": "string", "description": "任务 id"},
		}, "task_id"),
		Annotations: annotations("Delete probe task", false, true, false),
	},
	{
		Name:        "control_task",
		Description: "任务操作：pause 暂停、resume 恢复、run 立即安排执行（等 Agent 领取）、bind 替换绑定节点（空列表停止调度）。",
		InputSchema: schema(map[string]any{
			"task_id":  map[string]any{"type": "string", "description": "任务 id"},
			"action":   map[string]any{"type": "string", "enum": []string{"pause", "resume", "run", "bind"}, "description": "操作"},
			"node_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "仅 bind 使用：新的绑定节点 id 列表"},
		}, "task_id", "action"),
		Annotations: annotations("Control probe task", false, false, false),
	},
	{
		Name:        "task_runs",
		Description: "任务的执行记录：每个绑定节点各目标最近一次领取与完成时间、结果和失败原因。",
		InputSchema: schema(map[string]any{
			"task_id": map[string]any{"type": "string", "description": "任务 id"},
		}, "task_id"),
		Annotations: annotations("Task execution records", true, false, true),
	},
	{
		Name:        "get_settings",
		Description: "查看通知渠道状态（Telegram / Webhook / 关闭）与公开面板显示项（延迟色带时间窗、访客默认明暗与皮肤）。不返回任何凭据。",
		InputSchema: schema(map[string]any{}),
		Annotations: annotations("Settings", true, false, true),
	},
	{
		Name:        "update_settings",
		Description: "修改设置，只改传了的项。通知出口：channel 取 telegram、webhook 或 none（关闭通知必须显式传 none）；不传 channel 时沿用当前渠道；Telegram 的 bot_token、chat_id 留空沿用已存的；切换渠道时另一渠道的凭据会被清掉。面板显示项：latency_window_minutes、default_theme、default_skin。",
		InputSchema: schema(map[string]any{
			"channel":                map[string]any{"type": "string", "enum": []string{"telegram", "webhook", "none"}, "description": "通知渠道；不传则沿用当前渠道"},
			"webhook":                map[string]any{"type": "string", "description": "Webhook 地址（channel=webhook 时必填）"},
			"bot_token":              map[string]any{"type": "string", "description": "Telegram Bot Token（留空保留已存的）"},
			"chat_id":                map[string]any{"type": "string", "description": "Telegram Chat ID：数字 ID 或 @频道名（留空保留已存的）"},
			"latency_window_minutes": map[string]any{"type": "integer", "description": "公开卡片延迟色带回看的分钟数"},
			"default_theme":          map[string]any{"type": "string", "enum": model.PanelThemes, "description": "访客默认明暗"},
			"default_skin":           map[string]any{"type": "string", "enum": model.PanelSkins, "description": "访客默认皮肤"},
		}),
		Annotations: annotations("Update notification settings", false, false, true),
	},
	{
		Name:        "test_notification",
		Description: "向已配置的通知渠道发送一条测试通知。",
		InputSchema: schema(map[string]any{}),
		Annotations: annotations("Send test notification", false, false, false),
	},
}

// nodeArgs are the panel's node form: every field model.Node accepts, plus the
// id the write needs.
type nodeArgs struct {
	NodeID string `json:"node_id"`
	model.Node
}

type taskArgs struct {
	TaskID string `json:"task_id"`
	model.Task
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func nodeSchema(withID bool) map[string]any {
	object := func(description string, properties map[string]any) map[string]any {
		return map[string]any{"type": "object", "additionalProperties": false, "description": description, "properties": properties}
	}
	properties := map[string]any{
		"name":   map[string]any{"type": "string", "description": "节点名称，必填"},
		"group":  map[string]any{"type": "string", "description": "分组，如 HK"},
		"region": map[string]any{"type": "string", "description": "节点所在地区"},
		"public": map[string]any{"type": "boolean", "description": "是否在公开面板展示"},
		"order":  map[string]any{"type": "integer", "description": "排序，越小越靠前"},
		"display": object("公开卡片显示哪些探测结果；地区须与所绑任务的目标地区逐字一致", map[string]any{
			"route_region":     map[string]any{"type": "string", "description": "卡片回程显示的地区；留空不限地区"},
			"latency_region":   map[string]any{"type": "string", "description": "卡片延迟显示的地区；留空沿用 route_region"},
			"latency_carriers": carrierList(),
		}),
		"billing": object("价格与流量周期；新建时 reset_at 与 traffic_mode 必填", map[string]any{
			"reset_at":      map[string]any{"type": "string", "description": "计费周期锚点，RFC3339 精确到秒，如 2026-09-01T00:00:00Z；其日与时分秒逐月顺延"},
			"traffic_limit": map[string]any{"type": "integer", "description": "周期流量额度（十进制字节），0 表示不限"},
			"traffic_mode":  map[string]any{"type": "string", "enum": []string{"sum", "max", "in", "out"}, "description": "流量统计方式：双向合计 / 取较大方向 / 仅下载 / 仅上传"},
			"free":          map[string]any{"type": "boolean", "description": "免费机器：卡片写「免费」，保存时价格与价格周期清空"},
			"price":         map[string]any{"type": "number", "description": "价格，用于显示；free=true 时忽略"},
			"currency":      map[string]any{"type": "string", "description": "币种，如 CNY"},
			"price_period":  map[string]any{"type": "string", "description": "价格周期，如 月 / 年；不接受「免费」，免费请用 free"},
			"expires_at":    map[string]any{"type": "string", "description": "到期时间 RFC3339；留空表示不设"},
		}),
		"line": object("线路带宽与商家的降速规则", map[string]any{
			"mbps":           map[string]any{"type": "integer", "minimum": 0, "maximum": 1000000, "description": "线路带宽（Mbit/s），0 表示不标注"},
			"throttle_mbps":  map[string]any{"type": "integer", "minimum": 0, "maximum": 1000000, "description": "降速后的速率（Mbit/s）：德国商家那类写「近 24 小时平均超过 100 Mbit/s 降速到 100 Mbit/s」，DMIT T1 那类写用满额度后限速的 50/100，0 表示不设"},
			"throttle_hours": map[string]any{"type": "integer", "minimum": 0, "maximum": 168, "description": "降速统计窗口（小时），0 或省略为默认 24；仅 throttle_on=bandwidth 时有效"},
			"throttle_on":    map[string]any{"type": "string", "enum": []string{"bandwidth", "quota"}, "description": "降速触发条件：bandwidth=窗口平均带宽达到 throttle_mbps（省略即为此值）；quota=本周期用量用满 billing.traffic_limit 后按商家规则降到 throttle_mbps（不计量但限速）"},
		}),
		"alerts": alertsSchema(),
	}
	if withID {
		properties["node_id"] = map[string]any{"type": "string", "description": "节点 id，来自 list_nodes"}
		return schema(properties, "node_id")
	}
	return schema(properties, "name", "billing")
}

func carrierList() map[string]any {
	return map[string]any{
		"type": "array", "items": map[string]any{"type": "string", "enum": []string{"电信", "联通", "移动", "其他"}},
		"description": "卡片展示哪些运营商的延迟，留空显示全部",
	}
}

func alertsSchema() map[string]any {
	properties := map[string]any{
		"offline":         map[string]any{"type": "boolean", "description": "离线提醒"},
		"offline_seconds": map[string]any{"type": "integer", "minimum": 0, "maximum": 86400, "description": "判定离线后额外等待秒数，默认 60"},
		"repeat_seconds":  map[string]any{"type": "integer", "minimum": 0, "maximum": 604800, "description": "重复提醒间隔：0 表示只触发/恢复各一次，非零为 300–604800 秒"},
		"renewal":         map[string]any{"type": "boolean", "description": "到期续费提醒"},
		"renewal_days":    map[string]any{"type": "integer", "minimum": 0, "maximum": 365, "description": "提前多少天提醒，默认 7"},
		"traffic":         map[string]any{"type": "boolean", "description": "流量额度提醒"},
		"traffic_percent": map[string]any{"type": "number", "minimum": 0, "maximum": 100, "description": "流量阈值百分比，默认 80"},
		"throttle":        map[string]any{"type": "boolean", "description": "达量降速提醒：窗口平均带宽达到节点上的降速阈值时提醒"},
		"load":            map[string]any{"type": "boolean", "description": "CPU / 内存负载提醒"},
		"cpu_percent":     map[string]any{"type": "number", "minimum": 0, "maximum": 100, "description": "CPU 阈值，默认 90"},
		"memory_percent":  map[string]any{"type": "number", "minimum": 0, "maximum": 100, "description": "内存阈值，默认 90"},
		"hold_seconds":    map[string]any{"type": "integer", "minimum": 30, "maximum": 86400, "description": "负载需持续的秒数，默认 300"},
	}
	return map[string]any{"type": "object", "properties": properties, "additionalProperties": false, "description": "该节点的提醒规则；不传表示不改"}
}

func taskSchema(withID bool) map[string]any {
	properties := map[string]any{
		"name": map[string]any{"type": "string", "description": "任务名称，必填"},
		"type": map[string]any{"type": "string", "enum": []string{model.TaskProbe, model.TaskUnlock}, "description": "任务类型：probe 为网络探测（填 probe）；unlock 为解锁检测（填 unlock），由节点自己的出口逐项检测流媒体 / AI / 交易所（目录见 GET /api/v1/unlock-catalog）"},
		"probe": map[string]any{
			"type": "object", "additionalProperties": false,
			"description": "type=probe 时必填：每个检测项都对每个目标执行",
			"properties": map[string]any{
				"checks":         map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"tcp", "route"}}, "description": "检测项：tcp 延迟 / route 回程；两者可同时选"},
				"targets":        map[string]any{"type": "array", "items": targetSchema(), "description": "目标列表，最多 200 个；目录见 list_catalog"},
				"interval":       map[string]any{"type": "integer", "minimum": 30, "maximum": 86400, "description": "TCP 探测间隔秒数（选了 tcp 时必填）"},
				"route_interval": map[string]any{"type": "integer", "minimum": 900, "maximum": 86400, "description": "回程探测间隔秒数，最短 900（选了 route 时必填）"},
			},
		},
		"unlock": map[string]any{
			"type": "object", "additionalProperties": false,
			"description": "type=unlock 时必填",
			"properties": map[string]any{
				"interval": map[string]any{"type": "integer", "minimum": model.UnlockMinInterval, "maximum": 86400, "description": "检测间隔秒数，最短 3600"},
				"family":   map[string]any{"type": "string", "enum": []string{"", "ipv4", "ipv6"}, "description": "出口协议；留空自动（纯 IPv6 机器走 IPv6）"},
			},
		},
		"enabled":  map[string]any{"type": "boolean", "description": "是否启用调度"},
		"node_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "绑定节点 id；空列表不执行"},
	}
	if withID {
		properties["task_id"] = map[string]any{"type": "string", "description": "任务 id，来自 list_tasks"}
		return schema(properties, "task_id")
	}
	// probe / unlock 二选一，具体约束由 model.Task.Validate 把关，报错信息比 JSON Schema 更具体。
	return schema(properties, "name", "type", "node_ids")
}

func targetSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"host":    map[string]any{"type": "string", "description": "IPv4 / IPv6 字面量或 ASCII 域名"},
			"port":    map[string]any{"type": "integer", "minimum": 1, "maximum": 65535, "description": "端口"},
			"family":  map[string]any{"type": "string", "enum": []string{"ipv4", "ipv6"}, "description": "地址族，须与地址一致"},
			"region":  map[string]any{"type": "string", "description": "地区，如 上海；须与节点的卡片代表地区一致"},
			"carrier": map[string]any{"type": "string", "enum": []string{"电信", "联通", "移动", "其他"}, "description": "运营商；回程任务必填"},
		},
		"required": []string{"host", "port", "family"}, "additionalProperties": false,
	}
}

func (s *Server) callAdminTool(ctx context.Context, params call) (map[string]any, *rpcError, int) {
	svc := s.service()
	switch params.Name {
	case "create_node":
		var args nodeArgs
		if e := arguments(params.Arguments, &args); e != nil {
			return nil, e, http.StatusOK
		}
		id, token, err := svc.CreateNode(ctx, args.Node)
		if err != nil {
			return failure("新建节点失败", err)
		}
		return toolResult(s.credentials(ctx, id, token)), nil, 0

	case "update_node":
		var args nodeArgs
		if e := arguments(params.Arguments, &args); e != nil {
			return nil, e, http.StatusOK
		}
		node, missing := s.lookupNode(ctx, args.NodeID)
		if missing != nil {
			return missing, nil, 0
		}
		// The stored node is the base, so a partial edit cannot blank a field.
		if _, err := svc.PatchNode(ctx, node.ID, params.Arguments); err != nil {
			return failure("修改节点失败", err)
		}
		return toolResult(map[string]any{"id": node.ID, "name": node.Name, "updated": true}), nil, 0

	case "delete_node":
		var args nodeArgs
		if e := arguments(params.Arguments, &args); e != nil {
			return nil, e, http.StatusOK
		}
		if args.NodeID == "" {
			return toolError("缺少 node_id；先用 list_nodes 获取可用 id。"), nil, 0
		}
		// Deleting is destructive, so say which node it was.
		name := args.NodeID
		if node, missing := s.lookupNode(ctx, args.NodeID); missing == nil {
			name = node.Name
		}
		if err := svc.DeleteNode(ctx, args.NodeID); err != nil {
			return failure("删除节点失败", err)
		}
		return toolResult(map[string]any{"id": args.NodeID, "name": name, "deleted": true}), nil, 0

	case "rotate_node_token":
		var args nodeArgs
		if e := arguments(params.Arguments, &args); e != nil {
			return nil, e, http.StatusOK
		}
		if args.NodeID == "" {
			return toolError("缺少 node_id；先用 list_nodes 获取可用 id。"), nil, 0
		}
		token, err := svc.RotateToken(ctx, args.NodeID)
		if err != nil {
			return failure("轮换节点密钥失败", err)
		}
		return toolResult(s.credentials(ctx, args.NodeID, token)), nil, 0

	case "correct_node_traffic":
		var args struct {
			NodeID string `json:"node_id"`
			Used   int64  `json:"used"`
			Cycle  int64  `json:"cycle"`
		}
		if e := arguments(params.Arguments, &args); e != nil {
			return nil, e, http.StatusOK
		}
		if args.NodeID == "" {
			return toolError("缺少 node_id；先用 list_nodes 获取可用 id。"), nil, 0
		}
		if err := svc.CorrectTraffic(ctx, args.NodeID, args.Used, args.Cycle); err != nil {
			if errors.Is(err, model.ErrConflict) {
				return toolError("周期已变化：先用 list_nodes 取当前 cycle_start，再校正。"), nil, 0
			}
			return failure("用量校正失败", err)
		}
		return toolResult(map[string]any{"node_id": args.NodeID, "used": args.Used, "cycle": args.Cycle, "corrected": true}), nil, 0

	case "set_node_alerts":
		var args struct {
			NodeIDs []string     `json:"node_ids"`
			Alerts  model.Alerts `json:"alerts"`
		}
		if e := arguments(params.Arguments, &args); e != nil {
			return nil, e, http.StatusOK
		}
		if err := svc.SetAlerts(ctx, args.NodeIDs, args.Alerts); err != nil {
			return failure("设置提醒规则失败", err)
		}
		return toolResult(map[string]any{"node_ids": args.NodeIDs, "alerts": args.Alerts, "updated": true}), nil, 0

	case "list_catalog":
		return listCatalog(params)

	case "create_task":
		var args taskArgs
		if e := arguments(params.Arguments, &args); e != nil {
			return nil, e, http.StatusOK
		}
		id, err := svc.CreateTask(ctx, args.Task)
		if err != nil {
			return failure("新建任务失败", err)
		}
		return toolResult(map[string]any{"id": id, "created": true}), nil, 0

	case "update_task":
		var args taskArgs
		if e := arguments(params.Arguments, &args); e != nil {
			return nil, e, http.StatusOK
		}
		current, missing := s.lookupTask(ctx, args.TaskID)
		if missing != nil {
			return missing, nil, 0
		}
		if _, err := svc.PatchTask(ctx, current.ID, params.Arguments); err != nil {
			return failure("修改任务失败", err)
		}
		return toolResult(map[string]any{"id": current.ID, "updated": true}), nil, 0

	case "delete_task":
		var args taskArgs
		if e := arguments(params.Arguments, &args); e != nil {
			return nil, e, http.StatusOK
		}
		if args.TaskID == "" {
			return toolError("缺少 task_id；先用 list_tasks 获取可用 id。"), nil, 0
		}
		if err := svc.DeleteTask(ctx, args.TaskID); err != nil {
			return failure("删除任务失败", err)
		}
		return toolResult(map[string]any{"id": args.TaskID, "deleted": true}), nil, 0

	case "control_task":
		var args struct {
			TaskID  string   `json:"task_id"`
			Action  string   `json:"action"`
			NodeIDs []string `json:"node_ids"`
		}
		if e := arguments(params.Arguments, &args); e != nil {
			return nil, e, http.StatusOK
		}
		if args.TaskID == "" {
			return toolError("缺少 task_id；先用 list_tasks 获取可用 id。"), nil, 0
		}
		var err error
		switch args.Action {
		case "pause", "resume":
			err = svc.SetTaskEnabled(ctx, args.TaskID, args.Action == "resume")
		case "run":
			err = svc.RunTask(ctx, args.TaskID)
		case "bind":
			err = svc.BindTasks(ctx, []string{args.TaskID}, args.NodeIDs)
		default:
			return nil, &rpcError{Code: codeInvalidParams, Message: "action 须为 pause、resume、run 或 bind"}, http.StatusOK
		}
		if errors.Is(err, model.ErrConflict) {
			return toolError("任务未启用或未绑定节点，无法执行：先 resume 并 bind 节点。"), nil, 0
		}
		if err != nil {
			return failure("任务操作失败", err)
		}
		return toolResult(map[string]any{"task_id": args.TaskID, "action": args.Action, "node_ids": args.NodeIDs, "ok": true}), nil, 0

	case "task_runs":
		var args taskArgs
		if e := arguments(params.Arguments, &args); e != nil {
			return nil, e, http.StatusOK
		}
		if _, missing := s.lookupTask(ctx, args.TaskID); missing != nil {
			return missing, nil, 0
		}
		runs, err := svc.TaskRuns(ctx, args.TaskID)
		if err != nil {
			return toolFailure("读取执行记录失败", err)
		}
		return toolResult(map[string]any{"task_id": args.TaskID, "runs": runs, "count": len(runs)}), nil, 0

	case "get_settings":
		if e := arguments(params.Arguments, &struct{}{}); e != nil {
			return nil, e, http.StatusOK
		}
		config, err := svc.Notification(ctx)
		if err != nil {
			return toolFailure("读取通知配置失败", err)
		}
		panel, err := svc.Panel(ctx)
		if err != nil {
			return toolFailure("读取面板设置失败", err)
		}
		return toolResult(settingsView(config, panel)), nil, 0

	case "update_settings":
		var args struct {
			notify.Config
			model.PanelPatch
		}
		if e := arguments(params.Arguments, &args); e != nil {
			return nil, e, http.StatusOK
		}
		// 只改传了的项：AI 客户端常只传想改的那一个字段（比如只换 chat_id），按面板表单
		// 「整份提交、空值即清空」的语义处理会把整个 Telegram 配置清成 none。
		var notification *notify.Config
		if args.Channel != "" || args.Webhook != "" || args.BotToken != "" || args.ChatID != "" {
			notification = &args.Config
		}
		config, panel, err := svc.UpdateSettings(ctx, notification, args.PanelPatch)
		if err != nil {
			return failure("保存设置失败", err)
		}
		// Never echo the credentials back, only what is configured.
		return toolResult(settingsView(config, panel)), nil, 0

	case "test_notification":
		if e := arguments(params.Arguments, &struct{}{}); e != nil {
			return nil, e, http.StatusOK
		}
		channel, err := svc.TestNotification(ctx)
		var sendErr service.SendError
		switch {
		case errors.Is(err, model.ErrInvalid):
			return toolError("请先配置通知渠道（update_settings）。"), nil, 0
		case errors.Is(err, service.ErrNoSender):
			return toolError("当前 Server 没有可用的通知发送器。"), nil, 0
		case errors.As(err, &sendErr):
			return toolError(err.Error()), nil, 0
		case err != nil:
			return toolFailure("读取通知配置失败", err)
		}
		return toolResult(map[string]any{"sent": true, "channel": channel}), nil, 0
	}
	return nil, &rpcError{Code: codeInvalidParams, Message: "unknown tool: " + params.Name}, http.StatusOK
}

// listCatalog filters the built-in offline target list.
func listCatalog(params call) (map[string]any, *rpcError, int) {
	var args struct {
		Region  string `json:"region"`
		Carrier string `json:"carrier"`
		Family  string `json:"family"`
	}
	if e := arguments(params.Arguments, &args); e != nil {
		return nil, e, http.StatusOK
	}
	// The schema lists the values, but a client may ignore the hint: an
	// impossible filter must fail loudly instead of answering "no targets".
	if args.Carrier != "" && !oneOf(args.Carrier, "电信", "联通", "移动", "其他") {
		return nil, &rpcError{Code: codeInvalidParams, Message: "carrier 须为 电信、联通、移动 或 其他"}, http.StatusOK
	}
	if args.Family != "" && !oneOf(args.Family, "ipv4", "ipv6") {
		return nil, &rpcError{Code: codeInvalidParams, Message: "family 须为 ipv4 或 ipv6"}, http.StatusOK
	}
	var snapshot struct {
		Source       string `json:"source"`
		SnapshotDate string `json:"snapshot_date"`
		Targets      []struct {
			ID      string `json:"id"`
			Family  string `json:"family"`
			Region  string `json:"region"`
			Carrier string `json:"carrier"`
			Host    string `json:"host"`
			Port    int    `json:"port"`
			Source  string `json:"source"`
		} `json:"targets"`
	}
	if err := json.Unmarshal(catalog.JSON, &snapshot); err != nil {
		return toolError("内置目录无法解析：" + err.Error()), nil, 0
	}
	matches := make([]any, 0, len(snapshot.Targets))
	for _, t := range snapshot.Targets {
		if args.Region != "" && !strings.Contains(t.Region, args.Region) {
			continue
		}
		if args.Carrier != "" && t.Carrier != args.Carrier {
			continue
		}
		if args.Family != "" && t.Family != args.Family {
			continue
		}
		matches = append(matches, map[string]any{"id": t.ID, "family": t.Family, "region": t.Region, "carrier": t.Carrier, "host": t.Host, "port": t.Port})
	}
	return toolResult(map[string]any{
		"source": snapshot.Source, "snapshot_date": snapshot.SnapshotDate,
		"targets": matches, "count": len(matches), "total": len(snapshot.Targets),
	}), nil, 0
}

// settingsView is what get_settings and update_settings answer with: the
// credential-free notification view plus the panel display settings.
func settingsView(config notify.Config, panel model.PanelSettings) map[string]any {
	view := config.Public()
	view[model.SettingLatencyWindowMinutes] = panel.LatencyWindowMinutes
	view[model.SettingDefaultTheme] = panel.DefaultTheme
	view[model.SettingDefaultSkin] = panel.DefaultSkin
	return view
}

// credentials is the shape the panel's 接入 dialog shows: the node id, the
// one-time key and the command an Agent host runs. It is the only place a node
// key leaves the Server, exactly like the panel's rotate button.
func (s *Server) credentials(ctx context.Context, id, token string) map[string]any {
	endpoint := baseURL(ctx)
	out := map[string]any{
		"id":              id,
		"token":           token,
		"endpoint":        endpoint,
		"install_command": installCommand(endpoint, token),
	}
	if strings.HasPrefix(endpoint, "http://") {
		out["warning"] = "当前地址是明文 HTTP，命令只适用于可信私网；生产环境请从 HTTPS 地址调用。"
	}
	return out
}

// lookupTask resolves a task id so a typo comes back as a readable tool error.
func (s *Server) lookupTask(ctx context.Context, id string) (model.Task, map[string]any) {
	if id == "" {
		return model.Task{}, toolError("缺少 task_id；先用 list_tasks 获取可用 id。")
	}
	t, err := s.service().Task(ctx, id)
	if errors.Is(err, model.ErrNotFound) {
		return model.Task{}, toolError(err.Error() + "；先用 list_tasks 获取可用 id。")
	}
	if err != nil {
		return model.Task{}, toolError("读取任务失败：" + err.Error())
	}
	return t, nil
}

// failure turns a service error into what the caller can act on: input the
// caller got wrong is a protocol error, everything else is a readable result.
func failure(message string, err error) (map[string]any, *rpcError, int) {
	if errors.Is(err, model.ErrInvalid) {
		return nil, &rpcError{Code: codeInvalidParams, Message: err.Error()}, http.StatusOK
	}
	return toolFailure(message, err)
}

type baseURLKey struct{}

// BaseURL is how an Agent would reach this Server, built the same way the panel
// builds it from location.origin. Behind a TLS proxy the proxy must set
// X-Forwarded-Proto, otherwise the install command comes out with http.
func BaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwarded := r.Header.Get("X-Forwarded-Proto"); forwarded != "" {
		scheme = strings.TrimSpace(strings.Split(forwarded, ",")[0])
	}
	return scheme + "://" + r.Host
}

func baseURL(ctx context.Context) string {
	value, _ := ctx.Value(baseURLKey{}).(string)
	return value
}

// installCommand mirrors web/agent-setup.js: the script is static and served by
// this Server, the endpoint and the node key travel as its arguments.
func installCommand(endpoint, token string) string {
	return "curl -fsSL " + endpoint + "/install.sh | sh -s -- --endpoint " + endpoint + " --token " + token
}
