package tzapkg

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
)

func TestSignVerify(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	archive := []byte("zip bytes")
	sig := Sign(key, "cc-test", "matcha", "1.0.0", archive)
	keys := map[string]ed25519.PublicKey{"cc-test": pub}
	if err := sig.Verify(keys, archive); err != nil {
		t.Fatal(err)
	}
	if err := sig.Verify(keys, []byte("zip bytez")); err == nil {
		t.Fatal("tampered archive verified")
	}
	forged := sig
	forged.Version = "1.0.1"
	if err := forged.Verify(keys, archive); err == nil {
		t.Fatal("signature verified for another version")
	}
	if err := sig.Verify(map[string]ed25519.PublicKey{"other": pub}, archive); err == nil {
		t.Fatal("untrusted key id verified")
	}

	id, parsed, err := ParsePublicKey(FormatPublicKey("cc-test", pub))
	if err != nil || id != "cc-test" || !parsed.Equal(pub) {
		t.Fatalf("round trip: %q %v", id, err)
	}
	for _, bad := range []string{"", "cc-test", "Bad Id:AAAA", "cc-test:not-base64", "cc-test:AAAA"} {
		if _, _, err := ParsePublicKey(bad); err == nil {
			t.Errorf("ParsePublicKey(%q) accepted", bad)
		}
	}
}

func TestValidVersion(t *testing.T) {
	for v, want := range map[string]bool{"1.0.0": true, "0.12.3": true, "1.0": false, "01.0.0": false, "1.0.0-beta": false, "v1.0.0": false} {
		if ValidVersion(v) != want {
			t.Errorf("ValidVersion(%q) = %v", v, !want)
		}
	}
}
