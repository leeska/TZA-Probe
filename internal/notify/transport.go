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

type Transport struct{ Telegram Telegram }

func (s Transport) Send(ctx context.Context, c Config, event model.Event) error {
	if err := c.Validate(); err != nil {
		return err
	}
	switch c.Channel {
	case "telegram":
		return s.Telegram.Send(ctx, c, event)
	case "webhook":
		return (Webhook{}).Send(ctx, c.Webhook, event)
	default:
		return errors.New("通知渠道未启用")
	}
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
	state := "已恢复"
	if e.Active {
		state = "告警触发"
	}
	if e.Kind == "test" {
		state = "连接测试"
	}
	names := map[string]string{"offline": "节点离线", "renewal": "到期续费", "traffic": "流量额度", "throttle": "达量降速", "load": "系统负载", "test": "测试通知"}
	kind := names[e.Kind]
	if kind == "" {
		kind = e.Kind
	}
	message := fmt.Sprintf("TZA Probe · %s\n%s\n节点：%s\n%s\n时间：%s\n事件：%d", state, kind, e.NodeName, e.Message, time.Unix(e.At, 0).UTC().Format(time.RFC3339), e.ID)
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
