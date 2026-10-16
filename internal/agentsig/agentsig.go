// Package agentsig signs Agent binaries for one-click upgrades.
//
// The Server only relays upgrades; it never holds the signing key. The
// operator generates a key pair on their own machine (tza-server agent-keygen),
// signs each Agent build there (tza-server agent-sign) and puts the binaries,
// their .sig files and the public key into the Server's agents directory.
// The install script pins that public key on the node, and the Agent accepts
// an upgrade only when it is signed by a pinned key and is newer than itself.
// A Server that is taken over can therefore stop upgrades, but cannot push a
// binary of its own or roll nodes back to an older signed release.
package agentsig

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/leeska/TZA-Probe/pkg/tzapkg"
)

// Signature is the operator's statement that a binary is release Version of
// the Agent for Arch. It is stored next to the binary as <binary>.sig (JSON).
type Signature struct {
	KeyID   string `json:"key_id"`
	Arch    string `json:"arch"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	Sig     string `json:"sig"`
}

// message is what gets signed: one line per field under a fixed prefix, so no
// field can bleed into another and no other TZA signature can be replayed here.
func message(arch, version, sum string) []byte {
	return []byte("tza-agent-sig-v1\n" + arch + "\n" + version + "\n" + sum + "\n")
}

// Sign signs the binary digest sum as release version for arch.
func Sign(key ed25519.PrivateKey, keyID, arch, version, sum string) Signature {
	return Signature{
		KeyID:   keyID,
		Arch:    arch,
		Version: version,
		SHA256:  sum,
		Sig:     base64.StdEncoding.EncodeToString(ed25519.Sign(key, message(arch, version, sum))),
	}
}

// Verify checks the signature against the trusted keys. It does not look at
// the binary: callers compare SHA256 with the digest of what they downloaded.
func (s Signature) Verify(keys map[string]ed25519.PublicKey) error {
	pub, ok := keys[s.KeyID]
	if !ok {
		return fmt.Errorf("signing key %q is not trusted on this node", s.KeyID)
	}
	sig, err := base64.StdEncoding.DecodeString(s.Sig)
	if err != nil || !ed25519.Verify(pub, message(s.Arch, s.Version, s.SHA256), sig) {
		return errors.New("agent signature is invalid")
	}
	return nil
}

// ParseKeys reads trusted public keys, one "<key id>:<base64>" per line; blank
// lines and lines starting with # are skipped.
func ParseKeys(text string) (map[string]ed25519.PublicKey, error) {
	keys := map[string]ed25519.PublicKey{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		id, pub, err := tzapkg.ParsePublicKey(line)
		if err != nil {
			return nil, err
		}
		keys[id] = pub
	}
	return keys, nil
}

// GenerateKey makes a key pair and returns the private key file content and
// the public key line, both "<key id>:<base64>".
func GenerateKey(keyID string) (private, public string, err error) {
	if !keyIDPattern.MatchString(keyID) {
		return "", "", errors.New("key id: lowercase letters, digits, dots and dashes, at most 32")
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return keyID + ":" + base64.StdEncoding.EncodeToString(priv) + "\n", tzapkg.FormatPublicKey(keyID, pub) + "\n", nil
}

var keyIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,31}$`)

// ParsePrivateKey reads a private key file written by GenerateKey.
func ParsePrivateKey(text string) (string, ed25519.PrivateKey, error) {
	id, b64, ok := strings.Cut(strings.TrimSpace(text), ":")
	if !ok || !keyIDPattern.MatchString(id) {
		return "", nil, errors.New("private key must be <key id>:<base64>")
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return "", nil, errors.New("private key is not a base64 Ed25519 private key")
	}
	return id, ed25519.PrivateKey(raw), nil
}

var versionPattern = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

type version struct {
	core       [3]int
	prerelease bool
}

func parseVersion(v string) (version, bool) {
	m := versionPattern.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return version{}, false
	}
	var out version
	for i := range out.core {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return version{}, false
		}
		out.core[i] = n
	}
	out.prerelease = m[4] != ""
	return out, true
}

// Releasable reports whether v can be signed: MAJOR.MINOR.PATCH without a
// pre-release suffix, so every signed build is strictly ordered.
func Releasable(v string) bool {
	p, ok := parseVersion(v)
	return ok && !p.prerelease
}

// Newer reports whether release candidate is newer than running. A running
// version that cannot be read (a hand-built binary) accepts any release; a
// pre-release (1.5.0-dev) is older than its release (1.5.0).
func Newer(candidate, running string) bool {
	c, ok := parseVersion(candidate)
	if !ok || c.prerelease {
		return false
	}
	r, ok := parseVersion(running)
	if !ok {
		return true
	}
	for i := range c.core {
		if c.core[i] != r.core[i] {
			return c.core[i] > r.core[i]
		}
	}
	return r.prerelease
}
