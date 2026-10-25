package mcp

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

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
		Name:        "batch_update_nodes",
		Description: "批量修改节点（面板节点列表的多选工具栏）：把同一份部分字段合并到 node_ids 的每台节点上，全部成功或全部不改。常用于设分组（group）、所在地区（region）、卡片地区（display.route_region / display.latency_region）、公开（public）、商家（vendor）。不能改 name 与 order。",
		InputSchema: schema(map[string]any{
			"node_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "要修改的节点 id，1—500 个"},
			"patch":    map[string]any{"type": "object", "description": "要改的字段，结构同 update_node（嵌套对象按字段合并），例如 {\"group\":\"日本\"} 或 {\"display\":{\"route_region\":\"上海\"}}"},
		}, "node_ids", "patch"),
		Annotations: annotations("Batch update nodes", false, false, true),
	},
	{
		Name:        "rename_group",
		Description: "分组改名：from 分组的全部节点移到 to，作用于 from 的提醒规则一起改。to 是已有分组即合并；to 为空字符串即解散（节点回到未分组）。合并后若同类提醒规则冲突则拒绝、什么都不改。",
		InputSchema: schema(map[string]any{
			"from": map[string]any{"type": "string", "description": "原分组名；空字符串是未分组"},
			"to":   map[string]any{"type": "string", "description": "新分组名；空字符串即解散"},
		}, "from", "to"),
		Annotations: annotations("Rename group", false, false, true),
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
		Name:        "list_alert_rules",
		Description: "列出全部提醒规则（面板「告警与通知 → 提醒规则」）：每条规则一种类型、自己的阈值与重复间隔，以及作用范围 scope（all 全部节点 / group 所列分组 / nodes 所列节点）。同一台节点同一种类型只生效一条：指定节点 > 分组 > 全部节点；某台节点实际生效的规则见 list_nodes 的 alerts。",
		InputSchema: schema(map[string]any{}),
		Annotations: annotations("List alert rules", true, false, true),
	},
	{
		Name:        "create_alert_rule",
		Description: "新建一条提醒规则并返回 id。同一种类型、同一种范围不能有两条规则覆盖同一台节点（例如两条 scope=all 的 CPU 规则），会被拒绝并说明冲突在哪。",
		InputSchema: alertRuleSchema(false),
		Annotations: annotations("Create alert rule", false, false, false),
	},
	{
		Name:        "update_alert_rule",
		Description: "修改提醒规则：只提交要改的字段，其余沿用当前值；提交的列表整份替换。改了 scope 时，新范围用不上的列表若没重新提交会被清掉；exclude_node_ids 在 all 与 group 之间切换时保留。",
		InputSchema: alertRuleSchema(true),
		Annotations: annotations("Update alert rule", false, false, true),
	},
	{
		Name:        "delete_alert_rule",
		Description: "删除一条提醒规则；被它覆盖的节点改由范围更宽的同类型规则接管，没有就不再提醒这一项。",
		InputSchema: schema(map[string]any{
			"rule_id": map[string]any{"type": "string", "description": "规则 id，来自 list_alert_rules"},
		}, "rule_id"),
		Annotations: annotations("Delete alert rule", false, true, true),
	},
	{
		Name:        "list_catalog",
		Description: "目标目录（地区 / 运营商 / IP 协议 / 地址）：内置的离线目录加上已安装的目标集插件（这些目标带 plugin / plugin_name，id 以 plugin: 开头），新建任务时用来挑目标。可按地区、运营商、协议筛选。",
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
		Name:        "list_plugins",
		Description: "列出已安装的声明式插件：kind（notify 通知渠道 / targets 探测目标集）、名称、版本、来源（market 市场 / 本地上传）、它能做的事（permissions）；通知插件带设置表单 settings（update_settings 的 plugin_settings 按它填），目标集带目标个数（目标本身在 list_catalog 里）。",
		InputSchema: schema(map[string]any{}),
		Annotations: annotations("List installed plugins", true, false, true),
	},
	{
		Name:        "get_settings",
		Description: "查看通知渠道状态（Telegram / Webhook / 通知插件 / 关闭）与公开面板显示项（延迟色带时间窗、访客默认明暗与皮肤、站点名称 site_name 与 logo 地址 site_logo；两者空值表示内置的 TZA Probe 品牌、计费汇总单位 price_unit）。通知插件渠道另回 plugin、plugin_name、非密钥设置 plugin_settings 与 plugin_secrets（哪些密钥已保存）；插件失效时回 plugin_error。不返回任何凭据。",
		InputSchema: schema(map[string]any{}),
		Annotations: annotations("Settings", true, false, true),
	},
	{
		Name:        "update_settings",
		Description: "修改设置，只改传了的项。通知出口：channel 取 telegram、webhook、plugin 或 none（关闭通知必须显式传 none）；不传 channel 时沿用当前渠道；Telegram 的 bot_token、chat_id 留空沿用已存的；channel=plugin 时 plugin 是已安装通知插件的 id（list_plugins），plugin_settings 按插件的 settings 填，没传或留空的项沿用已存值（同一插件时）；切换渠道时另一渠道的凭据会被清掉。面板显示项：latency_window_minutes、default_theme、default_skin、site_name（站点名称，传空串恢复默认；logo 只能在后台上传）、price_unit（公开面板顶部每月成本与剩余价值汇总的统一币种，跨币种按固定参考汇率折算）。",
		InputSchema: schema(map[string]any{
			"channel":                map[string]any{"type": "string", "enum": []string{"telegram", "webhook", "plugin", "none"}, "description": "通知渠道；不传则沿用当前渠道"},
			"plugin":                 map[string]any{"type": "string", "description": "channel=plugin 时：已安装通知插件的 id（list_plugins）"},
			"plugin_settings":        map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "description": "通知插件的设置（键见 list_plugins 的 settings）；密钥留空沿用已存的"},
			"webhook":                map[string]any{"type": "string", "description": "Webhook 地址（channel=webhook 时必填）"},
			"bot_token":              map[string]any{"type": "string", "description": "Telegram Bot Token（留空保留已存的）"},
			"chat_id":                map[string]any{"type": "string", "description": "Telegram Chat ID：数字 ID 或 @频道名（留空保留已存的）"},
			"latency_window_minutes": map[string]any{"type": "integer", "description": "公开卡片延迟色带最早显示多久以前的探测（分钟）：色带固定画最近 10 次，早于这个范围的不画"},
			"default_theme":          map[string]any{"type": "string", "enum": model.PanelThemes, "description": "访客默认明暗"},
			"default_skin":           map[string]any{"type": "string", "description": "访客默认皮肤：内置的 " + strings.Join(model.PanelSkins, " / ") + "，或后台已安装的主题包 id"},
			"site_name":              map[string]any{"type": "string", "maxLength": model.MaxSiteNameLen, "description": "站点名称：浏览器标签页标题与公开面板、后台品牌区显示的名字；空串恢复默认的 TZA Probe"},
			"price_unit":             map[string]any{"type": "string", "enum": model.PriceUnits, "description": "公开面板顶部「每月成本 / 剩余价值汇总」统一折算成的货币"},
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
		"name":       map[string]any{"type": "string", "description": "节点名称，必填"},
		"group":      map[string]any{"type": "string", "description": "分组，如 HK"},
		"region":     map[string]any{"type": "string", "description": "节点所在地区"},
		"vendor":     map[string]any{"type": "string", "description": "商家（如 netcup / Hytron），公开卡片上显示为名牌；留空不显示"},
		"decoration": map[string]any{"type": "string", "description": "这台节点卡片上的主题装饰：「包 id/装饰项 id」（当前主题或它的附加包里声明的装饰项）；留空按节点名、地区、默认项自动选"},
		"public":     map[string]any{"type": "boolean", "description": "是否在公开面板展示"},
		"order":      map[string]any{"type": "integer", "description": "排序，越小越靠前"},
		"display": object("公开卡片显示哪些探测结果；两个地区须是内置目录或已有任务用到的目标地区（list_catalog / list_tasks），否则拒绝", map[string]any{
			"route_region":     map[string]any{"type": "string", "description": "卡片回程显示的地区；留空不限地区"},
			"latency_region":   map[string]any{"type": "string", "description": "卡片延迟显示的地区；留空沿用 route_region"},
			"latency_carriers": carrierList(),
			"route_carriers": map[string]any{
				"type": "array", "items": map[string]any{"type": "string", "enum": []string{"电信", "联通", "移动"}},
				"description": "卡片展示哪些运营商的回程（只有三网），留空显示全部",
			},
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

func alertRuleSchema(withID bool) map[string]any {
	ids := func(description string) map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": description}
	}
	properties := map[string]any{
		"kind":             map[string]any{"type": "string", "enum": model.AlertKinds, "description": "offline 离线 / renewal 续费 / traffic 流量 / throttle 达量降速（节点须填 line.throttle_mbps，否则不会触发）/ cpu / memory"},
		"threshold":        map[string]any{"type": "number", "description": "renewal：到期前多少天（0–365）；traffic / cpu / memory：百分比（1–100）；offline、throttle 不填"},
		"hold_seconds":     map[string]any{"type": "integer", "description": "offline：判定离线后再等的秒数（0–86400）；cpu / memory：持续多久才算（30–86400）；其余不填"},
		"repeat_seconds":   map[string]any{"type": "integer", "description": "持续告警的重复提醒间隔：0 只提醒一次，否则 300–604800 秒"},
		"scope":            map[string]any{"type": "string", "enum": []string{model.ScopeAll, model.ScopeGroup, model.ScopeNodes}, "description": "作用范围：all 全部节点（含以后新加的）/ group 所列分组 / nodes 所列节点"},
		"groups":           ids("scope=group 时必填：分组名，空串表示未分组的节点"),
		"node_ids":         ids("scope=nodes 时必填：节点 id，来自 list_nodes"),
		"exclude_node_ids": ids("scope=all / group 时可选：不适用这条规则的节点 id"),
	}
	if withID {
		properties["rule_id"] = map[string]any{"type": "string", "description": "规则 id，来自 list_alert_rules"}
		return schema(properties, "rule_id")
	}
	return schema(properties, "kind", "scope")
}

// withGroupWarning adds a warning when a group rule names a group with no
// nodes: a typo or a renamed group is saved without error and covers nothing.
// The rule is already saved; a failed lookup only drops the warning.
func withGroupWarning(ctx context.Context, svc *service.Service, rule model.AlertRule, payload map[string]any) map[string]any {
	empty, err := svc.EmptyGroups(ctx, rule)
	if err != nil || len(empty) == 0 {
		return payload
	}
	names := make([]string, len(empty))
	for i, g := range empty {
		names[i] = "「" + cmp.Or(g, "默认（未分组）") + "」"
	}
	payload["warning"] = "分组" + strings.Join(names, "、") + "下目前没有节点，这条规则暂时不覆盖任何节点；请核对分组名（list_nodes 的 group）。"
	return payload
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
			"checks": map[string]any{
				"type": "array", "items": map[string]any{"type": "string", "enum": []string{"tcp", "route"}},
				"description": "只对这个目标生效的检测项，须是任务 checks 的子集；省略即任务的全部检测项（例如海外服务只测延迟：[\"tcp\"]）",
			},
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

	case "batch_update_nodes":
		var args struct {
			NodeIDs []string        `json:"node_ids"`
			Patch   json.RawMessage `json:"patch"`
		}
		if e := arguments(params.Arguments, &args); e != nil {
			return nil, e, http.StatusOK
		}
		count, err := svc.BatchPatchNodes(ctx, args.NodeIDs, args.Patch)
		if err != nil {
			return failure("批量修改节点失败", err)
		}
		return toolResult(map[string]any{"updated": count}), nil, 0

	case "rename_group":
		var args struct {
			From string `json:"from"`
			To   string `json:"to"`
		}
		if e := arguments(params.Arguments, &args); e != nil {
			return nil, e, http.StatusOK
		}
		count, err := svc.RenameGroup(ctx, args.From, args.To)
		if err != nil {
			return failure("分组改名失败", err)
		}
		return toolResult(map[string]any{"from": args.From, "to": args.To, "moved": count}), nil, 0

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

	case "list_alert_rules":
		rules, err := svc.AlertRules(ctx)
		if err != nil {
			return failure("读取提醒规则失败", err)
		}
		return toolResult(map[string]any{"rules": rules}), nil, 0

	case "create_alert_rule":
		var rule model.AlertRule
		if e := arguments(params.Arguments, &rule); e != nil {
			return nil, e, http.StatusOK
		}
		id, err := svc.CreateAlertRule(ctx, rule)
		if err != nil {
			return failure("新建提醒规则失败", err)
		}
		return toolResult(withGroupWarning(ctx, svc, rule, map[string]any{"id": id, "created": true})), nil, 0

	case "update_alert_rule", "delete_alert_rule":
		var args struct {
			model.AlertRule
			RuleID string `json:"rule_id"`
		}
		if e := arguments(params.Arguments, &args); e != nil {
			return nil, e, http.StatusOK
		}
		if args.RuleID == "" {
			return toolError("缺少 rule_id；先用 list_alert_rules 获取可用 id。"), nil, 0
		}
		if params.Name == "delete_alert_rule" {
			if err := svc.DeleteAlertRule(ctx, args.RuleID); err != nil {
				return failure("删除提醒规则失败", err)
			}
			return toolResult(map[string]any{"id": args.RuleID, "deleted": true}), nil, 0
		}
		// The stored rule is the base, so a partial edit cannot blank a field.
		rule, err := svc.PatchAlertRule(ctx, args.RuleID, params.Arguments)
		if err != nil {
			return failure("修改提醒规则失败", err)
		}
		return toolResult(withGroupWarning(ctx, svc, rule, map[string]any{"rule": rule, "updated": true})), nil, 0

	case "list_catalog":
		return listCatalog(svc, params)

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

	case "list_plugins":
		if e := arguments(params.Arguments, &struct{}{}); e != nil {
			return nil, e, http.StatusOK
		}
		plugins := svc.PluginList()
		return toolResult(map[string]any{"plugins": plugins, "count": len(plugins)}), nil, 0

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
		return toolResult(settingsView(svc, config, panel)), nil, 0

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
		if !args.Config.IsZero() {
			notification = &args.Config
		}
		config, panel, err := svc.UpdateSettings(ctx, notification, args.PanelPatch)
		if err != nil {
			return failure("保存设置失败", err)
		}
		// Never echo the credentials back, only what is configured.
		return toolResult(settingsView(svc, config, panel)), nil, 0

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
	return s.callMarketTool(ctx, params)
}

// listCatalog filters the built-in offline target list.
func listCatalog(svc *service.Service, params call) (map[string]any, *rpcError, int) {
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
	snapshot, err := svc.Catalog()
	if err != nil {
		return toolError("目标目录无法读取：" + err.Error()), nil, 0
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
		entry := map[string]any{"id": t.ID, "family": t.Family, "region": t.Region, "carrier": t.Carrier, "host": t.Host, "port": t.Port}
		if t.Plugin != "" {
			entry["plugin"], entry["plugin_name"] = t.Plugin, t.PluginName
		}
		matches = append(matches, entry)
	}
	return toolResult(map[string]any{
		"source": snapshot.Source, "snapshot_date": snapshot.SnapshotDate, "plugins": snapshot.Plugins,
		"targets": matches, "count": len(matches), "total": len(snapshot.Targets),
	}), nil, 0
}

// settingsView is what get_settings and update_settings answer with: the
// credential-free notification view plus the panel display settings.
func settingsView(svc *service.Service, config notify.Config, panel model.PanelSettings) map[string]any {
	view := svc.PublicNotification(config)
	view[model.SettingLatencyWindowMinutes] = panel.LatencyWindowMinutes
	view[model.SettingDefaultTheme] = panel.DefaultTheme
	view[model.SettingDefaultSkin] = panel.DefaultSkin
	view[model.SettingSiteName] = panel.SiteName
	view["site_logo"] = panel.SiteLogo
	view[model.SettingPriceUnit] = panel.PriceUnit
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
