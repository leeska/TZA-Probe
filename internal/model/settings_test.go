package model

import (
	"strings"
	"testing"
)

func TestLatencyWindowMinutesFallsBackAndClamps(t *testing.T) {
	cases := []struct {
		stored string
		want   int
	}{
		{"", DefaultLatencyWindowMinutes},
		{"   ", DefaultLatencyWindowMinutes},
		{"abc", DefaultLatencyWindowMinutes},
		{"-5", MinLatencyWindowMinutes},
		{"0", MinLatencyWindowMinutes},
		{"60", 60},
		{"1440", 1440},
		{"10080", MaxLatencyWindowMinutes},
		{"99999", MaxLatencyWindowMinutes},
	}
	for _, c := range cases {
		if got := LatencyWindowMinutes(c.stored); got != c.want {
			t.Fatalf("LatencyWindowMinutes(%q) = %d, want %d", c.stored, got, c.want)
		}
	}
}

func TestValidateLatencyWindowMinutes(t *testing.T) {
	if err := ValidateLatencyWindowMinutes(DefaultLatencyWindowMinutes); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []int{0, -1, MaxLatencyWindowMinutes + 1} {
		if err := ValidateLatencyWindowMinutes(bad); err == nil {
			t.Fatalf("%d 应该被拒绝", bad)
		}
	}
}

func TestPriceUnitFallsBackAndNormalizes(t *testing.T) {
	cases := []struct {
		stored string
		want   string
	}{
		{"", DefaultPriceUnit},
		{"   ", DefaultPriceUnit},
		{"usdt", DefaultPriceUnit}, // 汇率表里没有的币种折不出汇总，退回默认
		{"usd", "USD"},             // 手滑小写也能认，落库前统一大写
		{" USD ", "USD"},
		{"JPY", "JPY"},
	}
	for _, c := range cases {
		if got := PriceUnit(c.stored); got != c.want {
			t.Fatalf("PriceUnit(%q) = %q, want %q", c.stored, got, c.want)
		}
	}
}

func TestValidatePriceUnit(t *testing.T) {
	for _, ok := range PriceUnits {
		if err := ValidatePriceUnit(ok); err != nil {
			t.Fatalf("%q 应该可用: %v", ok, err)
		}
		if err := ValidatePriceUnit(strings.ToLower(ok)); err != nil {
			t.Fatalf("小写 %q 也应该可用: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "  ", "USDT", "CNY2"} {
		if err := ValidatePriceUnit(bad); err == nil {
			t.Fatalf("%q 应该被拒绝", bad)
		}
	}
}

// 汇总单位随 PanelPatch 走整条链路：只改传了的项，空 patch 不算改动。
func TestPanelPatchPriceUnit(t *testing.T) {
	unit := "USD"
	p := PanelPatch{PriceUnit: &unit}
	if p.Empty() {
		t.Fatal("带 price_unit 的 patch 不该是空的")
	}
	if err := p.Validate(nil); err != nil {
		t.Fatal(err)
	}
	bad := "USDT"
	if err := (PanelPatch{PriceUnit: &bad}).Validate(nil); err == nil {
		t.Fatal("未知币种应该被拒绝")
	}
	if !(PanelPatch{}).Empty() {
		t.Fatal("空 patch 应该是空的")
	}
}
