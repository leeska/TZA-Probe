package model

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// 面板显示项：与通知出口分开存放（settings 表里一个键一个值），所以用 sqlite3 就能读，
// 也不需要改表结构。
const (
	// SettingLatencyWindowMinutes 是公开面板延迟色带最早显示多久以前的探测（分钟）。
	// 色带固定画最近若干次探测，这一项只是年龄上限：早于它的探测不画，那几格留空。
	// 查询开销不随它变大（见 storage.RecentLatencySamples）。
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

// PanelThemes / PanelSkins 是内置的两组取值，与 web/theme.js 的 ORDER、internal/theme 的
// Builtins 一一对应（httpapi 的测试会拿它们对一遍）。后台上传的主题包另由 Service.Themes 认。
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
	return fmt.Errorf("默认皮肤须为 %s 之一，或一个已安装的主题包", strings.Join(PanelSkins, " / "))
}

// 站点名称：浏览器标签页标题与公开面板、后台品牌区显示的名字。空值就是内置的 TZA Probe 品牌，
// 所以没配过的部署外观不变。logo 是上传的图片文件，不在这张表里（见 internal/brand）。
const (
	SettingSiteName = "site_name"

	DefaultSiteName = "TZA Probe"
	MaxSiteNameLen  = 32 // 字符数：品牌区一行放得下，标签页标题也不至于被截成省略号
)

// SiteName 把存下来的值变成可显示的名字：空值（或手改库留下的空白）就是 ""，由前端显示内置品牌。
func SiteName(stored string) string {
	return strings.TrimSpace(stored)
}

// ValidateSiteName 拦住过长或带控制字符的名字；空串表示恢复默认。
func ValidateSiteName(v string) error {
	v = strings.TrimSpace(v)
	if utf8.RuneCountInString(v) > MaxSiteNameLen {
		return fmt.Errorf("站点名称最多 %d 个字", MaxSiteNameLen)
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return fmt.Errorf("站点名称不能含控制字符")
		}
	}
	return nil
}

// PanelSettings is the visitor-facing panel configuration as the admin pages
// and the MCP tools read it: every value already normalized. SiteName "" and
// SiteLogo "" mean the built-in TZA Probe brand; SiteLogo is the uploaded
// logo's versioned URL.
type PanelSettings struct {
	LatencyWindowMinutes int    `json:"latency_window_minutes"`
	DefaultTheme         string `json:"default_theme"`
	DefaultSkin          string `json:"default_skin"`
	SiteName             string `json:"site_name"`
	SiteLogo             string `json:"site_logo"`
}

// PanelPatch changes only the fields it carries. An empty SiteName restores
// the default; the logo is uploaded separately (PUT /api/v1/admin/panel/logo).
type PanelPatch struct {
	LatencyWindowMinutes *int    `json:"latency_window_minutes"`
	DefaultTheme         *string `json:"default_theme"`
	DefaultSkin          *string `json:"default_skin"`
	SiteName             *string `json:"site_name"`
}

// Empty reports whether the patch changes nothing.
func (p PanelPatch) Empty() bool {
	return p.LatencyWindowMinutes == nil && p.DefaultTheme == nil && p.DefaultSkin == nil && p.SiteName == nil
}

// Validate checks every carried field before anything is written. installed
// reports whether a skin id names an uploaded theme package (nil: built-ins only).
func (p PanelPatch) Validate(installed func(string) bool) error {
	if p.LatencyWindowMinutes != nil {
		if err := ValidateLatencyWindowMinutes(*p.LatencyWindowMinutes); err != nil {
			return err
		}
	}
	if p.DefaultTheme != nil {
		if err := ValidatePanelTheme(*p.DefaultTheme); err != nil {
			return err
		}
	}
	if p.DefaultSkin != nil && (installed == nil || !installed(*p.DefaultSkin)) {
		if err := ValidatePanelSkin(*p.DefaultSkin); err != nil {
			return err
		}
	}
	if p.SiteName != nil {
		if err := ValidateSiteName(*p.SiteName); err != nil {
			return err
		}
	}
	return nil
}
