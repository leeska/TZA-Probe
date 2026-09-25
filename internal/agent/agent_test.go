package agent

import (
	"context"
	"errors"
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

// A family whose address exists but which the kernel cannot route must not be
// reported: otherwise the Agent advertises IPv4 on a host whose IPv4 lives on a
// dead tunnel or a userspace proxy and collects 100% failures from every
// target.
func TestRoutableFamiliesDropFamiliesWithoutARoute(t *testing.T) {
	unreachable := errors.New("dial udp4 192.0.2.1:9: connect: network is unreachable")
	cases := []struct {
		name  string
		fails map[string]bool
		have  []string
		want  []string
	}{
		{"both families routed", nil, []string{"ipv4", "ipv6"}, []string{"ipv4", "ipv6"}},
		{"address without a route is dropped", map[string]bool{"udp4": true}, []string{"ipv4", "ipv6"}, []string{"ipv6"}},
		{"nothing routable probes nothing", map[string]bool{"udp4": true, "udp6": true}, []string{"ipv4", "ipv6"}, []string{}},
		{"only the families the host has are looked up", nil, []string{"ipv6"}, []string{"ipv6"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer stubRouteLookup(func(network, address string) error {
				if tc.fails[network] {
					return unreachable
				}
				return nil
			})()
			if got := routableFamilies(tc.have); !sameFamilies(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

// The lookup must stay packet-free and destination-neutral: a connected UDP
// socket to a documentation address (RFC 5737 / RFC 3849) resolves both the
// route and the source address without ever reaching a real host.
func TestFamilyRoutableLooksUpRouteWithoutSendingPackets(t *testing.T) {
	networks := map[string]string{"ipv4": "udp4", "ipv6": "udp6"}
	blocks := map[string]string{"ipv4": "192.0.2.0/24", "ipv6": "2001:db8::/32"}
	for _, family := range []string{"ipv4", "ipv6"} {
		var network, address string
		defer stubRouteLookup(func(gotNetwork, gotAddress string) error {
			network, address = gotNetwork, gotAddress
			return nil
		})()
		if !familyRoutable(family) {
			t.Fatalf("%s: expected routable", family)
		}
		if network != networks[family] {
			t.Fatalf("%s: network %q want %q", family, network, networks[family])
		}
		_, block, err := net.ParseCIDR(blocks[family])
		if err != nil {
			t.Fatal(err)
		}
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			t.Fatalf("%s: %v", family, err)
		}
		ip := net.ParseIP(host)
		if ip == nil || !block.Contains(ip) || port != "9" {
			t.Fatalf("%s: %q is not a documentation address on the discard port", family, address)
		}
	}
}

func stubRouteLookup(replacement func(network, address string) error) func() {
	original := routeLookup
	routeLookup = replacement
	return func() { routeLookup = original }
}

func sameFamilies(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
