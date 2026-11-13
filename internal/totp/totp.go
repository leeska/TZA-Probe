// Package totp is the panel login's second factor: RFC 6238 time-based codes
// (HMAC-SHA1, 30-second steps, 6 digits), the format every authenticator app
// reads. The secret lives in its own 0600 file next to admin-account, so a
// password reset leaves it alone and `tza-server reset-password
// --disable-totp` can remove it without touching the database.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// FileName is the TOTP file inside the data directory.
const FileName = "admin-totp"

const (
	step   = 30
	digits = 6
	// window accepts the previous and the next step, for clock drift.
	window = 1
)

var encoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// Config is the stored second factor. LastStep is the newest step already
// used to sign in: a code is accepted once.
type Config struct {
	Secret   string `json:"secret"`
	LastStep int64  `json:"last_step"`
}

// NewSecret returns a random 160-bit secret in base32.
func NewSecret() string {
	b := make([]byte, 20)
	rand.Read(b)
	return encoding.EncodeToString(b)
}

// URI is the otpauth:// link an authenticator app imports (as text or QR).
func URI(secret, issuer, account string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{"secret": {secret}, "issuer": {issuer}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// Code is the code for secret at time t.
func Code(secret string, t time.Time) (string, error) {
	return code(secret, t.Unix()/step)
}

func code(secret string, counter int64) (string, error) {
	key, err := encoding.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil || len(key) < 10 {
		return "", errors.New("invalid TOTP secret")
	}
	mac := hmac.New(sha1.New, key)
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(counter))
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", digits, value%1_000_000), nil
}

// Match reports the step a code is valid for at time t (within the drift
// window) and newer than after; 0 means no match.
func Match(secret, candidate string, t time.Time, after int64) int64 {
	candidate = strings.ReplaceAll(strings.TrimSpace(candidate), " ", "")
	if len(candidate) != digits {
		return 0
	}
	now := t.Unix() / step
	for counter := now - window; counter <= now+window; counter++ {
		want, err := code(secret, counter)
		if err == nil && counter > after && subtle.ConstantTimeCompare([]byte(want), []byte(candidate)) == 1 {
			return counter
		}
	}
	return 0
}

func path(dir string) string { return filepath.Join(dir, FileName) }

// Load reads dir's second factor; ok is false when none is set up.
func Load(dir string) (cfg Config, ok bool, err error) {
	data, err := os.ReadFile(path(dir))
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, false, nil
	}
	if err != nil {
		return Config{}, false, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil || cfg.Secret == "" {
		return Config{}, false, fmt.Errorf("%s 内容无效，可用 tza-server reset-password --disable-totp 关闭两步验证", path(dir))
	}
	return cfg, true, nil
}

var saveMu sync.Mutex

// Save writes cfg to dir atomically with mode 0600.
func Save(dir string, cfg Config) error {
	saveMu.Lock()
	defer saveMu.Unlock()
	return save(dir, cfg)
}

func save(dir string, cfg Config) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+FileName+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(append(data, '\n')); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0600)
	}
	if err == nil {
		err = ownLikeDir(tmp.Name(), dir)
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path(dir))
}

// Remove turns the second factor off.
func Remove(dir string) error {
	err := os.Remove(path(dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Use checks candidate against dir's second factor and, when it matches,
// records its step so the same code cannot be used again.
func Use(dir, candidate string, t time.Time) (bool, error) {
	// One lock around read, check and write: two logins racing with the
	// same code cannot both pass.
	saveMu.Lock()
	defer saveMu.Unlock()
	cfg, ok, err := Load(dir)
	if err != nil || !ok {
		return false, err
	}
	matched := Match(cfg.Secret, candidate, t, cfg.LastStep)
	if matched == 0 {
		return false, nil
	}
	cfg.LastStep = matched
	return true, save(dir, cfg)
}
