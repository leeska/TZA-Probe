package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leeska/TZA-ControlCenter/internal/account"
	"github.com/leeska/TZA-ControlCenter/internal/model"
	"github.com/leeska/TZA-ControlCenter/internal/storage"
	"github.com/leeska/TZA-ControlCenter/web"
)

// 部署重启主控后后台不用重新登录：会话存在数据目录里（只存令牌的哈希，0600），新进程读回；
// 登出和改密码同样跨重启生效。
func TestSessionsSurviveRestart(t *testing.T) {
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
	start := func() http.Handler {
		return (&API{Store: s, AdminToken: token, AccountDir: dir, Assets: web.Handler()}).Handler()
	}
	call := func(h http.Handler, method, path string, cookie *http.Cookie, body any) (*httptest.ResponseRecorder, *http.Cookie) {
		data, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "https://probe.example"+path, strings.NewReader(string(data)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "https://probe.example")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		for _, c := range w.Result().Cookies() {
			if c.Name == "tza_session" {
				return w, c
			}
		}
		return w, nil
	}
	h := start()
	_, a := call(h, "POST", "/api/v1/login", nil, map[string]string{"username": "admin", "password": "first-password"})
	_, b := call(h, "POST", "/api/v1/login", nil, map[string]string{"username": "admin", "password": "first-password"})
	if a == nil || b == nil {
		t.Fatal("login")
	}
	info, err := os.Stat(filepath.Join(dir, sessionsFile))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("会话文件要 0600", err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, sessionsFile))
	if strings.Contains(string(raw), a.Value) || !strings.Contains(string(raw), model.Hash(a.Value)) {
		t.Fatal("文件里只能有令牌的哈希")
	}

	h = start() // 重启
	if w, _ := call(h, "GET", "/api/v1/session", a, nil); w.Code != 200 {
		t.Fatal("重启后会话要还在", w.Code)
	}
	call(h, "POST", "/api/v1/logout", b, nil)
	h = start()
	if w, _ := call(h, "GET", "/api/v1/session", b, nil); w.Code != 401 {
		t.Fatal("登出要跨重启生效", w.Code)
	}
	if w, _ := call(h, "GET", "/api/v1/session", a, nil); w.Code != 200 {
		t.Fatal("别的会话不受影响", w.Code)
	}
	// 终端里重置密码（账号哈希变了）后，重启也不能让旧会话复活。
	account.Save(dir, "admin", "second-password")
	h = start()
	if w, _ := call(h, "GET", "/api/v1/session", a, nil); w.Code != 401 {
		t.Fatal("改了密码旧会话要失效", w.Code)
	}
	// 损坏的文件不挡启动。
	os.WriteFile(filepath.Join(dir, sessionsFile), []byte("{oops"), 0600)
	h = start()
	if w, _ := call(h, "GET", "/healthz", nil, nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
}
