package model

import (
	"fmt"
	"strconv"
	"strings"
)

// 面板显示项：与通知出口分开存放（settings 表里一个键一个值），所以用 sqlite3 就能读，
// 也不需要改表结构。
const (
	// SettingLatencyWindowMinutes 是公开面板延迟色带往回取多久的分钟数。
	// 色带每格一次探测，取多了只是白扫历史（线上 17 台实测：1 小时约 0.05s、
	// 24 小时约 1.25s），取少了老目标就只剩空格。
	SettingLatencyWindowMinutes = "latency_window_minutes"

	DefaultLatencyWindowMinutes = 60
	MinLatencyWindowMinutes     = 1
	MaxLatencyWindowMinutes     = 1440
)

// LatencyWindowMinutes 把存下来的值变成可用分钟数：空值或读不出来的值退回默认，
// 超范围就夹到区间里——设置里一个手滑的值不该让样本查询又去扫整段保留期。
func LatencyWindowMinutes(stored string) int {
	n, err := strconv.Atoi(strings.TrimSpace(stored))
	if err != nil {
		return DefaultLatencyWindowMinutes
	}
	return min(max(n, MinLatencyWindowMinutes), MaxLatencyWindowMinutes)
}

// ValidateLatencyWindowMinutes 拦住写入的越界值，让面板拿到的永远是能用的数。
func ValidateLatencyWindowMinutes(minutes int) error {
	if minutes < MinLatencyWindowMinutes || minutes > MaxLatencyWindowMinutes {
		return fmt.Errorf("延迟色带时间窗须为 %d–%d 分钟", MinLatencyWindowMinutes, MaxLatencyWindowMinutes)
	}
	return nil
}
