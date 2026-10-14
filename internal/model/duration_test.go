package model

import (
	"testing"
	"time"
)

func TestHumanDuration(t *testing.T) {
	for _, c := range []struct {
		d    time.Duration
		want string
	}{
		{0, "0秒"},
		{-time.Minute, "0秒"},
		{500 * time.Millisecond, "不足1秒"},
		{45 * time.Second, "45秒"},
		{330 * time.Second, "5分钟30秒"},
		{30 * time.Hour, "1天6小时"},
		{24*time.Hour + 30*time.Minute, "1天"},
		{400*24*time.Hour + 3*time.Hour + 12*time.Minute + 5*time.Second, "400天3小时"},
	} {
		if got := HumanDuration(c.d); got != c.want {
			t.Errorf("HumanDuration(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}
