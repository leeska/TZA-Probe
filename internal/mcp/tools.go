package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/leeska/TZA-Probe/internal/model"
)

// tool is one entry of tools/list. inputSchema is a JSON Schema object; the
// modern revision loosened it to any JSON Schema 2020-12 keywords, so the plain
// object schema below works for both eras. Annotations tell the client whether a
// tool only reads (annotations() builds them) so it can ask for confirmation
// before a destructive call.
type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`
}

func annotations(title string, readOnly, destructive, idempotent bool) map[string]any {
	return map[string]any{
		"title":           title,
		"readOnlyHint":    readOnly,
		"destructiveHint": destructive,
		"idempotentHint":  idempotent,
		"openWorldHint":   false,
	}
}

// tools are the read-only half of the surface: they answer questions about the
// fleet and cannot change it. They are always advertised in this order, which
// the modern revision asks for so a client can cache the list.
var readTools = []tool{
	{
		Name:        "list_nodes",
		Annotations: annotations("List nodes", true, false, true),
		Description: "列出全部节点：在线状态、最近一次上报的 CPU / 内存 / 磁盘 / 上下行速率、当前计费周期的已用流量、额度与到期时间。",
		InputSchema: schema(map[string]any{}),
	},
	{
		Name:        "list_tasks",
		Annotations: annotations("List probe tasks", true, false, true),
		Description: "列出探测任务定义：名称、检测项（tcp 延迟 / route 回程）、目标地址与运营商、间隔、启停状态和绑定的节点 id。",
		InputSchema: schema(map[string]any{}),
	},
	{
		Name:        "node_history",
		Annotations: annotations("Node host history", true, false, true),
		Description: "读取单个节点的主机历史曲线（CPU、内存、磁盘、上下行速率），按小时聚合，最多约 240 个采样点。",
		InputSchema: schema(map[string]any{
			"node_id": map[string]any{"type": "string", "description": "节点 id，来自 list_nodes"},
			"hours":   map[string]any{"type": "integer", "minimum": 1, "maximum": 720, "description": "回看小时数，默认 24"},
		}, "node_id"),
	},
	{
		Name:        "node_network_history",
		Annotations: annotations("Node network history", true, false, true),
		Description: "读取单个节点的 TCP 网络历史：按目标地址与 IP 协议分开的延迟（毫秒）与连接失败率（%）序列，最多 168 小时。",
		InputSchema: schema(map[string]any{
			"node_id": map[string]any{"type": "string", "description": "节点 id，来自 list_nodes"},
			"hours":   map[string]any{"type": "integer", "minimum": 1, "maximum": 168, "description": "回看小时数，默认 24"},
		}, "node_id"),
	},
	{
		Name:        "node_observations",
		Annotations: annotations("Node observations", true, false, true),
		Description: "读取单个节点每个检测项的最新网络结果，包含回程线路标签（163 / CN2GIA / CN2GT / CTG / 4837 / 9929 / 10099 / CMIN2 / CMI）。跳地址已脱敏，标签未知时不猜。",
		InputSchema: schema(map[string]any{
			"node_id": map[string]any{"type": "string", "description": "节点 id，来自 list_nodes"},
		}, "node_id"),
	},
	{
		Name:        "recent_events",
		Annotations: annotations("Recent alerts", true, false, true),
		Description: "读取告警事件与通知投递结果（离线、续费、流量、降速、CPU、内存的触发与恢复），新的在前。事件保存 90 天：翻页时把上一页最后一条的 id 作为 before 传入。active=true 只列正在告警的项目。",
		InputSchema: schema(map[string]any{
			"before":  map[string]any{"type": "integer", "description": "只取 id 小于它的事件（翻页游标），不传从最新开始"},
			"limit":   map[string]any{"type": "integer", "minimum": 1, "maximum": model.MaxEventPage, "description": "条数，默认 100"},
			"node_id": map[string]any{"type": "string", "description": "只看这个节点"},
			"kind":    map[string]any{"type": "string", "description": "只看这类事件：offline / renewal / traffic / throttle / cpu / memory（load 是拆分前的旧事件）"},
			"active":  map[string]any{"type": "boolean", "description": "只列正在告警的项目，每项取最近一次事件"},
		}),
	},
	{
		Name:        "latency_samples",
		Annotations: annotations("Recent latency samples", true, false, true),
		Description: "读取逐次 TCP 探测结果：每个「节点 / 运营商 / 协议 / 地区」最近若干次连接耗时，失败的那次 latency 为空。",
		InputSchema: schema(map[string]any{
			"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 60, "description": "每组保留的最近次数，默认 20"},
		}),
	},
}

func schema(properties map[string]any, required ...string) map[string]any {
	out := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

// tools is the whole surface in a fixed order: the read-only tools first, then
// the ones that change the fleet.
var tools = append(append([]tool{}, readTools...), adminTools...)

// toolList adds the caching hints the modern revision requires of list results;
// the handshake revisions have no such field.
func (s *Server) toolList(modern bool) map[string]any {
	out := map[string]any{"tools": tools}
	if modern {
		out["ttlMs"] = 3600000
		out["cacheScope"] = "public"
	}
	return out
}

func (s *Server) callTool(ctx context.Context, params call) (map[string]any, *rpcError, int) {
	if params.Name == "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: "tools/call requires a tool name"}, http.StatusOK
	}
	svc := s.service()
	switch params.Name {
	case "list_nodes":
		if e := arguments(params.Arguments, &struct{}{}); e != nil {
			return nil, e, http.StatusOK
		}
		nodes, err := svc.Nodes(ctx)
		if err != nil {
			return toolFailure("读取节点失败", err)
		}
		return toolResult(map[string]any{"nodes": nodes, "count": len(nodes)}), nil, 0

	case "list_tasks":
		if e := arguments(params.Arguments, &struct{}{}); e != nil {
			return nil, e, http.StatusOK
		}
		tasks, err := svc.Tasks(ctx)
		if err != nil {
			return toolFailure("读取任务失败", err)
		}
		return toolResult(map[string]any{"tasks": tasks, "count": len(tasks)}), nil, 0

	case "recent_events":
		var args struct {
			Before int64  `json:"before"`
			Limit  int    `json:"limit"`
			NodeID string `json:"node_id"`
			Kind   string `json:"kind"`
			Active bool   `json:"active"`
		}
		if e := arguments(params.Arguments, &args); e != nil {
			return nil, e, http.StatusOK
		}
		events, err := svc.Events(ctx, model.EventQuery{Before: args.Before, Limit: args.Limit, NodeID: args.NodeID, Kind: args.Kind, Active: args.Active})
		if err != nil {
			return toolFailure("读取告警事件失败", err)
		}
		return toolResult(map[string]any{"events": events, "count": len(events)}), nil, 0

	case "node_history":
		var args historyArgs
		if e := arguments(params.Arguments, &args); e != nil {
			return nil, e, http.StatusOK
		}
		node, failure := s.lookupNode(ctx, args.NodeID)
		if failure != nil {
			return failure, nil, 0
		}
		samples, hours, err := svc.History(ctx, node.ID, args.Hours)
		if err != nil {
			return toolFailure("读取主机历史失败", err)
		}
		return toolResult(map[string]any{"node_id": node.ID, "name": node.Name, "hours": hours, "samples": samples, "count": len(samples)}), nil, 0

	case "node_network_history":
		var args historyArgs
		if e := arguments(params.Arguments, &args); e != nil {
			return nil, e, http.StatusOK
		}
		node, failure := s.lookupNode(ctx, args.NodeID)
		if failure != nil {
			return failure, nil, 0
		}
		series, hours, err := svc.NetworkHistory(ctx, node.ID, args.Hours)
		if err != nil {
			return toolFailure("读取网络历史失败", err)
		}
		return toolResult(map[string]any{"node_id": node.ID, "name": node.Name, "hours": hours, "series": series, "count": len(series)}), nil, 0

	case "node_observations":
		var args historyArgs
		if e := arguments(params.Arguments, &args); e != nil {
			return nil, e, http.StatusOK
		}
		node, failure := s.lookupNode(ctx, args.NodeID)
		if failure != nil {
			return failure, nil, 0
		}
		// Same masking as the HTTP contract (service.Observations redacts).
		out, err := svc.Observations(ctx, node.ID)
		if err != nil {
			return toolFailure("读取观测结果失败", err)
		}
		return toolResult(map[string]any{"node_id": node.ID, "name": node.Name, "observations": out, "count": len(out)}), nil, 0

	case "latency_samples":
		var args struct {
			Limit int `json:"limit"`
		}
		if e := arguments(params.Arguments, &args); e != nil {
			return nil, e, http.StatusOK
		}
		nodes, err := svc.Nodes(ctx)
		if err != nil {
			return toolFailure("读取节点失败", err)
		}
		ids := make([]string, 0, len(nodes))
		for _, n := range nodes {
			ids = append(ids, n.ID)
		}
		// 与公开面板同一个时间窗（settings 里的 latency_window_minutes）。
		samples, limit, minutes, err := svc.LatencySamples(ctx, ids, args.Limit)
		if err != nil {
			return toolFailure("读取延迟样本失败", err)
		}
		return toolResult(map[string]any{"limit": limit, "window_minutes": minutes, "samples": samples, "count": len(samples)}), nil, 0
	}
	return s.callAdminTool(ctx, params)
}

// historyArgs select one node and a look-back window.
type historyArgs struct {
	NodeID string `json:"node_id"`
	Hours  int    `json:"hours"`
}

// lookupNode resolves an id against the current fleet so a typo comes back as a
// readable tool error instead of an empty history. It returns exactly one of a
// node or a finished tool error.
func (s *Server) lookupNode(ctx context.Context, id string) (model.NodeView, map[string]any) {
	if id == "" {
		return model.NodeView{}, toolError("缺少 node_id；先用 list_nodes 获取可用 id。")
	}
	n, err := s.service().Node(ctx, id)
	if errors.Is(err, model.ErrNotFound) {
		return model.NodeView{}, toolError(err.Error() + "；先用 list_nodes 获取可用 id。")
	}
	if err != nil {
		return model.NodeView{}, toolError("读取节点失败：" + err.Error())
	}
	return n, nil
}

// arguments decodes tool arguments, rejecting unknown fields the way the HTTP
// contract does so a misspelled parameter fails loudly.
func arguments(raw json.RawMessage, v any) *rpcError {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		return &rpcError{Code: codeInvalidParams, Message: "invalid arguments: " + err.Error()}
	}
	return nil
}

// toolResult returns the payload as both readable text and structured content.
// Erroring tools answer with a result instead of a JSON-RPC error, which is what
// the specification asks for: the model can read the failure and retry.
func toolResult(payload map[string]any) map[string]any {
	text, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return toolError("结果序列化失败：" + err.Error())
	}
	return map[string]any{
		"content":           []map[string]any{{"type": "text", "text": string(text)}},
		"structuredContent": payload,
	}
}

func toolError(message string) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": message}}, "isError": true}
}

func toolFailure(message string, err error) (map[string]any, *rpcError, int) {
	return toolError(message + "：" + err.Error()), nil, 0
}
