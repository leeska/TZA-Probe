package market

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/market/markettest"
)

type bindingTransport func(*http.Request) (*http.Response, error)

func (f bindingTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDisconnectPreservesReplacementBinding(t *testing.T) {
	e := newEnv(t)
	old := instance{URL: e.client.URL(), ID: "ins_old", Token: markettest.Token, Account: "old"}
	if err := e.client.save(old); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	transport := e.client.api.Transport
	e.client.api.Transport = bindingTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodDelete && r.URL.Path == "/api/v1/instance" {
			if got := r.Header.Get("Authorization"); got != "Bearer "+old.Token {
				t.Errorf("revoked wrong binding: %q", got)
			}
			close(started)
			select {
			case <-release:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		return transport.RoundTrip(r)
	})
	done := make(chan error, 1)
	go func() { _, err := e.client.Disconnect(context.Background()); done <- err }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("disconnect did not reach CC")
	}
	// Poll can save a newly approved account while the previous account's
	// network revocation is still in flight.
	replacement := instance{URL: e.client.URL(), ID: "ins_new", Token: "replacement-token", Account: "new"}
	e.client.mu.Lock()
	err := e.client.save(replacement)
	e.client.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("disconnect stuck")
	}
	if got := e.client.Status(); !got.Bound || got.InstanceID != replacement.ID {
		t.Fatalf("old disconnect erased new binding: %+v", got)
	}
}
