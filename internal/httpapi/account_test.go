package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/account"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/storage"
	"github.com/leeska/TZA-Probe/web"
)

// The panel account logs in by username and password, can be changed with the
// current password, and a change signs every other browser out. A terminal
// reset (account.Save behind the server's back) applies on the next login.
func TestAccountLoginAndChange(t *testing.T) {
	dir := t.TempDir()
	s, err := storage.Open(filepath.Join(dir, "account.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := account.Save(dir, "admin", "first-password"); err != nil {
		t.Fatal(err)
	}
	token := model.Secret()
	handler := (&API{Store: s, AdminToken: token, AccountDir: dir, Assets: web.Handler()}).Handler()
	call := func(method, path string, cookie *http.Cookie, body any) (*httptest.ResponseRecorder, *http.Cookie) {
		data, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "https://probe.example"+path, strings.NewReader(string(data)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "https://probe.example")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		var got *http.Cookie
		for _, c := range w.Result().Cookies() {
			if c.Name == "tza_session" {
				got = c
			}
		}
		return w, got
	}
	login := func(user, pass string) (int, *http.Cookie) {
		w, c := call("POST", "/api/v1/login", nil, map[string]string{"username": user, "password": pass})
		return w.Code, c
	}
	if code, _ := login("admin", "wrong-password"); code != 401 {
		t.Fatalf("wrong password: %d", code)
	}
	if code, _ := login("root", "first-password"); code != 401 {
		t.Fatalf("wrong username: %d", code)
	}
	code, mine := login("admin", "first-password")
	if code != 204 || mine == nil {
		t.Fatalf("right password: %d", code)
	}
	_, other := login("admin", "first-password")

	if w, _ := call("GET", "/api/v1/admin/account", mine, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"username":"admin"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if w, _ := call("PUT", "/api/v1/admin/account", mine, map[string]string{"current_password": "nope-nope", "username": "ops", "password": "second-password"}); w.Code != 400 {
		t.Fatalf("wrong current password accepted: %d", w.Code)
	}
	if w, _ := call("PUT", "/api/v1/admin/account", mine, map[string]string{"current_password": "first-password", "username": "ops", "password": "short"}); w.Code != 400 {
		t.Fatalf("short password accepted: %d", w.Code)
	}
	if w, _ := call("PUT", "/api/v1/admin/account", nil, map[string]string{"current_password": "first-password", "username": "ops", "password": "second-password"}); w.Code != 401 {
		t.Fatalf("anonymous change: %d", w.Code)
	}
	if w, _ := call("PUT", "/api/v1/admin/account", mine, map[string]string{"current_password": "first-password", "username": "ops", "password": "second-password"}); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w, _ := call("GET", "/api/v1/session", mine, nil); w.Code != 200 {
		t.Fatalf("the changing session was signed out: %d", w.Code)
	}
	if w, _ := call("GET", "/api/v1/session", other, nil); w.Code != 401 {
		t.Fatalf("other session survived the change: %d", w.Code)
	}
	if code, _ := login("admin", "first-password"); code != 401 {
		t.Fatalf("old credentials still work: %d", code)
	}
	if code, _ := login("ops", "second-password"); code != 204 {
		t.Fatalf("new credentials: %d", code)
	}
	// 终端重置：直接改文件，运行中的服务下次登录就认新密码。
	if err := account.Save(dir, "ops", "reset-password"); err != nil {
		t.Fatal(err)
	}
	if w, _ := call("GET", "/api/v1/session", mine, nil); w.Code != 401 {
		t.Fatalf("browser session survived terminal password reset: %d", w.Code)
	}
	if code, _ := login("ops", "reset-password"); code != 204 {
		t.Fatalf("terminal reset not picked up: %d", code)
	}
	// 管理密钥登录已移除，不得签发会话。
	if w, _ := call("POST", "/api/v1/login", nil, map[string]string{"token": token}); w.Code != 400 {
		t.Fatalf("key login: %d", w.Code)
	}
}

// Correct and incorrect passwords are both refused before verification when
// the attempt budget is full; the next refill permits a normal login again.
func TestPasswordLoginRateLimit(t *testing.T) {
	dir := t.TempDir()
	s, err := storage.Open(filepath.Join(dir, "limit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := account.Save(dir, "admin", "right-password"); err != nil {
		t.Fatal(err)
	}
	api := &API{Store: s, AdminToken: model.Secret(), AccountDir: dir, Assets: web.Handler()}
	handler := api.Handler()
	login := func(password string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "https://probe.example/api/v1/login", strings.NewReader(`{"username":"admin","password":"`+password+`"}`))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	api.loginNext = map[string]time.Time{"": time.Now().Add(time.Minute)}
	for _, password := range []string{"wrong-password", "right-password"} {
		w := login(password)
		if w.Code != 429 || w.Header().Get("Retry-After") == "" || len(w.Result().Cookies()) != 0 {
			t.Fatalf("exhausted budget: status %d, retry %q, cookies %d", w.Code, w.Header().Get("Retry-After"), len(w.Result().Cookies()))
		}
	}
	api.mu.Lock()
	api.loginNext[""] = time.Now().Add(time.Minute - 2*time.Second)
	api.mu.Unlock()
	if w := login("right-password"); w.Code != 204 {
		t.Fatalf("login after refill: %d", w.Code)
	}
}

// Trusted devices have a separate budget; forged cookies and cookies signed
// for the old password use the shared budget and cannot bypass throttling.
func TestDeviceCookieSurvivesSharedLimit(t *testing.T) {
	dir := t.TempDir()
	s, err := storage.Open(filepath.Join(dir, "device.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := account.Save(dir, "admin", "right-password"); err != nil {
		t.Fatal(err)
	}
	api := &API{Store: s, AdminToken: model.Secret(), AccountDir: dir, Assets: web.Handler()}
	handler := api.Handler()
	login := func(password string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "https://probe.example/api/v1/login", strings.NewReader(`{"username":"admin","password":"`+password+`"}`))
		r.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	exhaust := func(bucket string) {
		api.mu.Lock()
		api.loginNext[bucket] = time.Now().Add(time.Minute)
		api.mu.Unlock()
	}
	deviceFrom := func(w *httptest.ResponseRecorder) *http.Cookie {
		t.Helper()
		if w.Code != 204 {
			t.Fatalf("device login: %d", w.Code)
		}
		for _, c := range w.Result().Cookies() {
			if c.Name == deviceCookie {
				if !c.HttpOnly || c.Path != "/api/v1/" {
					t.Fatalf("device cookie: %+v", c)
				}
				return c
			}
		}
		t.Fatal("no device cookie")
		return nil
	}
	device := deviceFrom(login("right-password", nil))
	exhaust("")
	for _, password := range []string{"wrong-password", "right-password"} {
		if w := login(password, nil); w.Code != 429 {
			t.Fatalf("anonymous login while shared budget is full: %d", w.Code)
		}
	}
	exhaust("")
	if w := login("right-password", device); w.Code != 204 {
		t.Fatalf("trusted device affected by shared budget: %d", w.Code)
	}
	forged := &http.Cookie{Name: deviceCookie, Value: strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10) + ".forged"}
	exhaust("")
	if w := login("right-password", forged); w.Code != 429 {
		t.Fatalf("forged device bypassed budget: %d", w.Code)
	}
	if err := account.Save(dir, "admin", "another-password"); err != nil {
		t.Fatal(err)
	}
	exhaust("")
	if w := login("another-password", device); w.Code != 429 {
		t.Fatalf("old-password device bypassed budget: %d", w.Code)
	}
	api.mu.Lock()
	api.loginNext[""] = time.Now().Add(-time.Minute)
	api.mu.Unlock()
	device = deviceFrom(login("another-password", nil))
	probe := httptest.NewRequest("POST", "https://probe.example/api/v1/login", nil)
	probe.AddCookie(device)
	bucket := api.loginBucket(probe, time.Now())
	if bucket == "" {
		t.Fatal("trusted device not recognized")
	}
	api.mu.Lock()
	api.loginNext[bucket] = time.Now().Add(time.Minute)
	api.mu.Unlock()
	for _, password := range []string{"wrong-password", "another-password"} {
		if w := login(password, device); w.Code != 429 || w.Header().Get("Retry-After") == "" {
			t.Fatalf("device exceeded its own budget: %d", w.Code)
		}
	}
	if w := login("another-password", nil); w.Code != 204 {
		t.Fatalf("device budget affected shared budget: %d", w.Code)
	}
}

// A login that verified the old password just before a terminal reset must
// not create a session for the new password version when its reply is delayed.
func TestDelayedLoginCannotSurvivePasswordReset(t *testing.T) {
	dir := t.TempDir()
	if err := account.Save(dir, "admin", "first-password"); err != nil {
		t.Fatal(err)
	}
	a := &API{AccountDir: dir, Assets: web.Handler()}
	h := a.Handler()
	verifiedHash := a.checkAccount("admin", "first-password")
	if verifiedHash == "" {
		t.Fatal("credentials were not verified")
	}
	if err := account.Save(dir, "admin", "reset-password"); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	a.startSession(w, httptest.NewRequest("POST", "https://probe.example/api/v1/login", nil), time.Now(), verifiedHash)
	r := httptest.NewRequest("GET", "https://probe.example/api/v1/session", nil)
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == deviceCookie {
			t.Fatal("stale login issued a device credential for the reset account")
		}
		if cookie.Name == "tza_session" {
			r.AddCookie(cookie)
		}
	}
	result := httptest.NewRecorder()
	h.ServeHTTP(result, r)
	if result.Code != 401 {
		t.Fatalf("delayed login survived reset: %d", result.Code)
	}
}
