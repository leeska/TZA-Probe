package notify

import (
	"fmt"
	"strings"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

func eventTime(at int64) string {
	if at <= 0 {
		return "未记录"
	}
	return time.Unix(at, 0).UTC().Format("2006-01-02 15:04:05 UTC")
}

func singleLine(value string) string { return strings.Join(strings.Fields(value), " ") }

func nodeContext(n model.NodeView, message string) string {
	lines := []string{}
	if n.Group != "" {
		lines = append(lines, "分组："+singleLine(n.Group))
	}
	if n.Region != "" {
		lines = append(lines, "地区："+singleLine(n.Region))
	}
	if len(lines) > 0 {
		lines = append(lines, "")
	}
	return strings.Join(append(lines, message), "\n")
}

func trafficDetails(n model.NodeView) string {
	quota := "未设置"
	if n.Billing.TrafficLimit > 0 {
		quota = fmt.Sprintf("%s（已用 %.1f%%）", model.HumanBytes(n.Billing.TrafficLimit), float64(n.TrafficUsed)/float64(n.Billing.TrafficLimit)*100)
	}
	mode := map[string]string{"sum": "双向合计", "max": "出入取较大值", "in": "仅入站", "out": "仅出站"}[n.Billing.TrafficMode]
	if mode == "" {
		mode = "未记录"
	}
	message := fmt.Sprintf("本周期已用：%s\n流量额度：%s\n统计口径：%s", model.HumanBytes(n.TrafficUsed), quota, mode)
	if n.CycleStart > 0 && n.CycleEnd > 0 {
		message += fmt.Sprintf("\n计费周期：%s → %s", eventTime(n.CycleStart), eventTime(n.CycleEnd))
	}
	return message
}

func resourceDetails(n model.NodeView, kind string) string {
	if !n.Online || n.Metrics == nil {
		return "当前指标：不可用（节点离线或尚未上报），不能确认使用率已恢复正常"
	}
	if kind == model.AlertCPU {
		return fmt.Sprintf("当前 CPU：%.1f%%", n.Metrics.CPU)
	}
	if n.Metrics.MemoryTotal == 0 {
		return "当前内存：未上报总容量"
	}
	return fmt.Sprintf("当前内存：%.1f%%（%s / %s）", float64(n.Metrics.MemoryUsed)/float64(n.Metrics.MemoryTotal)*100, model.HumanBytes(int64(n.Metrics.MemoryUsed)), model.HumanBytes(int64(n.Metrics.MemoryTotal)))
}

func renewalDetails(n model.NodeView, now time.Time) string {
	expiry, err := time.Parse(time.RFC3339, n.Billing.ExpiresAt)
	if err != nil {
		return "到期时间：未设置"
	}
	message := renewalMessage(expiry, now)
	if n.Billing.Free {
		message += "\n计费：免费"
	} else if n.Billing.Price > 0 {
		message += fmt.Sprintf("\n续费参考：%g %s", n.Billing.Price, singleLine(n.Billing.Currency))
		if n.Billing.PricePeriod != "" {
			message += " / " + singleLine(n.Billing.PricePeriod)
		}
	}
	return message
}

func ruleDetails(r model.AlertRule) string {
	origin := map[string]string{model.ScopeAll: "全部节点", model.ScopeGroup: "分组规则", model.ScopeNodes: "指定节点"}[r.Scope]
	message := ""
	if origin != "" {
		message += "\n规则来源：" + origin
	}
	if r.RepeatSeconds > 0 {
		message += "\n重复提醒间隔：" + model.HumanDuration(time.Duration(r.RepeatSeconds)*time.Second)
	} else {
		message += "\n重复提醒：关闭（触发、解除各通知一次）"
	}
	return message
}

func recoveryMessage(n model.NodeView, r model.AlertRule, enabled bool, now time.Time) string {
	reason := "告警条件已解除"
	if !enabled {
		reason = "该节点当前未启用此类提醒（规则已关闭、删除或不再覆盖）"
	}
	facts := ""
	switch r.Kind {
	case model.AlertOffline:
		if n.Online {
			facts = "当前状态：在线\n最近上报：" + eventTime(n.LastSeen)
		} else {
			facts = "当前状态：仍未在线\n最后上报：" + eventTime(n.LastSeen)
		}
	case model.AlertCPU, model.AlertMemory:
		facts = resourceDetails(n, r.Kind)
		if enabled && (!n.Online || n.Metrics == nil || (r.Kind == model.AlertMemory && n.Metrics.MemoryTotal == 0)) {
			reason = "节点离线或指标不可用，使用率告警停止评估"
		}
		if enabled {
			facts += fmt.Sprintf("\n当前阈值：%g%%", r.Threshold)
		}
	case model.AlertTraffic:
		facts = trafficDetails(n)
		if enabled {
			facts += fmt.Sprintf("\n当前阈值：%g%%", r.Threshold)
		}
	case model.AlertRenewal:
		facts = renewalDetails(n, now)
	case model.AlertThrottle:
		if n.Line.ThrottlesOnQuota() {
			facts = trafficDetails(n)
		} else {
			facts = fmt.Sprintf("当前窗口平均带宽：%.1f Mbit/s\n当前降速阈值：%d Mbit/s", n.AvgBits/1e6, n.Line.ThrottleMbps)
		}
		if n.Throttled {
			facts += "\n当前判定：仍满足面板推算的降速条件"
		} else {
			facts += "\n当前判定：不再满足面板推算的降速条件"
		}
		facts += "\n说明：面板判定不代表已验证商家实际限速"
	}
	return nodeContext(n, "解除原因："+reason+"\n"+facts)
}

func telegramMessage(e model.Event) string {
	state := "⚪ 告警解除"
	if e.Active {
		state = "🔴 告警触发"
	}
	if e.Kind == "test" {
		state = "🧪 连接测试"
	}
	lines := []string{"TZA Probe · " + state}
	if e.Kind != "test" {
		kind := telegramTitles[e.Kind]
		if kind == "" {
			kind = singleLine(e.Kind)
		}
		lines = append(lines, "类型："+kind)
	} else {
		lines = append(lines, "通道：Telegram")
	}
	if e.NodeName != "" {
		lines = append(lines, "节点："+singleLine(e.NodeName))
	}
	if e.NodeID != "" {
		lines = append(lines, "节点 ID："+singleLine(e.NodeID))
	}
	if e.Message != "" {
		lines = append(lines, "", e.Message)
	}
	lines = append(lines, "", "事件时间："+eventTime(e.At))
	if e.ID > 0 {
		lines = append(lines, fmt.Sprintf("事件编号：#%d", e.ID))
	}
	return strings.Join(lines, "\n")
}
