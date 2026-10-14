package model

import (
	"fmt"
	"time"
)

// HumanDuration renders alert intervals without rounding an expiry across now:
// it truncates, keeping the largest unit and the one right below it
// (2天12小时, 5分钟30秒), so a renewal months away does not read down to the
// second. A positive duration under a second is 不足1秒, not 0秒.
func HumanDuration(d time.Duration) string {
	if d > 0 && d < time.Second {
		return "不足1秒"
	}
	seconds := max(int64(d/time.Second), 0)
	units := []struct {
		seconds int64
		label   string
	}{{86400, "天"}, {3600, "小时"}, {60, "分钟"}, {1, "秒"}}
	for i, unit := range units {
		value := seconds / unit.seconds
		if value == 0 {
			continue
		}
		out := fmt.Sprintf("%d%s", value, unit.label)
		if i+1 < len(units) {
			next := units[i+1]
			if rest := seconds % unit.seconds / next.seconds; rest > 0 {
				out += fmt.Sprintf("%d%s", rest, next.label)
			}
		}
		return out
	}
	return "0秒"
}
