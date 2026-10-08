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
	// MaxLatencyWindowMinutes 是探测结果的保留期（storage.JobsRetention，7 天）：
	// 再长的窗口取到的还是这些行，只会更慢。storage 的测试把两者钉在一起。
	MaxLatencyWindowMinutes = 7 * 24 * 60
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

// 前台默认外观：访客自己没选过（localStorage 里没有偏好）时用哪一套明暗与皮肤。
// 默认值就是各处的内置默认（跟随系统 + 液态玻璃），所以没配过的部署行为不变；
// 访客一旦自己点了切换，存下的偏好优先于这里。
const (
	SettingDefaultTheme = "default_theme"
	SettingDefaultSkin  = "default_skin"

	DefaultPanelTheme = "auto"
	DefaultPanelSkin  = "glass"
)

// PanelThemes / PanelSkins 是允许写入的两组取值，与 web/theme.js 的 ORDER、
// web/skins.js 的 SKINS 一一对应（httpapi 的测试会拿 web/skins.js 对一遍）。
var PanelThemes = []string{"auto", "light", "dark"}
var PanelSkins = []string{"glass", "press", "shadcn", "anime"}

// PanelTheme / PanelSkin 把存下来的值变成可用值：空值或读不出来的值退回默认，
// 认不出来的皮肤退回默认皮肤——设置里一个手滑的值不该让面板挂在一张不存在的皮肤上。
func PanelTheme(stored string) string {
	v := strings.TrimSpace(stored)
	for _, ok := range PanelThemes {
		if v == ok {
			return v
		}
	}
	return DefaultPanelTheme
}

func PanelSkin(stored string) string {
	v := strings.TrimSpace(stored)
	for _, ok := range PanelSkins {
		if v == ok {
			return v
		}
	}
	return DefaultPanelSkin
}

func ValidatePanelTheme(v string) error {
	for _, ok := range PanelThemes {
		if v == ok {
			return nil
		}
	}
	return fmt.Errorf("默认明暗须为 %s 之一", strings.Join(PanelThemes, " / "))
}

func ValidatePanelSkin(v string) error {
	for _, ok := range PanelSkins {
		if v == ok {
			return nil
		}
	}
	return fmt.Errorf("默认皮肤须为 %s 之一", strings.Join(PanelSkins, " / "))
}
