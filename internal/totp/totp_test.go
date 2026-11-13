package totp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// RFC 6238 appendix B, SHA-1, secret "12345678901234567890".
func TestRFCVectors(t *testing.T) {
	secret := encoding.EncodeToString([]byte("12345678901234567890"))
	for unix, want := range map[int64]string{59: "287082", 1111111109: "081804", 1234567890: "005924", 2000000000: "279037"} {
		got, err := Code(secret, time.Unix(unix, 0))
		if err != nil || got != want {
			t.Errorf("t=%d got %s want %s (%v)", unix, got, want, err)
		}
	}
}

func TestUseRejectsReplayAndDrift(t *testing.T) {
	dir := t.TempDir()
	secret := NewSecret()
	if err := Save(dir, Config{Secret: secret}); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(filepath.Join(dir, FileName)); info.Mode().Perm() != 0600 {
		t.Fatalf("mode %v", info.Mode())
	}
	now := time.Unix(1_800_000_000, 0)
	code, _ := Code(secret, now)
	if ok, err := Use(dir, code, now); !ok || err != nil {
		t.Fatalf("first use %v %v", ok, err)
	}
	if ok, _ := Use(dir, code, now); ok {
		t.Fatal("replayed code accepted")
	}
	late, _ := Code(secret, now.Add(-30*time.Second))
	if ok, _ := Use(dir, late, now); ok {
		t.Fatal("code older than the last used one accepted")
	}
	next, _ := Code(secret, now.Add(30*time.Second))
	if ok, _ := Use(dir, " "+next[:3]+" "+next[3:], now); !ok {
		t.Fatal("next-step code with spaces refused")
	}
	far, _ := Code(secret, now.Add(5*time.Minute))
	if ok, _ := Use(dir, far, now); ok {
		t.Fatal("code outside the window accepted")
	}
	if !strings.HasPrefix(URI(secret, "TZA Probe", "admin"), "otpauth://totp/TZA%20Probe:admin?") {
		t.Fatal(URI(secret, "TZA Probe", "admin"))
	}
	Remove(dir)
	if _, ok, _ := Load(dir); ok {
		t.Fatal("not removed")
	}
}
