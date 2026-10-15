package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/notify"
	"github.com/leeska/TZA-Probe/pkg/tzapkg"
)

// notificationKey is the settings row holding the channel as JSON. Before it
// existed a bare Webhook URL lived under legacyWebhookKey; that row is still
// read when the JSON one is missing.
const (
	notificationKey  = "notification"
	legacyWebhookKey = "webhook"
)

// loadNotification reads the stored channel. Nothing stored is channel "none";
// a stored configuration that no longer validates is an error rather than a
// silently disabled channel.
func (s *Service) loadNotification(ctx context.Context) (notify.Config, error) {
	value, err := s.Store.Setting(ctx, notificationKey)
	if err != nil {
		return notify.Config{}, err
	}
	if value == "" {
		hook, err := s.Store.Setting(ctx, legacyWebhookKey)
		if err != nil || hook == "" {
			return notify.Config{Channel: "none"}, err
		}
		value = encodeConfig(notify.Config{Channel: "webhook", Webhook: hook})
	}
	var c notify.Config
	if err = json.Unmarshal([]byte(value), &c); err != nil {
		return notify.Config{}, errors.New("invalid notification configuration")
	}
	if c.Channel == "none" {
		return notify.Config{Channel: "none"}, nil
	}
	if err = c.Validate(); err != nil {
		return notify.Config{}, err
	}
	return c, nil
}

func encodeConfig(c notify.Config) string {
	data, _ := json.Marshal(c)
	return string(data)
}

func (s *Service) saveNotification(ctx context.Context, c notify.Config) error {
	return s.Store.SetSetting(ctx, notificationKey, encodeConfig(c))
}

// normalizeNotification applies the rules both the panel and the MCP endpoint
// share: an empty channel means "no webhook configured" and therefore none, a
// blank Telegram token keeps the stored one (the form never echoes it), and the
// credentials of the unselected channel are dropped. A model.Invalid error means
// the submitted configuration is wrong; any other error is storage.
func (s *Service) normalizeNotification(ctx context.Context, next notify.Config) (notify.Config, error) {
	if next.Channel == "" {
		next.Channel = "webhook"
		if next.Webhook == "" {
			next.Channel = "none"
		}
	}
	if next.Channel == "telegram" && next.BotToken == "" {
		old, err := s.loadNotification(ctx)
		if err != nil {
			return notify.Config{}, err
		}
		next.BotToken = old.BotToken
	}
	if err := next.Validate(); err != nil {
		return notify.Config{}, model.Invalid(err.Error())
	}
	if next.Channel == "plugin" {
		settings, err := s.pluginSettings(ctx, next.Plugin, next.PluginSettings)
		if err != nil {
			return notify.Config{}, err
		}
		next.PluginSettings = settings
	} else {
		next.Plugin, next.PluginSettings = "", nil
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

// pluginSettings checks the settings of a plugin channel against the
// installed plugin. A blank secret keeps the stored value when the same
// plugin is already the channel (the form never echoes secrets).
func (s *Service) pluginSettings(ctx context.Context, id string, values map[string]string) (map[string]string, error) {
	p, err := s.notifyPlugin(id)
	if err != nil {
		return nil, err
	}
	merged := map[string]string{}
	for k, v := range values {
		merged[k] = v
	}
	if old, err := s.loadNotification(ctx); err == nil && old.Channel == "plugin" && old.Plugin == id {
		for _, key := range p.SecretKeys() {
			if strings.TrimSpace(merged[key]) == "" && old.PluginSettings[key] != "" {
				merged[key] = old.PluginSettings[key]
			}
		}
	}
	settings, err := p.CheckSettings(merged)
	if err != nil {
		return nil, model.Invalid(p.Name + "：" + err.Error())
	}
	return settings, nil
}

// notifyPlugin resolves an installed notify plugin, as a model error.
func (s *Service) notifyPlugin(id string) (tzapkg.Plugin, error) {
	if s.Plugins == nil {
		return tzapkg.Plugin{}, model.Invalid("这个 Server 没有启用插件")
	}
	p, err := s.Plugins.Plugin(id)
	if err != nil {
		return tzapkg.Plugin{}, model.Invalid(fmt.Sprintf("通知插件 %s 不可用：%v", id, err))
	}
	if p.Kind != tzapkg.KindNotify {
		return tzapkg.Plugin{}, model.Invalid(fmt.Sprintf("插件 %s 不是通知插件", id))
	}
	return p, nil
}

// PublicNotification is the credential-free view of a channel: Config.Public
// plus, for a plugin channel, the plugin's name, the values of its non-secret
// settings and which secrets are stored (never their values). A plugin that
// disappeared or no longer parses is reported in plugin_error, so the panel
// shows why notifications stopped instead of a silently empty channel.
func (s *Service) PublicNotification(c notify.Config) map[string]any {
	view := c.Public()
	if c.Channel != "plugin" {
		return view
	}
	view["plugin_name"] = c.Plugin
	view["plugin_settings"] = map[string]string{}
	view["plugin_secrets"] = map[string]bool{}
	p, err := s.notifyPlugin(c.Plugin)
	if err != nil {
		view["plugin_error"] = err.Error()
		return view
	}
	secret := map[string]bool{}
	for _, key := range p.SecretKeys() {
		secret[key] = true
	}
	values, stored := map[string]string{}, map[string]bool{}
	for k, v := range c.PluginSettings {
		if secret[k] {
			stored[k] = v != ""
		} else {
			values[k] = v
		}
	}
	view["plugin_name"], view["plugin_settings"], view["plugin_secrets"] = p.Name, values, stored
	if _, err := p.CheckSettings(c.PluginSettings); err != nil {
		view["plugin_error"] = "设置不再符合插件要求：" + err.Error()
	}
	return view
}

// NotificationConfig is the notify.Channels port: the Engine delivers through
// whatever the panel last saved.
func (s *Service) NotificationConfig(ctx context.Context) (notify.Config, error) {
	return s.loadNotification(ctx)
}

// Notification is the stored channel configuration; callers show only
// Config.Public(), never the credentials.
func (s *Service) Notification(ctx context.Context) (notify.Config, error) {
	return s.loadNotification(ctx)
}

// ReplaceNotification is the panel form: the whole configuration is
// submitted, an empty field clears it (except a blank Telegram token, which
// the form never echoes and therefore keeps).
func (s *Service) ReplaceNotification(ctx context.Context, next notify.Config) (notify.Config, error) {
	normalized, err := s.normalizeNotification(ctx, next)
	if err != nil {
		return notify.Config{}, err
	}
	return normalized, s.saveNotification(ctx, normalized)
}

// resolveNotificationPatch turns a partial change into a full configuration:
// no channel keeps the current one (or, when notifications are off, the one
// whose credentials were given), and blank credentials keep the stored ones.
func (s *Service) resolveNotificationPatch(ctx context.Context, patch notify.Config) (notify.Config, error) {
	current, err := s.Notification(ctx)
	if err != nil {
		return notify.Config{}, err
	}
	if patch.Channel == "" {
		patch.Channel = current.Channel
		if patch.Plugin != "" && patch.Plugin != current.Plugin {
			patch.Channel = "plugin"
		}
		if patch.Channel == "none" || patch.Channel == "" {
			patch.Channel = "webhook"
			switch {
			case patch.BotToken != "" || patch.ChatID != "":
				patch.Channel = "telegram"
			case patch.Plugin != "":
				patch.Channel = "plugin"
			}
		}
	}
	if patch.Channel == "telegram" && patch.ChatID == "" && current.Channel == "telegram" {
		patch.ChatID = current.ChatID
	}
	if patch.Channel == "webhook" && patch.Webhook == "" && current.Channel == "webhook" {
		patch.Webhook = current.Webhook
	}
	if patch.Channel == "plugin" && current.Channel == "plugin" {
		if patch.Plugin == "" {
			patch.Plugin = current.Plugin
		}
		// 只改传了的设置：没传或留空的沿用当前值（同一个插件时）。
		if patch.Plugin == current.Plugin {
			merged := map[string]string{}
			for k, v := range current.PluginSettings {
				merged[k] = v
			}
			for k, v := range patch.PluginSettings {
				if strings.TrimSpace(v) != "" {
					merged[k] = v
				}
			}
			patch.PluginSettings = merged
		}
	}
	return s.normalizeNotification(ctx, patch)
}

// TestNotification sends a test message through the configured channel and
// returns the channel it used.
func (s *Service) TestNotification(ctx context.Context) (string, error) {
	channel, err := s.loadNotification(ctx)
	if err != nil {
		return "", err
	}
	if channel.Channel == "none" {
		return "", model.Invalid("请先配置通知渠道")
	}
	if s.Notifier == nil {
		return "", ErrNoSender
	}
	if err = s.Notifier.Send(ctx, channel, model.Event{Kind: "test", Message: "这是一条通知连接测试消息，用于验证当前通知渠道。", At: time.Now().Unix()}); err != nil {
		return "", SendError{err}
	}
	return channel.Channel, nil
}

// PatchNotification changes only the fields given — the rule MCP's
// update_settings uses — so a script can switch the channel or rotate one
// credential without resubmitting the rest. Closing notifications takes an
// explicit channel "none".
func (s *Service) PatchNotification(ctx context.Context, patch notify.Config) (notify.Config, error) {
	if patch.IsZero() {
		return notify.Config{}, model.Invalid("至少给一项通知设置；关闭通知请传 channel=none")
	}
	config, err := s.resolveNotificationPatch(ctx, patch)
	if err != nil {
		return notify.Config{}, err
	}
	return config, s.saveNotification(ctx, config)
}
