package model

import (
	"math"
	"testing"
	"time"
)

func TestRemainingValue(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	paid := Billing{Price: 30, PricePeriod: "月", Currency: "USD", ExpiresAt: now.Add(15 * 24 * time.Hour).Format(time.RFC3339)}
	change := func(f func(*Billing)) Billing { b := paid; f(&b); return b }
	tests := []struct {
		name    string
		b       Billing
		want    float64
		unknown bool
	}{
		{name: "half month", b: paid, want: 15},
		{name: "partial day", b: change(func(b *Billing) { b.ExpiresAt = now.Add(time.Hour).Format(time.RFC3339) }), want: 1.0 / 24},
		{name: "multiple prepaid periods", b: change(func(b *Billing) { b.ExpiresAt = now.Add(60 * 24 * time.Hour).Format(time.RFC3339) }), want: 60},
		{name: "at expiry", b: change(func(b *Billing) { b.ExpiresAt = now.Format(time.RFC3339) })},
		{name: "expired", b: change(func(b *Billing) { b.ExpiresAt = now.Add(-time.Second).Format(time.RFC3339) })},
		{name: "free without dates", b: Billing{Free: true}},
		{name: "missing price", b: change(func(b *Billing) { b.Price = 0 }), unknown: true},
		{name: "missing expiry", b: change(func(b *Billing) { b.ExpiresAt = "" }), unknown: true},
		{name: "invalid expiry", b: change(func(b *Billing) { b.ExpiresAt = "invalid" }), unknown: true},
		{name: "unknown period", b: change(func(b *Billing) { b.PricePeriod = "自定义" }), unknown: true},
		{name: "missing period", b: change(func(b *Billing) { b.PricePeriod = "" }), unknown: true},
		{name: "nonfinite price", b: change(func(b *Billing) { b.Price = math.Inf(1) }), unknown: true},
		{name: "traffic reset independent", b: change(func(b *Billing) { b.ResetAt = "2026-10-31T12:34:56Z"; b.TrafficLimit = 1 }), want: 15},
		{name: "timezone equivalent", b: change(func(b *Billing) { b.ExpiresAt = "2026-11-06T20:00:00+08:00" }), want: 15},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RemainingValue(tt.b, now)
			if tt.unknown {
				if got != nil {
					t.Fatalf("unknown value = %v", *got)
				}
				return
			}
			if got == nil || math.Abs(*got-tt.want) > 1e-9 {
				t.Fatalf("got=%v want=%g", got, tt.want)
			}
		})
	}
	for period, days := range map[string]int{"周": 7, "月": 30, "季": 90, "半年": 180, "年": 365, "两年": 730, "三年": 1095} {
		b := Billing{Price: 100, PricePeriod: period, ExpiresAt: now.Add(time.Duration(days) * 24 * time.Hour).Format(time.RFC3339)}
		got := RemainingValue(b, now)
		if got == nil || *got != 100 {
			t.Fatalf("%s: one full price period = %v", period, got)
		}
	}
}
