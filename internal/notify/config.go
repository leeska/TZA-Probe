package notify

import (
	"errors"
	"regexp"

	"github.com/leeska/TZA-Probe/pkg/tzapkg"
)

// Config is the one notification channel in use. Where it is stored is not
// this package's business: the Engine is handed it through Channels, and the
// service owns reading, normalising and writing it.
//
// Channel "plugin" sends through an installed notify plugin: Plugin is its id
// and PluginSettings the values for its settings form (secrets included; the
// service never shows those back). Whether the settings still fit the plugin
// is checked when saving and again when sending, against the installed plugin.
type Config struct {
	Channel        string            `json:"channel"`
	Webhook        string            `json:"webhook,omitempty"`
	BotToken       string            `json:"bot_token,omitempty"`
	ChatID         string            `json:"chat_id,omitempty"`
	Plugin         string            `json:"plugin,omitempty"`
	PluginSettings map[string]string `json:"plugin_settings,omitempty"`
}

// IsZero reports whether nothing at all was given (a PATCH with no
// notification fields).
func (c Config) IsZero() bool {
	return c.Channel == "" && c.Webhook == "" && c.BotToken == "" && c.ChatID == "" && c.Plugin == "" && len(c.PluginSettings) == 0
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
	case "plugin":
		if !tzapkg.ValidID(c.Plugin) {
			return errors.New("请选择通知插件")
		}
		return nil
	default:
		return errors.New("请选择有效的通知渠道")
	}
}

// Public is the credential-free view of a configuration for the panel and for
// the MCP tools. A plugin channel shows only its id here; which of its
// settings may be shown takes the installed plugin (service.PublicNotification).
func (c Config) Public() map[string]any {
	out := map[string]any{
		"webhook_configured":  c.Channel == "webhook",
		"telegram_configured": c.Channel == "telegram",
		"plugin_configured":   c.Channel == "plugin",
		"channel":             c.Channel,
		"chat_id":             c.ChatID,
	}
	if c.Channel == "plugin" {
		out["plugin"] = c.Plugin
	}
	return out
}
