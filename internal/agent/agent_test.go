package agent

import (
	"context"
	"net"
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

func TestIPFamiliesFromInterfaceAddresses(t *testing.T) {
	addr := func(s string) net.Addr { return &net.IPNet{IP: net.ParseIP(s)} }
	cases := []struct {
		name  string
		addrs []net.Addr
		want  []string
	}{
		{"loopback and link-local only", []net.Addr{addr("127.0.0.1"), addr("fe80::1")}, []string{}},
		{"private IPv4 is still usable behind NAT", []net.Addr{addr("10.0.0.5")}, []string{"ipv4"}},
		{"global IPv6", []net.Addr{addr("2400:cb00::1")}, []string{"ipv6"}},
		{"IPv6 unique-local is not publicly routable", []net.Addr{addr("fd00::1")}, []string{}},
		{"dual stack", []net.Addr{addr("10.0.0.5"), addr("2400:cb00::1")}, []string{"ipv4", "ipv6"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ipFamilies(tc.addrs)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v want %v", got, tc.want)
				}
			}
		})
	}
}
