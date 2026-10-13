package notify

import (
	"errors"
	"regexp"
)

// Config is the one notification channel in use. Where it is stored is not
// this package's business: the Engine is handed it through Channels, and the
// service owns reading, normalising and writing it.
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
