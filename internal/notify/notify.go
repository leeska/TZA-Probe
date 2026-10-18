// Package notify owns alert rules and delivery orchestration. Persistence and
// delivery are ports, so adding a transport does not change rule evaluation.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

type Repository interface {
	Nodes(context.Context, time.Time) ([]model.NodeView, error)
	Transition(context.Context, model.Node, string, func(active, repeat bool) string, bool, int, int, time.Time) error
	PendingEvents(context.Context, time.Time) ([]model.Event, error)
	Delivery(context.Context, model.Event, bool, time.Time) error
}

// Channels supplies the configured channel. A Channel of "none" means
// notifications are off: events are still recorded, just not delivered.
type Channels interface {
	NotificationConfig(context.Context) (Config, error)
}

type Sender interface {
	Send(context.Context, Config, model.Event) error
}

// throttleMessage 是降速告警文案。两种规则读的不是同一个事实：窗口平均带宽的线路
// 报的是「近 N 小时均值」，按额度降速的线路报的是「本周期用量顶到额度、之后限速多少」。
// 离线提醒对「添加后从没上报过」的节点同样会触发（按创建时间算 model.FirstReportGrace 宽限），那种情况说「已离线」
// 会让人以为机器掉线了，实际是 Agent 还没装好或连不上面板。
func offlineMessage(n model.NodeView) string {
	if n.LastSeen == 0 {
		return "节点添加后一直未接入（Agent 从未上报），请检查安装与面板地址"
	}
	return "节点已离线"
}

// 续费提醒使用精确的剩余 / 过期时长，并明确标注 UTC，避免短时过期仍显示今天到期。
func renewalMessage(expiry, now time.Time) string {
	message := "到期时间：" + expiry.UTC().Format("2006-01-02 15:04:05 UTC")
	switch {
	case expiry.After(now):
		return message + "\n剩余时间：" + model.HumanDuration(expiry.Sub(now))
	case expiry.Before(now):
		return message + "\n已过期：" + model.HumanDuration(now.Sub(expiry))
	default:
		return message + "\n当前状态：已到期"
	}
}

func throttleMessage(n model.NodeView) string {
	if n.Line.ThrottlesOnQuota() {
		return trafficDetails(n) + fmt.Sprintf("\n触发条件：本周期用量达到 100%% 额度\n商家声明的限速：%d Mbit/s\n说明：根据额度推断，实际限速需向商家确认", n.Line.ThrottleMbps)
	}
	return fmt.Sprintf("当前窗口平均带宽：%.1f Mbit/s\n统计窗口：%d 小时（实际有数据 %d 小时）\n触发阈值：%d Mbit/s\n说明：达到商家声明的降速条件，实际限速需确认", n.AvgBits/1e6, n.WindowHours, n.CoveredHours, n.Line.ThrottleMbps)
}

// evaluate reports whether one rule's condition holds for the node right now,
// and how to build the message an alert for it carries. The message is lazy:
// the engine evaluates every rule every tick, and almost no tick writes an event.
func evaluate(n model.NodeView, r model.AlertRule, now time.Time) (bool, func() string) {
	none := func() string { return "" }
	hold := func() string { return model.HumanDuration(time.Duration(r.HoldSeconds) * time.Second) }
	switch r.Kind {
	case model.AlertOffline:
		if n.Online {
			return false, none
		}
		// 从没上报过的节点按创建时间给足装 Agent 的时间，否则一条「全部节点」的离线规则
		// 会在每台新节点建好一两分钟后就报「一直未接入」。
		if n.LastSeen == 0 {
			return now.Unix()-n.CreatedAt > int64(model.FirstReportGrace.Seconds()), func() string {
				return offlineMessage(n) + "\n添加时间：" + eventTime(n.CreatedAt) + "\n允许首次接入：" + model.HumanDuration(model.FirstReportGrace) + "\n触发后等待：" + hold()
			}
		}
		return true, func() string {
			return offlineMessage(n) + "\n最后上报：" + eventTime(n.LastSeen) + "\n未上报时长：" + model.HumanDuration(now.Sub(time.Unix(n.LastSeen, 0))) + "\n离线判定：连续 " + model.HumanDuration(model.OfflineAfter) + " 未上报\n触发后等待：" + hold()
		}
	case model.AlertRenewal:
		if n.Billing.ExpiresAt == "" {
			return false, none
		}
		expiry, err := time.Parse(time.RFC3339, n.Billing.ExpiresAt)
		if err != nil {
			return false, none
		}
		return !now.Add(time.Duration(r.Threshold) * 24 * time.Hour).Before(expiry), func() string {
			return renewalDetails(n, now) + fmt.Sprintf("\n提醒窗口：到期前 %g 天", r.Threshold)
		}
	case model.AlertTraffic:
		limit := n.Billing.TrafficLimit
		return limit > 0 && float64(n.TrafficUsed)/float64(limit)*100 >= r.Threshold, func() string {
			return trafficDetails(n) + fmt.Sprintf("\n触发阈值：%g%%", r.Threshold)
		}
	case model.AlertThrottle:
		return n.Throttled, func() string { return throttleMessage(n) }
	case model.AlertCPU, model.AlertMemory:
		used, ok := usage(n, r.Kind)
		return ok && used >= r.Threshold, func() string {
			at := n.MetricsAt
			if at == 0 {
				at = n.LastSeen
			}
			return resourceDetails(n, r.Kind) + fmt.Sprintf("\n触发条件：%s ≥ %g%%，持续至少 %s\n指标采样：%s", usageLabels[r.Kind], r.Threshold, hold(), eventTime(at))
		}
	}
	return false, none
}

// eventText builds one node/kind's event text for Transition: the trigger or
// repeat snapshot when active, otherwise the recovery and its cause.
func eventText(n model.NodeView, r model.AlertRule, enabled bool, message func() string, now time.Time) func(active, repeat bool) string {
	return func(active, repeat bool) string {
		if !active {
			return recoveryMessage(n, r, enabled, now)
		}
		body := message() + ruleDetails(r)
		if repeat {
			body = "提醒状态：异常仍持续（重复提醒）\n" + body
		}
		return nodeContext(n, body)
	}
}

type Engine struct {
	Repository Repository
	Channels   Channels // nil: rules are evaluated, nothing is delivered
	Sender     Sender
}

func (e Engine) Tick(ctx context.Context, now time.Time) error {
	nodes, err := e.Repository.Nodes(ctx, now)
	if err != nil {
		return err
	}
	for _, n := range nodes {
		// 每种规则都要过一遍：没有这条规则（关掉了）就是条件不成立，正在触发的告警据此解除。
		for _, kind := range model.AlertKinds {
			rule, on := n.Alerts.Rule(kind)
			rule.Kind = kind
			condition, message := false, func() string { return "" }
			if on {
				condition, message = evaluate(n, rule, now)
			}
			if err = e.Repository.Transition(ctx, n.Node, kind, eventText(n, rule, on, message, now), condition, rule.HoldSeconds, rule.RepeatSeconds, now); err != nil {
				return err
			}
		}
	}
	if e.Channels == nil {
		return nil
	}
	channel, err := e.Channels.NotificationConfig(ctx)
	if err != nil || channel.Channel == "none" {
		return err
	}
	pending, err := e.Repository.PendingEvents(ctx, now)
	if err != nil {
		return err
	}
	// Bound work per tick; a failing destination cannot monopolize the scheduler.
	for _, event := range pending[:min(len(pending), 5)] {
		err = e.Sender.Send(ctx, channel, event)
		if err != nil {
			slog.Warn("notification delivery failed", "event", event.ID)
		}
		if err != nil {
			event.LastError = err.Error()
		} else {
			event.LastError = ""
		}
		if saveErr := e.Repository.Delivery(ctx, event, err == nil, now); saveErr != nil {
			return saveErr
		}
	}
	return nil
}
func (e Engine) Run(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		if err := e.Tick(ctx, time.Now()); err != nil && ctx.Err() == nil {
			slog.Error("alert evaluation failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func ValidateEndpoint(endpoint string) error {
	if endpoint == "" {
		return nil
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") || len(endpoint) > 2048 {
		return errors.New("Webhook 须为有效的 HTTP(S) 地址")
	}
	return nil
}

type Webhook struct{}

func (Webhook) Send(ctx context.Context, endpoint string, e model.Event) error {
	if err := ValidateEndpoint(endpoint); err != nil {
		return err
	}
	data, err := json.Marshal(struct {
		Version int         `json:"version"`
		Event   model.Event `json:"event"`
	}{model.Protocol, e})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "TZA-Probe/"+model.Version)
	client := http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("webhook connection failed")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned HTTP %d", resp.StatusCode)
	}
	return nil
}
