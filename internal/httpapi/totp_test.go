package httpapi

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/account"
	"github.com/leeska/TZA-Probe/internal/storage"
	"github.com/leeska/TZA-Probe/internal/totp"
)

func TestLoginWithTwoFactor(t *testing.T) {
	s, err := storage.Open(filepath.Join(t.TempDir(), "totp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	dir := t.TempDir()
	if err := account.Save(dir, "admin", "right-password"); err != nil {
		t.Fatal(err)
	}
	secret := totp.NewSecret()
	if err := totp.Save(dir, totp.Config{Secret: secret}); err != nil {
		t.Fatal(err)
	}
	handler := (&API{Store: s, AccountDir: dir, Assets: http.NotFoundHandler()}).Handler()
	login := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "https://probe.example/api/v1/login", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "https://probe.example")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := login(`{"username":"admin","password":"right-password"}`); w.Code != 401 || !strings.Contains(w.Body.String(), `"totp":true`) || len(w.Result().Cookies()) != 0 {
		t.Fatalf("missing code: %d %s", w.Code, w.Body)
	}
	if w := login(`{"username":"admin","password":"wrong","code":"123456"}`); w.Code != 401 || strings.Contains(w.Body.String(), "totp") {
		t.Fatalf("wrong password must not reveal the second factor: %d %s", w.Code, w.Body)
	}
	if w := login(`{"username":"admin","password":"right-password","code":"000000"}`); w.Code != 401 {
		t.Fatalf("wrong code: %d", w.Code)
	}
	code, _ := totp.Code(secret, time.Now())
	if w := login(`{"username":"admin","password":"right-password","code":"` + code + `"}`); w.Code != 204 {
		t.Fatalf("right code: %d %s", w.Code, w.Body)
	}
	if w := login(`{"username":"admin","password":"right-password","code":"` + code + `"}`); w.Code != 401 {
		t.Fatalf("replayed code: %d", w.Code)
	}
	entries, _ := s.AuditLog(t.Context(), 0, 20, "")
	actions := []string{}
	for _, e := range entries {
		actions = append(actions, e.Action)
	}
	if got := strings.Join(actions, ","); !strings.Contains(got, "login,") || strings.Count(got, "login.failed") < 2 {
		t.Fatalf("audit %s", got)
	}
}
