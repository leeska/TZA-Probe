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

// 登录限流分两档。没有设备凭据的请求共用一个桶（每分钟 30 次失败）：这是挡猜密码的那道闸，
// 匿名者能把它刷满。为了不让人靠刷满它把管理员关在门外，成功登录过的浏览器会拿到一张
// 「设备」Cookie——HMAC 签过的用户名、密码哈希与到期时间——之后它用自己的桶（每分钟 10 次）。
// 攻击者伪造不出设备 Cookie，猜密码的速度仍受共享桶限制；改密码后旧设备 Cookie 随密码哈希一起失效。
// 不按来源 IP 分桶：Server 通常在本机反代甚至 CDN 之后，看到的地址并不可信。

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
func (a *API) setDeviceCookie(w http.ResponseWriter, r *http.Request, now time.Time) {
	acct, err := account.Load(a.AccountDir)
	if err != nil {
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
	if now.Sub(a.loginWindow) > time.Minute || a.loginFailures == nil {
		a.loginFailures = map[string]int{}
		a.loginWindow = now
	}
	if failed {
		a.loginFailures[bucket]++
	}
	limit := sharedLoginLimit
	if bucket != "" {
		limit = deviceLoginLimit
	}
	return a.loginFailures[bucket] > limit
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
