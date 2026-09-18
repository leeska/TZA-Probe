package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/leeska/TZA-Probe/internal/model"
)

func TestTransportRejectsUnsafeOriginsAndRedirects(t *testing.T) {
	token := model.Secret()
	for _, endpoint := range []string{"http://example.com", "https://user:pass@example.com", "https://example.com/subpath", "https://example.com/?token=secret", "file:///tmp/server"} {
		if _, err := NewTransport(endpoint, token, false); err == nil {
			t.Fatal("accepted", endpoint)
		}
	}
	var leaked bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	transport, err := NewTransport(source.URL, token, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = transport.Jobs(context.Background()); err == nil || leaked {
		t.Fatal("redirect followed")
	}
}
