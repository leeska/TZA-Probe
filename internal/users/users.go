// Package users is the 使用者 module: the people (the administrator's
// friends) who get proxy access. Each has a traffic quota that can reset
// monthly, an expiry date, an on/off switch and a secret subscription token;
// the token also opens a read-only usage page (/u/{token}). Proxy inbounds,
// traffic accounting and subscription output build on this in later steps.
package users

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

type User struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Note    string `json:"note"`
	Enabled bool   `json:"enabled"`
	// TrafficLimit is the allowance per period in bytes; 0 is unlimited.
	TrafficLimit int64 `json:"traffic_limit"`
	// TrafficUsed is counted since PeriodStart (filled by traffic accounting).
	TrafficUsed int64 `json:"traffic_used"`
	// ResetDay is the day of the month (1–28) the usage starts over; 0 never.
	ResetDay    int    `json:"reset_day"`
	PeriodStart int64  `json:"period_start"`
	ExpiresAt   string `json:"expires_at"` // RFC 3339; empty never expires
	Token       string `json:"token"`
	CreatedAt   int64  `json:"created_at"`
}

// Status words, in the order they win: a disabled user is disabled even when
// also expired.
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
	StatusExpired  = "expired"
	StatusExceeded = "exceeded"
)

// Status is what the user's access is right now.
func (u User) Status(now time.Time) string {
	if !u.Enabled {
		return StatusDisabled
	}
	if u.ExpiresAt != "" {
		if t, err := time.Parse(time.RFC3339, u.ExpiresAt); err == nil && !now.Before(t) {
			return StatusExpired
		}
	}
	if u.TrafficLimit > 0 && u.TrafficUsed >= u.TrafficLimit {
		return StatusExceeded
	}
	return StatusActive
}

// NextReset is when the current period ends (0 when usage never resets):
// the next ResetDay at 00:00 UTC after PeriodStart.
func (u User) NextReset() int64 {
	if u.ResetDay == 0 {
		return 0
	}
	start := time.Unix(u.PeriodStart, 0).UTC()
	next := time.Date(start.Year(), start.Month(), u.ResetDay, 0, 0, 0, 0, time.UTC)
	if !next.After(start) {
		next = next.AddDate(0, 1, 0)
	}
	return next.Unix()
}

const (
	MaxName = 40
	MaxNote = 200
)

// Validate checks what the administrator submits.
func (u User) Validate() error {
	name := strings.TrimSpace(u.Name)
	if name == "" || utf8.RuneCountInString(name) > MaxName {
		return fmt.Errorf("名称需要 1–%d 个字", MaxName)
	}
	if utf8.RuneCountInString(u.Note) > MaxNote {
		return fmt.Errorf("备注最多 %d 个字", MaxNote)
	}
	if u.TrafficLimit < 0 {
		return errors.New("流量额度不能为负")
	}
	if u.ResetDay < 0 || u.ResetDay > 28 {
		return errors.New("重置日为 1–28，0 表示不重置")
	}
	if u.ExpiresAt != "" {
		if _, err := time.Parse(time.RFC3339, u.ExpiresAt); err != nil {
			return errors.New("到期时间格式无效")
		}
	}
	return nil
}

// NewToken is a subscription token: 128 random bits, hex.
func NewToken() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(raw[:])
}

func newID() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(raw[:])
}
