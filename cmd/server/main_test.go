package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/leeska/TZA-Probe/internal/market"
	"github.com/leeska/TZA-Probe/pkg/tzapkg"
)

func TestDatabasePath(t *testing.T) {
	dir := t.TempDir()
	current, legacy := filepath.Join(dir, "tza-probe.db"), filepath.Join(dir, "tza-v2.db")
	if got := databasePath(dir); got != current {
		t.Fatalf("empty dir: %s", got)
	}
	if err := os.WriteFile(legacy, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if got := databasePath(dir); got != legacy {
		t.Fatalf("only the older file: %s", got)
	}
	// A restored backup under the current name wins over a leftover older file.
	if err := os.WriteFile(current, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if got := databasePath(dir); got != current {
		t.Fatalf("both files: %s", got)
	}
}

func TestMarketClient(t *testing.T) {
	env := func(vars map[string]string) func(string) string { return func(k string) string { return vars[k] } }
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	key := tzapkg.FormatPublicKey("cc-1", pub)
	// No address anywhere: no market, unless an official default is compiled in.
	// Nothing set means the official CC with its built-in keys.
	if c, err := marketClient("", nil, env(nil), t.TempDir(), nil, nil); err != nil || c == nil || c.URL() != market.DefaultURL || c.Status().TrustedKeys != len(market.OfficialKeys) {
		t.Fatalf("default: %v %v", c, err)
	}
	// "off" (flag or variable, any case) turns the market off; the flag still wins over the variable.
	for _, tc := range []struct{ flag, env string }{{"off", ""}, {"", "OFF"}, {" Off ", "https://env.example"}} {
		if c, err := marketClient(tc.flag, nil, env(map[string]string{"TZA_CC_URL": tc.env}), t.TempDir(), nil, nil); err != nil || c != nil {
			t.Fatalf("off %+v: %v %v", tc, c, err)
		}
	}
	if c, _ := marketClient("https://flag.example", nil, env(map[string]string{"TZA_CC_URL": "off"}), t.TempDir(), nil, nil); c == nil {
		t.Fatal("flag URL should win over TZA_CC_URL=off")
	}
	// The flag wins over the variable; keys from both are trusted.
	pub2, _, _ := ed25519.GenerateKey(rand.Reader)
	c, err := marketClient("https://flag.example", []string{key}, env(map[string]string{"TZA_CC_URL": "https://env.example", "TZA_CC_KEYS": " " + tzapkg.FormatPublicKey("cc-2", pub2) + ", "}), t.TempDir(), nil, nil)
	if err != nil || c.URL() != "https://flag.example" || c.Status().TrustedKeys != len(market.OfficialKeys)+2 {
		t.Fatalf("flag: %v %+v", err, c.Status())
	}
	if c, _ := marketClient("", nil, env(map[string]string{"TZA_CC_URL": "https://env.example/"}), t.TempDir(), nil, nil); c == nil || c.URL() != "https://env.example" {
		t.Fatalf("env: %v", c)
	}
	for _, bad := range [][]string{{"nope"}, {"cc-1:AAAA"}, {key, tzapkg.FormatPublicKey("cc-1", pub2)}} {
		if _, err := marketClient("https://cc.example", bad, env(nil), t.TempDir(), nil, nil); err == nil {
			t.Fatalf("keys %v accepted", bad)
		}
	}
	if _, err := marketClient("ftp://cc.example", nil, env(nil), t.TempDir(), nil, nil); err == nil {
		t.Fatal("bad URL accepted")
	}
}
