package tzapkg

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Signature is TZA-CC's statement that an archive is the reviewed release of a
// package. CC signs on approval with its Ed25519 key; the Probe server checks
// it against the public keys it trusts before installing from the market.
type Signature struct {
	KeyID   string `json:"key_id"`
	Package string `json:"package"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	Sig     string `json:"sig"`
}

var (
	keyIDPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,31}$`)
	versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,4})\.(0|[1-9][0-9]{0,4})\.(0|[1-9][0-9]{0,4})$`)
)

// ValidVersion reports whether v is a market release version (MAJOR.MINOR.PATCH).
func ValidVersion(v string) bool { return versionPattern.MatchString(v) }

// message is what gets signed: one line per field under a fixed prefix, so no
// field can bleed into another.
func message(pkg, version, sum string) []byte {
	return []byte("tzapkg-sig-v1\n" + pkg + "\n" + version + "\n" + sum + "\n")
}

// Sign signs archive as release version of package pkg.
func Sign(key ed25519.PrivateKey, keyID, pkg, version string, archive []byte) Signature {
	digest := sha256.Sum256(archive)
	sum := hex.EncodeToString(digest[:])
	return Signature{
		KeyID:   keyID,
		Package: pkg,
		Version: version,
		SHA256:  sum,
		Sig:     base64.StdEncoding.EncodeToString(ed25519.Sign(key, message(pkg, version, sum))),
	}
}

// Verify checks that archive is exactly the signed release and that the
// signature comes from one of keys.
func (s Signature) Verify(keys map[string]ed25519.PublicKey, archive []byte) error {
	pub, ok := keys[s.KeyID]
	if !ok {
		return fmt.Errorf("签名密钥 %q 不在信任列表里", s.KeyID)
	}
	digest := sha256.Sum256(archive)
	if hex.EncodeToString(digest[:]) != s.SHA256 {
		return errors.New("包内容与签名不符（SHA-256 不一致）")
	}
	sig, err := base64.StdEncoding.DecodeString(s.Sig)
	if err != nil || !ed25519.Verify(pub, message(s.Package, s.Version, s.SHA256), sig) {
		return errors.New("包签名无效")
	}
	return nil
}

// FormatPublicKey renders a public key as "<key id>:<base64>", the form the
// Probe server's --cc-key flag takes.
func FormatPublicKey(keyID string, pub ed25519.PublicKey) string {
	return keyID + ":" + base64.StdEncoding.EncodeToString(pub)
}

// ParsePublicKey reads the "<key id>:<base64>" form.
func ParsePublicKey(s string) (string, ed25519.PublicKey, error) {
	id, b64, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok || !keyIDPattern.MatchString(id) {
		return "", nil, errors.New("公钥格式须为 <key id>:<base64>")
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return "", nil, errors.New("公钥不是 base64 编码的 Ed25519 公钥")
	}
	return id, ed25519.PublicKey(raw), nil
}
