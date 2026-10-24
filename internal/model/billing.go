package model

import (
	"fmt"
	"time"
)

// HumanBytes renders a byte figure the way the notes do (decimal, 3 significant
// digits at most) for alert messages such as「本周期用量已达 1.00 TB 额度」.
func HumanBytes(n int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	value := float64(n)
	i := 0
	for value >= 1000 && i < len(units)-1 {
		value /= 1000
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}
	if value < 100 {
		return fmt.Sprintf("%.1f %s", value, units[i])
	}
	return fmt.Sprintf("%.0f %s", value, units[i])
}

// parseUTC accepts second-precision timestamps and returns them in UTC.
func parseUTC(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

// Cycle uses UTC monthly boundaries anchored at an instant: the anchor's day and
// time of day repeat every month, clamping days such as the 31st in February.
// The anchor keeps its seconds, so a cycle can start at e.g. 12:30:45 UTC.
func Cycle(now time.Time, anchor time.Time) (time.Time, time.Time) {
	now, anchor = now.UTC(), anchor.UTC()
	shift := func(months int) time.Time {
		year, month := anchor.Year(), int(anchor.Month())-1+months
		year += month / 12
		month = ((month % 12) + 12) % 12
		last := time.Date(year, time.Month(month+1)+1, 0, 0, 0, 0, 0, time.UTC).Day()
		return time.Date(year, time.Month(month+1), min(anchor.Day(), last),
			anchor.Hour(), anchor.Minute(), anchor.Second(), 0, time.UTC)
	}
	// Months between the anchor month and now, corrected by one when the day
	// clamp pushes the boundary past now.
	index := (now.Year()-anchor.Year())*12 + int(now.Month()) - int(anchor.Month())
	if shift(index).After(now) {
		index--
	}
	return shift(index), shift(index + 1)
}

// BillingAnchor parses a stored cycle anchor; the zero value falls back to the
// first of the current month so a config without one still bills monthly.
func BillingAnchor(at string, now time.Time) time.Time {
	parsed, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return time.Date(now.UTC().Year(), now.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	return parsed.UTC()
}

func Traffic(in, out int64, mode string) int64 {
	switch mode {
	case "in":
		return in
	case "out":
		return out
	case "max":
		return max(in, out)
	default:
		return in + out
	}
}

// RemainingValue estimates the prepaid time left using fixed price-period
// lengths, independently of monthly traffic resets. Nil means not configured.
// Multiple prepaid periods may be worth more than one period's price.
func RemainingValue(b Billing, now time.Time) *float64 {
	zero := 0.0
	if b.Free {
		return &zero
	}
	periods := map[string]int{"周": 7, "月": 30, "季": 90, "半年": 180, "年": 365, "两年": 730, "三年": 1095}
	days := periods[b.PricePeriod]
	expiry, err := time.Parse(time.RFC3339, b.ExpiresAt)
	if err != nil || days == 0 || b.Price <= 0 || !finite(b.Price) {
		return nil
	}
	remaining := float64(expiry.Unix()-now.Unix()) + float64(expiry.Nanosecond()-now.Nanosecond())/1e9
	value := b.Price * (max(0, remaining) / float64(days*86400))
	if !finite(value) {
		return nil
	}
	return &value
}
