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
	Transition(context.Context, model.Node, string, string, bool, int, time.Time) error
	Events(context.Context, bool, time.Time) ([]model.Event, error)
	Delivery(context.Context, model.Event, bool, time.Time) error
	Setting(context.Context, string) (string, error)
}
type Sender interface {
	Send(context.Context, string, model.Event) error
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
		a := n.Alerts
		load := false
		if n.Online && n.Metrics != nil {
			load = n.Metrics.CPU >= a.CPUPercent || float64(n.Metrics.MemoryUsed)/float64(n.Metrics.MemoryTotal)*100 >= a.MemoryPercent
		}
		renewal := false
		if n.ExpiresAt != "" {
			expiry, _ := time.Parse(time.RFC3339, n.ExpiresAt)
			renewal = !now.Add(time.Duration(a.RenewalDays) * 24 * time.Hour).Before(expiry)
		}
		rules := []struct {
			kind, message string
			condition     bool
			hold          int
		}{
			{"offline", "节点已离线", a.Offline && !n.Online && now.Unix()-n.CreatedAt > int64(model.OfflineAfter.Seconds()), a.OfflineSeconds},
			{"renewal", "节点即将到期或已经到期，请检查续费", a.Renewal && renewal, 0},
			{"traffic", fmt.Sprintf("本计费周期用量达到 %.0f%% 阈值", a.TrafficPercent), a.Traffic && n.TrafficLimit > 0 && float64(n.TrafficUsed)/float64(n.TrafficLimit)*100 >= a.TrafficPercent, 0},
			{"throttle", fmt.Sprintf("近 %d 小时平均带宽 %.1f Mbit/s 达到降速阈值 %d Mbit/s，可能已被商家降速", n.CoveredHours, n.AvgBits/1e6, n.ThrottleMbps), a.Throttle && n.Throttled, 0},
			{"load", "CPU 或内存持续超过设定阈值", a.Load && load, a.HoldSeconds},
		}
		for _, r := range rules {
			if err = e.Repository.Transition(ctx, n.Node, r.kind, r.message, r.condition, r.hold, now); err != nil {
				return err
			}
		}
	}
	endpoint, err := Destination(ctx, e.Repository)
	if err != nil || endpoint == "" {
		return err
	}
	pending, err := e.Repository.Events(ctx, true, now)
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
