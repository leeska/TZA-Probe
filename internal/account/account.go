// Package account is the administrator's username and password for the panel
// login. It lives in a 0600 file in the data directory, next to admin-token,
// rather than in the database: the terminal reset (tza-server reset-password)
// must work without opening the database, and the running server rereads the
// file on every login so a reset takes effect without a restart.
package account

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// FileName is the account file inside the data directory.
const FileName = "admin-account"

// DefaultUsername is the username a fresh installation starts with.
const DefaultUsername = "admin"

// iterations of PBKDF2-HMAC-SHA256 (OWASP 2023 recommendation). A login costs
// a few hundred milliseconds on a small VPS; the login rate limit caps how many
// run per minute.
const iterations = 600_000

const (
	minPassword = 8
	maxPassword = 128
	maxUsername = 32
)

// Account is the stored record. Only a salted hash of the password is kept.
type Account struct {
	Username   string `json:"username"`
	Salt       string `json:"salt"`
	Hash       string `json:"hash"`
	Iterations int    `json:"iterations"`
}

// ErrNotFound means no account has been created in the data directory yet.
var ErrNotFound = errors.New("后台账号尚未创建")

func path(dir string) string { return filepath.Join(dir, FileName) }

// Load reads the account in dir.
func Load(dir string) (Account, error) {
	data, err := os.ReadFile(path(dir))
	if errors.Is(err, os.ErrNotExist) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, err
	}
	var a Account
	if err := json.Unmarshal(data, &a); err != nil || a.Username == "" || a.Hash == "" || a.Iterations <= 0 {
		return Account{}, fmt.Errorf("%s 内容无效，可用 tza-server reset-password 重建", path(dir))
	}
	return a, nil
}

// Check reports whether username and password match. Both are compared in
// constant time and the hash is always computed, so a wrong username takes as
// long as a wrong password.
func (a Account) Check(username, password string) bool {
	salt, err := base64.RawStdEncoding.DecodeString(a.Salt)
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(a.Hash)
	if err != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, a.Iterations, len(want))
	if err != nil {
		return false
	}
	userOK := subtle.ConstantTimeCompare([]byte(username), []byte(a.Username)) == 1
	passOK := subtle.ConstantTimeCompare(got, want) == 1
	return userOK && passOK
}

// Validate checks a username and password before they are stored.
func Validate(username, password string) error {
	if username == "" {
		return errors.New("用户名不能为空")
	}
	if utf8.RuneCountInString(username) > maxUsername {
		return fmt.Errorf("用户名最多 %d 个字符", maxUsername)
	}
	for _, r := range username {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return errors.New("用户名不能含空白或控制字符")
		}
	}
	if n := utf8.RuneCountInString(password); n < minPassword || n > maxPassword {
		return fmt.Errorf("密码长度须在 %d 到 %d 个字符之间", minPassword, maxPassword)
	}
	return nil
}

// Save hashes password and writes the account to dir, replacing any previous
// one atomically. When run as root (the terminal reset), the file is handed to
// the owner of dir so the service account can still read it.
func Save(dir, username, password string) error {
	username = strings.TrimSpace(username)
	if err := Validate(username, password); err != nil {
		return err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	hash, err := pbkdf2.Key(sha256.New, password, salt, iterations, 32)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(Account{
		Username:   username,
		Salt:       base64.RawStdEncoding.EncodeToString(salt),
		Hash:       base64.RawStdEncoding.EncodeToString(hash),
		Iterations: iterations,
	}, "", "  ")
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
	if err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0600); err != nil {
		return err
	}
	if err := OwnLikeDir(tmp.Name(), dir); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path(dir))
}

// Ensure creates the default account with a random password when dir has
// none. created is false (and password empty) when one already existed.
func Ensure(dir string) (username, password string, created bool, err error) {
	a, err := Load(dir)
	if err == nil {
		return a.Username, "", false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return "", "", false, err
	}
	password = RandomPassword()
	if err := Save(dir, DefaultUsername, password); err != nil {
		return "", "", false, err
	}
	return DefaultUsername, password, true, nil
}

// InitialFileName is the one-time file WriteInitialCredentials drops into the
// data directory. Container logs are kept forever, so the auto-generated
// password must never go there; the operator reads this file instead and
// deletes it after signing in.
const InitialFileName = "admin-password.txt"

// WriteInitialCredentials stores an auto-generated username and password as
// InitialFileName (0600) in dir.
func WriteInitialCredentials(dir, username, password string) error {
	tmp, err := os.CreateTemp(dir, "."+InitialFileName+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = fmt.Fprintf(tmp, "用户名：%s\n密码：%s\n（登录后请在面板改成自己的密码，并删除本文件）\n", username, password); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0600); err != nil {
		return err
	}
	if err := OwnLikeDir(tmp.Name(), dir); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, InitialFileName))
}

// RandomPassword returns 16 symbols from an alphabet without look-alikes
// (0/O, 1/l/I), so a password read off a terminal is typed right first time.
func RandomPassword() string {
	const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	// 256 % 56 != 0 leaves a slight bias; at 16 symbols that is still well
	// over 90 bits, far beyond what the login rate limit lets anyone try.
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}
