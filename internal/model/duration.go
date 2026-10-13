package model

import (
	"fmt"
	"strings"
	"time"
)

// HumanDuration renders alert intervals without rounding an expiry across now.
func HumanDuration(d time.Duration) string {
	seconds := max(int64(d/time.Second), 0)
	parts := []string{}
	for _, unit := range []struct {
		seconds int64
		label   string
	}{{86400, "天"}, {3600, "小时"}, {60, "分钟"}, {1, "秒"}} {
		if value := seconds / unit.seconds; value > 0 {
			parts = append(parts, fmt.Sprintf("%d%s", value, unit.label))
			seconds %= unit.seconds
		}
	}
	if len(parts) == 0 {
		return "0秒"
	}
	return strings.Join(parts, "")
}
