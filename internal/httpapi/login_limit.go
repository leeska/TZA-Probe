package httpapi

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/leeska/TZA-Probe/internal/account"
	"github.com/leeska/TZA-Probe/internal/model"
)

// 密码校验前按桶限制实际尝试速度，匿名桶允许 30 次突发、每 2 秒补充一次，
// 已识别设备允许 10 次突发、每 6 秒补充一次。额度耗尽后直接返回 429，
// 不校验密码；Retry-After 指明下一次额度恢复的等待时间。已识别设备使用独立额度，
// 避免匿名请求消耗它的登录机会。PBKDF2 另用信号量限制并发 CPU 开销。
// 设备 Cookie 由用户名、密码哈希和到期时间的 HMAC 签名；改密码后旧 Cookie 失效。
// 不按来源 IP 分桶：Server 常在本机反代或 CDN 之后，看到的地址并不可信。

const (
	deviceCookie       = "tza_device"
	deviceTTL          = 180 * 24 * time.Hour
	sharedLoginLimit   = 30 // 每分钟，所有没有设备凭据的请求合计
	deviceLoginLimit   = 10 // 每分钟，每台已登录过的设备
	deviceSignatureTag = "tza-device-v1"
)

// deviceSigningKey derives the device-cookie key from the admin token, so
// devices stay recognized across restarts; without a token the key lives only
// as long as the process.
func deviceSigningKey(adminToken string) []byte {
	if adminToken != "" {
		sum := sha256.Sum256([]byte(deviceSignatureTag + "|" + adminToken))
		return sum[:]
	}
	key := make([]byte, 32)
	rand.Read(key)
	return key
}

func (a *API) deviceMAC(acct account.Account, expiry int64) string {
	mac := hmac.New(sha256.New, a.deviceKey)
	mac.Write([]byte(deviceSignatureTag + "\n" + acct.Username + "\n" + acct.Hash + "\n" + strconv.FormatInt(expiry, 10)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// loginBucket names the rate-limit bucket of r: the device's own when r carries
// a valid device cookie for the current account, the shared one ("") otherwise.
func (a *API) loginBucket(r *http.Request, now time.Time) string {
	c, err := r.Cookie(deviceCookie)
	if err != nil || a.AccountDir == "" {
		return ""
	}
	expiryText, sig, ok := strings.Cut(c.Value, ".")
	expiry, err := strconv.ParseInt(expiryText, 10, 64)
	if !ok || err != nil || now.Unix() > expiry {
		return ""
	}
	acct, err := account.Load(a.AccountDir)
	if err != nil || subtle.ConstantTimeCompare([]byte(sig), []byte(a.deviceMAC(acct, expiry))) != 1 {
		return ""
	}
	return "device:" + model.Hash(c.Value)
}

// setDeviceCookie marks the browser making r as one that knows the password.
func (a *API) setDeviceCookie(w http.ResponseWriter, r *http.Request, now time.Time, expectedHash string) {
	acct, err := account.Load(a.AccountDir)
	if err != nil || acct.Hash != expectedHash {
		return
	}
	expiry := now.Add(deviceTTL).Unix()
	value := strconv.FormatInt(expiry, 10) + "." + a.deviceMAC(acct, expiry)
	http.SetCookie(w, &http.Cookie{Name: deviceCookie, Value: value, Path: "/api/v1/", HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteStrictMode, MaxAge: int(deviceTTL / time.Second)})
}

// loginLimited reports whether wrong passwords from r's bucket in the current
// minute passed its limit, counting one more first when failed is set.
func (a *API) loginLimited(r *http.Request, now time.Time, failed bool) bool {
	bucket := a.loginBucket(r, now)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.resetLoginWindow(now)
	if failed {
		a.loginFailures[bucket]++
	}
	limit := sharedLoginLimit
	if bucket != "" {
		limit = deviceLoginLimit
	}
	return a.loginFailures[bucket] > limit
}

// resetLoginWindow is called with mu held. Expired pacing entries may be
// discarded; future reservations must survive the failure-counter reset.
func (a *API) resetLoginWindow(now time.Time) {
	if now.Sub(a.loginWindow) >= time.Minute || a.loginFailures == nil {
		a.loginFailures = map[string]int{}
		a.loginWindow = now
		for bucket, next := range a.loginNext {
			if !next.After(now) {
				delete(a.loginNext, bucket)
			}
		}
	}
	if a.loginNext == nil {
		a.loginNext = map[string]time.Time{}
	}
}

// reserveLogin is a token bucket represented by its next virtual start time.
// Reserving before PBKDF2 bounds guesses, regardless of their HTTP outcome.
func (a *API) reserveLogin(r *http.Request, now time.Time) (time.Duration, bool) {
	if r.Context().Err() != nil {
		return 0, false
	}
	bucket := a.loginBucket(r, now)
	limit := sharedLoginLimit
	if bucket != "" {
		limit = deviceLoginLimit
	}
	interval := time.Minute / time.Duration(limit)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.resetLoginWindow(now)
	next := a.loginNext[bucket]
	if next.Before(now) {
		next = now
	}
	next = next.Add(interval)
	delay := next.Sub(now) - time.Minute
	if delay < 0 {
		delay = 0
	}
	if delay > 0 {
		return delay, false
	}
	a.loginNext[bucket] = next
	return delay, true
}

// loginConcurrency caps how many password verifications (PBKDF2, a few
// hundred milliseconds each) run at once, in addition to the attempt budget
// reserved before verification.
const loginConcurrency = 2

// verifyLogin checks the credentials. busy is true when the semaphore stayed
// full for a couple of seconds — the caller answers 429; otherwise hash is the
// account's stored digest on a match ("" on failure), and the failed attempt
// is the caller's to count. The hash lets the session bind to this password
// version, so a login that verified before a reset cannot outlive it.
func (a *API) verifyLogin(username, password string) (hash string, busy bool) {
	a.mu.Lock()
	if a.loginSlots == nil {
		a.loginSlots = make(chan struct{}, loginConcurrency)
	}
	slots := a.loginSlots
	a.mu.Unlock()
	select {
	case slots <- struct{}{}:
	case <-time.After(2 * time.Second):
		return "", true
	}
	defer func() { <-slots }()
	return a.checkAccount(username, password), false
}

// secureRequest reports whether r reached the panel over HTTPS, directly or
// (as the browser's Origin says) through a reverse proxy.
func secureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	origin := r.Header.Get("Origin")
	return strings.HasPrefix(origin, "https://")
}
