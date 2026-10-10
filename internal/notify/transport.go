package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

// Config is stored atomically; rules and retry state remain transport-independent.
type Config struct {
	Channel  string `json:"channel"`
	Webhook  string `json:"webhook,omitempty"`
	BotToken string `json:"bot_token,omitempty"`
	ChatID   string `json:"chat_id,omitempty"`
}

var tokenPattern = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]{20,200}$`)
var chatPattern = regexp.MustCompile(`^(-?[0-9]+|@[A-Za-z][A-Za-z0-9_]{4,63})$`)

func (c Config) Validate() error {
	switch c.Channel {
	case "none":
		return nil
	case "webhook":
		if c.Webhook == "" {
			return errors.New("请填写 Webhook 地址")
		}
		return ValidateEndpoint(c.Webhook)
	case "telegram":
		if !tokenPattern.MatchString(c.BotToken) {
			return errors.New("Bot Token 格式无效")
		}
		if !chatPattern.MatchString(c.ChatID) {
			return errors.New("Chat ID 须为数字 ID 或 @频道名")
		}
		return nil
	default:
		return errors.New("请选择有效的通知渠道")
	}
}

type Settings interface {
	Setting(context.Context, string) (string, error)
}

func Destination(ctx context.Context, r Settings) (string, error) {
	value, err := r.Setting(ctx, "notification")
	if err != nil {
		return "", err
	}
	if value == "" {
		return r.Setting(ctx, "webhook")
	}
	var c Config
	if err = json.Unmarshal([]byte(value), &c); err != nil {
		return "", errors.New("invalid notification configuration")
	}
	if c.Channel == "none" {
		return "", nil
	}
	if err = c.Validate(); err != nil {
		return "", err
	}
	return value, nil
}
func ReadConfig(value string) Config {
	if strings.HasPrefix(value, "{") {
		var c Config
		_ = json.Unmarshal([]byte(value), &c)
		return c
	}
	if value != "" {
		return Config{Channel: "webhook", Webhook: value}
	}
	return Config{Channel: "none"}
}

// Store is the persistence port a notification configuration needs.
type Store interface {
	Settings
	SetSetting(context.Context, string, string) error
}

// Status reports the configured channel without exposing any credential.
func Status(ctx context.Context, store Settings) (Config, error) {
	value, err := Destination(ctx, store)
	if err != nil {
		return Config{}, err
	}
	return ReadConfig(value), nil
}

// Normalize applies the rules both the panel and the MCP endpoint must share: an
// empty channel means "no webhook configured" and therefore none, a blank
// Telegram token keeps the stored one (the form never echoes it), and the
// credentials of the unselected channel are dropped. An error here means the
// submitted configuration is invalid — the caller must fix its input — and never
// that storage failed.
func Normalize(ctx context.Context, store Settings, next Config) (Config, error) {
	if next.Channel == "" {
		next.Channel = "webhook"
		if next.Webhook == "" {
			next.Channel = "none"
		}
	}
	if next.Channel == "telegram" && next.BotToken == "" {
		old, err := Destination(ctx, store)
		if err != nil {
			return Config{}, err
		}
		next.BotToken = ReadConfig(old).BotToken
	}
	if err := next.Validate(); err != nil {
		return Config{}, err
	}
	if next.Channel != "telegram" {
		next.BotToken = ""
		next.ChatID = ""
	}
	if next.Channel != "webhook" {
		next.Webhook = ""
	}
	return next, nil
}

// Save normalises and persists a submitted configuration in one step.
func Save(ctx context.Context, store Store, next Config) error {
	normalized, err := Normalize(ctx, store, next)
	if err != nil {
		return err
	}
	return Persist(ctx, store, normalized)
}

// Persist writes an already normalised configuration.
func Persist(ctx context.Context, store Store, c Config) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return store.SetSetting(ctx, "notification", string(data))
}

// Public is the credential-free view of a configuration for the panel and for
// the MCP tools.
func (c Config) Public() map[string]any {
	return map[string]any{
		"webhook_configured":  c.Channel == "webhook",
		"telegram_configured": c.Channel == "telegram",
		"channel":             c.Channel,
		"chat_id":             c.ChatID,
	}
}

type Transport struct{ Telegram Telegram }

func (s Transport) Send(ctx context.Context, destination string, event model.Event) error {
	c := ReadConfig(destination)
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
