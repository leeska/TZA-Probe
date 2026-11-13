package model

import "testing"

func TestCapabilitiesAreShapeChecked(t *testing.T) {
	for _, ok := range [][]string{nil, {"unlock", "parallel_jobs"}, {"shell", "docker", "proxy_xray"}} {
		if !ValidCapabilities(ok) {
			t.Errorf("%v refused", ok)
		}
	}
	long := make([]string, MaxCapabilities+1)
	for i := range long {
		long[i] = "c" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	for _, bad := range [][]string{{""}, {"Unlock"}, {"9lives"}, {"a-b"}, {"unlock", "unlock"}, {"averyveryveryveryveryverylongname1"}, long} {
		if ValidCapabilities(bad) {
			t.Errorf("%v accepted", bad)
		}
	}
}
