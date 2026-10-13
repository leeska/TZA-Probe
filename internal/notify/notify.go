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
	"math"
	"net/http"
	"net/url"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

type Repository interface {
	Nodes(context.Context, time.Time) ([]model.NodeView, error)
	Transition(context.Context, model.Node, string, string, bool, int, int, time.Time) error
	PendingEvents(context.Context, time.Time) ([]model.Event, error)
	Delivery(context.Context, model.Event, bool, time.Time) error
	Setting(context.Context, string) (string, error)
}
type Sender interface {
	Send(context.Context, string, model.Event) error
}

// throttleMessage 是降速告警文案。两种规则读的不是同一个事实：窗口平均带宽的线路
// 报的是「近 N 小时均值」，按额度降速的线路报的是「本周期用量顶到额度、之后限速多少」。
// 离线提醒对「添加后从没上报过」的节点同样会触发（按创建时间算宽限），那种情况说「已离线」
// 会让人以为机器掉线了，实际是 Agent 还没装好或连不上面板。
func offlineMessage(n model.NodeView) string {
	if n.LastSeen == 0 {
		return "节点添加后一直未接入（Agent 从未上报），请检查安装与面板地址"
	}
	return "节点已离线"
}

// 续费提醒写清楚还剩几天、哪天到期（按 UTC 日期，与后台填写口径一致），而不是一句笼统的
// 「即将到期或已经到期」——这句话会原样发到 Telegram / Webhook。天数取整方式与公开面板的
// 到期胶囊一致（向上取整）。
func renewalMessage(expiry, now time.Time) string {
	days := int(math.Ceil(expiry.Sub(now).Hours() / 24))
	// 带上时分：天数是向上取整的（与面板一致），只写日期时「3 天后到期（10-12）」会和本地日历对不上。
	date := expiry.UTC().Format("2006-01-02 15:04")
	switch {
	case days > 0:
		return fmt.Sprintf("节点将在 %d 天后到期（%s UTC），请检查续费", days, date)
	case days == 0:
		return fmt.Sprintf("节点今天到期（%s UTC），请检查续费", date)
	default:
		return fmt.Sprintf("节点已过期 %d 天（%s UTC 到期），请检查续费", -days, date)
	}
}

func throttleMessage(n model.NodeView) string {
	if n.Line.ThrottlesOnQuota() {
		return fmt.Sprintf("本周期用量已达 %s 额度，线路按商家规则降速到 %d Mbit/s", model.HumanBytes(n.Billing.TrafficLimit), n.Line.ThrottleMbps)
	}
	return fmt.Sprintf("近 %d 小时平均带宽 %.1f Mbit/s 达到降速阈值 %d Mbit/s，可能已被商家降速", n.CoveredHours, n.AvgBits/1e6, n.Line.ThrottleMbps)
}

// evaluate reports whether one rule's condition holds for the node right now,
// and the message an alert for it carries.
func evaluate(n model.NodeView, r model.AlertRule, now time.Time) (bool, string) {
	switch r.Kind {
	case model.AlertOffline:
		return !n.Online && now.Unix()-n.CreatedAt > int64(model.OfflineAfter.Seconds()), offlineMessage(n)
	case model.AlertRenewal:
		if n.Billing.ExpiresAt == "" {
			return false, ""
		}
		expiry, err := time.Parse(time.RFC3339, n.Billing.ExpiresAt)
		if err != nil {
			return false, ""
		}
		return !now.Add(time.Duration(r.Threshold) * 24 * time.Hour).Before(expiry), renewalMessage(expiry, now)
	case model.AlertTraffic:
		limit := n.Billing.TrafficLimit
		return limit > 0 && float64(n.TrafficUsed)/float64(limit)*100 >= r.Threshold, fmt.Sprintf("本计费周期用量达到 %.0f%% 阈值", r.Threshold)
	case model.AlertThrottle:
		return n.Throttled, throttleMessage(n)
	case model.AlertCPU:
		return n.Online && n.Metrics != nil && n.Metrics.CPU >= r.Threshold, fmt.Sprintf("CPU 使用率持续 %d 秒超过 %.0f%%", r.HoldSeconds, r.Threshold)
	case model.AlertMemory:
		if !n.Online || n.Metrics == nil || n.Metrics.MemoryTotal == 0 {
			return false, ""
		}
		used := float64(n.Metrics.MemoryUsed) / float64(n.Metrics.MemoryTotal) * 100
		return used >= r.Threshold, fmt.Sprintf("内存使用率持续 %d 秒超过 %.0f%%", r.HoldSeconds, r.Threshold)
	}
	return false, ""
}

type Engine struct {
	Repository Repository
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
			condition, message := false, ""
			if on {
				condition, message = evaluate(n, rule, now)
			}
			if err = e.Repository.Transition(ctx, n.Node, kind, message, condition, rule.HoldSeconds, rule.RepeatSeconds, now); err != nil {
				return err
			}
		}
	}
	endpoint, err := Destination(ctx, e.Repository)
	if err != nil || endpoint == "" {
		return err
	}
	pending, err := e.Repository.PendingEvents(ctx, now)
	if err != nil {
		return err
	}
	// Bound work per tick; a failing destination cannot monopolize the scheduler.
	for _, event := range pending[:min(len(pending), 5)] {
		err = e.Sender.Send(ctx, endpoint, event)
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
