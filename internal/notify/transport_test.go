package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/leeska/TZA-Probe/internal/model"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestTelegramTransport(t *testing.T) {
	c := Config{Channel: "telegram", BotToken: "123456:abcdefghijklmnopqrstuvwxyz", ChatID: "-100123456"}
	for _, active := range []bool{true, false} {
		sender := Telegram{RoundTripper: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Host != "api.telegram.org" || r.Method != "POST" || r.URL.Scheme != "https" {
				t.Fatal("invalid destination")
			}
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload["chat_id"] != c.ChatID || payload["parse_mode"] != nil {
				t.Fatal(payload)
			}
			text := payload["text"].(string)
			state := "已恢复"
			if active {
				state = "告警触发"
			}
			if !strings.Contains(text, state) || !strings.Contains(text, "<node>") {
				t.Fatal(text)
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true}`)), Header: make(http.Header)}, nil
		})}
		if err := sender.Send(context.Background(), c, model.Event{Kind: "load", NodeName: "<node>", Active: active}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		status int
		body   string
	}{{200, `{"ok":false}`}, {429, `{"ok":false}`}, {302, ""}, {200, "invalid"}} {
		sender := Telegram{RoundTripper: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
		})}
		if err := sender.Send(context.Background(), c, model.Event{}); err == nil || strings.Contains(err.Error(), c.BotToken) {
			t.Fatalf("unsafe or absent error: %v", err)
		}
	}
	sender := Telegram{RoundTripper: roundTripFunc(func(r *http.Request) (*http.Response, error) { return nil, errors.New(r.URL.String()) })}
	if err := sender.Send(context.Background(), c, model.Event{}); err == nil || strings.Contains(err.Error(), c.BotToken) {
		t.Fatal("token leaked")
	}
}
func TestTelegramConfigValidation(t *testing.T) {
	for _, c := range []Config{
		{Channel: "telegram", BotToken: "bad", ChatID: "123"},
		{Channel: "telegram", BotToken: "123:abcdefghijklmnopqrstuvwxyz", ChatID: ""},
		{Channel: "telegram", BotToken: "123:abcdefghijklmnopqrstuvwxyz/path", ChatID: "123"},
	} {
		if c.Validate() == nil {
			t.Fatal("invalid credentials accepted")
		}
	}
}
