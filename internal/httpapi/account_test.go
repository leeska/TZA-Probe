package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
		if c := w.Result().Cookies(); len(c) == 1 {
			got = c[0]
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
	if code, _ := login("ops", "reset-password"); code != 204 {
		t.Fatalf("terminal reset not picked up: %d", code)
	}
	// 密钥登录照常可用。
	if w, _ := call("POST", "/api/v1/login", nil, map[string]string{"token": token}); w.Code != 204 {
		t.Fatalf("key login: %d", w.Code)
	}
}

// Past the limit a password login is refused even when right: for passwords
// the limit is what stops guessing.
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
	// 预先记满失败次数，省得在测试里真算 30 次 PBKDF2。
	for i := 0; i < 30; i++ {
		api.loginLimited(time.Now(), true)
	}
	r := httptest.NewRequest("POST", "https://probe.example/api/v1/login", strings.NewReader(`{"username":"admin","password":"wrong-password"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 429 {
		t.Fatalf("31st wrong password: %d", w.Code)
	}
	r = httptest.NewRequest("POST", "https://probe.example/api/v1/login", strings.NewReader(`{"username":"admin","password":"right-password"}`))
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 429 {
		t.Fatalf("password login while limited: %d", w.Code)
	}
}
