package model

import "testing"

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
