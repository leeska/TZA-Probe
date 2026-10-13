package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/notify"
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
	if next.Channel != "telegram" {
		next.BotToken = ""
		next.ChatID = ""
	}
	if next.Channel != "webhook" {
		next.Webhook = ""
	}
	return next, nil
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
		if patch.Channel == "none" || patch.Channel == "" {
			patch.Channel = "webhook"
			if patch.BotToken != "" || patch.ChatID != "" {
				patch.Channel = "telegram"
			}
		}
	}
	if patch.Channel == "telegram" && patch.ChatID == "" && current.Channel == "telegram" {
		patch.ChatID = current.ChatID
	}
	if patch.Channel == "webhook" && patch.Webhook == "" && current.Channel == "webhook" {
		patch.Webhook = current.Webhook
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
	if err = s.Notifier.Send(ctx, channel, model.Event{Kind: "test", Message: "TZA Probe 通知测试", At: time.Now().Unix()}); err != nil {
		return "", SendError{err}
	}
	return channel.Channel, nil
}

// PatchNotification changes only the fields given — the rule MCP's
// update_settings uses — so a script can switch the channel or rotate one
// credential without resubmitting the rest. Closing notifications takes an
// explicit channel "none".
func (s *Service) PatchNotification(ctx context.Context, patch notify.Config) (notify.Config, error) {
	if patch == (notify.Config{}) {
		return notify.Config{}, model.Invalid("至少给一项通知设置；关闭通知请传 channel=none")
	}
	config, err := s.resolveNotificationPatch(ctx, patch)
	if err != nil {
		return notify.Config{}, err
	}
	return config, s.saveNotification(ctx, config)
}
