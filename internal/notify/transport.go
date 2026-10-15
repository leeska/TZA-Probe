package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

// Transport delivers to the configured channel. Plugins resolves a "plugin"
// channel (nil: plugin channels fail with a clear error); RoundTripper is the
// HTTP transport for plugin requests (tests; nil is the default).
type Transport struct {
	Telegram     Telegram
	Plugins      Plugins
	RoundTripper http.RoundTripper
}

func (s Transport) Send(ctx context.Context, c Config, event model.Event) error {
	if err := c.Validate(); err != nil {
		return err
	}
	switch c.Channel {
	case "telegram":
		return s.Telegram.Send(ctx, c, event)
	case "webhook":
		return (Webhook{}).Send(ctx, c.Webhook, event)
	case "plugin":
		return s.sendPlugin(ctx, c, event)
	default:
		return errors.New("通知渠道未启用")
	}
}

// telegramTitles names each event kind on the second line of a Telegram
// message. Every model.AlertKinds entry needs one (a test holds them together),
// otherwise the message falls back to the raw kind; load is the retired
// combined rule, kept for its events still in the log. Test messages carry no
// kind line, so they need no title.
var telegramTitles = map[string]string{
	model.AlertOffline: "节点离线", model.AlertRenewal: "到期续费", model.AlertTraffic: "流量额度",
	model.AlertThrottle: "达量降速", model.AlertCPU: "CPU 使用率", model.AlertMemory: "内存使用率",
	"load": "系统负载",
}

type Telegram struct {
	// HTTP transport is injectable for offline contract tests; the destination is fixed.
	RoundTripper http.RoundTripper
}

func (s Telegram) Send(ctx context.Context, c Config, e model.Event) error {
	if c.Channel != "telegram" {
		return errors.New("invalid Telegram configuration")
	}
	if err := c.Validate(); err != nil {
		return err
	}
	message := telegramMessage(e)
	payload, _ := json.Marshal(map[string]any{"chat_id": c.ChatID, "text": message, "link_preview_options": map[string]bool{"is_disabled": true}})
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.telegram.org/bot"+c.BotToken+"/sendMessage", bytes.NewReader(payload))
	if err != nil {
		return errors.New("Telegram 请求创建失败")
	}
	req.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 5 * time.Second, Transport: s.RoundTripper, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("Telegram 连接失败，请检查服务器网络")
	}
	defer resp.Body.Close()
	var result struct {
		OK bool `json:"ok"`
	}
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&result)
	// Never expose response bodies or request URLs containing the bot token.
	if resp.StatusCode != http.StatusOK || decodeErr != nil || !result.OK {
		return fmt.Errorf("Telegram 投递失败（HTTP %d），请检查 Token、Chat ID 和机器人权限", resp.StatusCode)
	}
	return nil
}
