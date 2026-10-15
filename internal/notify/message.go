package notify

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf16"

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

var usageLabels = map[string]string{model.AlertCPU: "CPU", model.AlertMemory: "内存"}

// usage is the CPU or memory percentage the rules compare, false when the node
// is offline or has not reported what the percentage needs.
func usage(n model.NodeView, kind string) (float64, bool) {
	if !n.Online || n.Metrics == nil {
		return 0, false
	}
	if kind == model.AlertCPU {
		return n.Metrics.CPU, true
	}
	if n.Metrics.MemoryTotal == 0 {
		return 0, false
	}
	return float64(n.Metrics.MemoryUsed) / float64(n.Metrics.MemoryTotal) * 100, true
}

func resourceDetails(n model.NodeView, kind string) string {
	used, ok := usage(n, kind)
	switch {
	case !ok && n.Online && n.Metrics != nil:
		return "当前内存：未上报总容量"
	case !ok:
		return "当前指标：不可用（节点离线或尚未上报），不能确认使用率已恢复正常"
	case kind == model.AlertCPU:
		return fmt.Sprintf("当前 CPU：%.1f%%", used)
	}
	return fmt.Sprintf("当前内存：%.1f%%（%s / %s）", used, model.HumanBytes(int64(n.Metrics.MemoryUsed)), model.HumanBytes(int64(n.Metrics.MemoryTotal)))
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
		message += fmt.Sprintf("\n续费参考：%g", n.Billing.Price)
		if currency := singleLine(n.Billing.Currency); currency != "" {
			message += " " + currency
		}
		if singleLine(n.Billing.PricePeriod) != "" {
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
		if _, ok := usage(n, r.Kind); enabled && !ok {
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
		// 线路的降速声明被清掉时没有阈值可比，不写「0 Mbit/s」这种像真阈值的数字。
		if n.Line.ThrottleMbps <= 0 {
			facts = "降速规则：线路当前未声明降速条件，面板不再推算降速"
			break
		}
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

func telegramMessage(e model.Event) string { return messageText(e, "Telegram", telegramLimit) }

// messageText is the full multi-line message: status, kind, node, the
// reading, time and event number. channel names the channel on a test
// message ("" leaves the line out); the reading is cut to keep the whole text
// within limit UTF-16 code units.
func messageText(e model.Event, channel string, limit int) string {
	state := "⚪ 告警解除"
	if e.Active {
		state = "🔴 告警触发"
	}
	if e.Kind == "test" {
		state = "🧪 连接测试"
	}
	lines := []string{"TZA Probe · " + state}
	if e.Kind != "test" {
		lines = append(lines, "类型："+kindName(e.Kind))
	} else if channel != "" {
		lines = append(lines, "通道："+channel)
	}
	if e.NodeName != "" {
		lines = append(lines, "节点："+singleLine(e.NodeName))
	}
	if e.NodeID != "" {
		lines = append(lines, "节点 ID："+singleLine(e.NodeID))
	}
	footer := []string{"", "事件时间：" + eventTime(e.At)}
	if e.ID > 0 {
		footer = append(footer, fmt.Sprintf("事件编号：#%d", e.ID))
	}
	if e.Message != "" {
		// Telegram 拒收超长正文（HTTP 400），那样重试多少次都发不出去；超出时截断正文，
		// 保留头部身份和尾部的时间、事件编号，便于回后台核对完整记录。
		budget := limit - utf16Len(strings.Join(lines, "\n")) - utf16Len(strings.Join(footer, "\n")) - utf16Len("\n\n\n")
		lines = append(lines, "", truncateUTF16(e.Message, budget))
	}
	return strings.Join(append(lines, footer...), "\n")
}

// kindName is the Chinese name of an event kind (the raw kind when unknown).
func kindName(kind string) string {
	if kind == "test" {
		return "连接测试"
	}
	if name := telegramTitles[kind]; name != "" {
		return name
	}
	return singleLine(kind)
}

// telegramLimit is sendMessage's text limit, counted in UTF-16 code units.
const telegramLimit = 4096

const truncatedNote = "\n…（内容过长已截断，完整内容见后台事件记录）"

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

func truncateUTF16(s string, budget int) string {
	if utf16Len(s) <= budget {
		return s
	}
	budget -= utf16Len(truncatedNote)
	used := 0
	for i, r := range s {
		if used+utf16.RuneLen(r) > budget {
			return s[:i] + truncatedNote
		}
		used += utf16.RuneLen(r)
	}
	return s
}
