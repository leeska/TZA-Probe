package agentsig

import (
	"strings"
	"testing"
)

func TestSignVerifyAndKeyFiles(t *testing.T) {
	private, public, err := GenerateKey("ops-2026")
	if err != nil {
		t.Fatal(err)
	}
	id, key, err := ParsePrivateKey(private)
	if err != nil || id != "ops-2026" {
		t.Fatal(id, err)
	}
	keys, err := ParseKeys("# pinned by install.sh\n\n" + public)
	if err != nil || len(keys) != 1 {
		t.Fatal(keys, err)
	}
	sig := Sign(key, id, "amd64", "1.5.0", strings.Repeat("ab", 32))
	if err := sig.Verify(keys); err != nil {
		t.Fatal(err)
	}
	// 任何一个字段被改都不再成立；别的密钥签的也不认。
	for name, edit := range map[string]func(*Signature){
		"arch":    func(s *Signature) { s.Arch = "arm64" },
		"version": func(s *Signature) { s.Version = "9.0.0" },
		"sha256":  func(s *Signature) { s.SHA256 = strings.Repeat("cd", 32) },
		"key id":  func(s *Signature) { s.KeyID = "other" },
		"sig":     func(s *Signature) { s.Sig = "AAAA" },
	} {
		bad := sig
		edit(&bad)
		if bad.Verify(keys) == nil {
			t.Fatalf("%s edited: must not verify", name)
		}
	}
	if _, _, err := ParsePrivateKey(public); err == nil {
		t.Fatal("a public key is not a private key")
	}
	if _, _, err := GenerateKey("Bad ID"); err == nil {
		t.Fatal("invalid key id accepted")
	}
	if _, err := ParseKeys("not-a-key"); err == nil {
		t.Fatal("garbage key file accepted")
	}
}

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		candidate, running string
		want               bool
	}{
		{"1.5.0", "1.4.2", true},
		{"v1.5.0", "1.4.2", true},
		{"1.4.10", "1.4.9", true},
		{"1.5.0", "1.5.0", false},
		{"1.4.0", "1.5.0", false},
		{"1.5.0", "1.5.0-dev", true},
		{"1.5.0", "1.0.0-dev+abc1234", true},
		{"1.5.0-rc1", "1.4.0", false},
		{"1.5", "1.4.0", false},
		{"1.5.0", "dev", true},
	} {
		if got := Newer(c.candidate, c.running); got != c.want {
			t.Errorf("Newer(%q, %q) = %v", c.candidate, c.running, got)
		}
	}
	if Releasable("1.0.0-dev+abc") || !Releasable("v1.5.0") {
		t.Fatal("Releasable")
	}
}
