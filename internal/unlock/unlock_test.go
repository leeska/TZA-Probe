package unlock

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

// runCheck runs one catalogue check against a client whose hosts point at the
// given test servers.
func runCheck(t *testing.T, id string, hosts map[string]string) model.UnlockResult {
	t.Helper()
	c := clientFor("", DefaultUserAgent, 3*time.Second, hosts)
	if _, ok := checks[id]; !ok {
		t.Fatalf("no check for %q", id)
	}
	return runOne(context.Background(), c, id, 3*time.Second)
}

func server(t *testing.T, h http.HandlerFunc) (string, func()) {
	t.Helper()
	s := httptest.NewServer(h)
	return s.URL, s.Close
}

// TestCatalogCoverage pins checks to the model catalogue: every service has an
// implementation and no implementation is orphaned.
func TestCatalogCoverage(t *testing.T) {
	ids := model.UnlockServiceIDs()
	if len(ids) != 24 {
		t.Fatalf("catalogue has %d services, want 24", len(ids))
	}
	for _, id := range ids {
		if _, ok := checks[id]; !ok {
			t.Errorf("service %q has no check implementation", id)
		}
	}
	known := map[string]bool{}
	for _, id := range ids {
		known[id] = true
	}
	for id := range checks {
		if !known[id] {
			t.Errorf("check %q is not in the catalogue", id)
		}
	}
}

// TestRunOrder verifies Run returns one result per requested service, in
// catalogue order, with valid verdicts even when every host answers a generic
// page.
func TestRunOrder(t *testing.T) {
	url, done := server(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html></html>"))
	})
	defer done()

	hosts := map[string]string{}
	for _, h := range []string{
		"www.netflix.com", "api.fast.com", "www.disneyplus.com", "www.youtube.com",
		"redirector.googlevideo.com", "www.primevideo.com", "uapisfm.tvbanywhere.com.sg",
		"www.iq.com", "startup.core.indazn.com", "chat.openai.com", "gemini.google.com",
		"claude.ai", "api.binance.com", "www.binance.com", "api.bybit.com", "www.bybit.com",
		"www.okx.com", "www.bing.com",
		"gspe1-ssl.ls.apple.com", "play.google.com", "www.google.com", "zh.wikipedia.org",
		"www.reddit.com", "store.steampowered.com", "geolocation.onetrust.com",
		"api.gl.eternal.channel.or.jp",
	} {
		hosts[h] = url
	}

	got := Run(context.Background(), Options{Concurrency: 8, Timeout: 3 * time.Second, hosts: hosts})
	want := model.UnlockServiceIDs()
	if len(got) != len(want) {
		t.Fatalf("got %d results, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].Service != id {
			t.Errorf("result %d: service %q, want %q", i, got[i].Service, id)
		}
		if !model.ValidUnlockStatus(got[i].Status) {
			t.Errorf("result %d (%s): invalid status %q", i, id, got[i].Status)
		}
		if len(got[i].Region) > maxRegion || len(got[i].Detail) > maxDetail {
			t.Errorf("result %d (%s): region/detail over cap", i, id)
		}
	}
	// The whole slice must satisfy the Server's wire contract.
	if err := (model.Result{Status: "ok", Unlocks: got}).Validate(); err != nil {
		t.Errorf("results do not validate against model.Result: %v", err)
	}
}

func TestNetflixStates(t *testing.T) {
	newServer := func(c812, c701 int, location string) (string, func()) {
		return server(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/title/81280792":
				w.WriteHeader(c812)
			case "/title/70143836":
				w.WriteHeader(c701)
			case "/title/80018499":
				if location != "" {
					w.Header().Set("Location", location)
				}
				w.WriteHeader(http.StatusOK)
			default:
				w.WriteHeader(http.StatusOK)
			}
		})
	}

	cases := []struct {
		name       string
		c812, c701 int
		location   string
		want       string
		region     string
	}{
		{"originals", 404, 404, "", model.UnlockOriginalsOnly, ""},
		{"banned", 403, 403, "", model.UnlockBanned, ""},
		{"region", 200, 200, "https://www.netflix.com/jp/title/80018499", model.UnlockYes, "JP"},
		{"default", 200, 200, "", model.UnlockYes, "US"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url, done := newServer(tc.c812, tc.c701, tc.location)
			defer done()
			got := runCheck(t, "netflix", map[string]string{"www.netflix.com": url})
			if got.Status != tc.want {
				t.Fatalf("status %q, want %q (detail %q)", got.Status, tc.want, got.Detail)
			}
			if got.Region != tc.region {
				t.Errorf("region %q, want %q", got.Region, tc.region)
			}
		})
	}
}

func TestOpenAIStates(t *testing.T) {
	newServer := func(rootCode int, rootBody, loc string) (string, func()) {
		return server(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/cdn-cgi/trace" {
				_, _ = fmt.Fprintf(w, "fl=abc\nloc=%s\n", loc)
				return
			}
			w.WriteHeader(rootCode)
			_, _ = w.Write([]byte(rootBody))
		})
	}

	cases := []struct {
		name   string
		code   int
		body   string
		loc    string
		want   string
		region string
		detail string
	}{
		{"vpn", 403, "blocked", "US", model.UnlockBanned, "", "VPN Blocked"},
		{"rate", 429, "{}", "US", model.UnlockOriginalsOnly, "US", "429 Rate limit"},
		{"supported", 200, "<html>ok</html>", "US", model.UnlockYes, "US", ""},
		{"unsupported", 200, "<html>ok</html>", "CN", model.UnlockNo, "CN", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url, done := newServer(tc.code, tc.body, tc.loc)
			defer done()
			got := runCheck(t, "openai", map[string]string{"chat.openai.com": url})
			if got.Status != tc.want {
				t.Fatalf("status %q, want %q", got.Status, tc.want)
			}
			if got.Region != tc.region {
				t.Errorf("region %q, want %q", got.Region, tc.region)
			}
			if got.Detail != tc.detail {
				t.Errorf("detail %q, want %q", got.Detail, tc.detail)
			}
		})
	}
}

func TestRedditStates(t *testing.T) {
	for _, tc := range []struct {
		code int
		want string
	}{{403, model.UnlockNo}, {200, model.UnlockYes}} {
		url, done := server(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.code) })
		got := runCheck(t, "reddit", map[string]string{"www.reddit.com": url})
		done()
		if got.Status != tc.want {
			t.Errorf("reddit %d: status %q, want %q", tc.code, got.Status, tc.want)
		}
	}
}

func TestBinanceStates(t *testing.T) {
	for _, tc := range []struct {
		code   int
		want   string
		detail string
	}{
		{451, model.UnlockNo, "HTTP 451"},
		{200, model.UnlockYes, ""},
		{202, model.UnlockYes, ""},
		{500, model.UnlockFailed, "HTTP 500"},
	} {
		url, done := server(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.code) })
		got := runCheck(t, "binance", map[string]string{"api.binance.com": url, "www.binance.com": url})
		done()
		if got.Status != tc.want {
			t.Errorf("binance %d: status %q, want %q", tc.code, got.Status, tc.want)
		}
		if got.Detail != tc.detail {
			t.Errorf("binance %d: detail %q, want %q", tc.code, got.Detail, tc.detail)
		}
	}
}

func TestNetflixCDNAndPrimeVideo(t *testing.T) {
	// netflix_cdn prefers the egress's own client.location over the CDN node.
	cdn, cdnDone := server(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"client":{"location":{"city":"Tampa","country":"US"}},` +
			`"targets":[{"location":{"city":"Warsaw","country":"PL"}}]}`))
	})
	got := runCheck(t, "netflix_cdn", map[string]string{"api.fast.com": cdn})
	cdnDone()
	if got.Status != model.UnlockYes || got.Region != "Tampa, US" {
		t.Errorf("netflix_cdn: got %q/%q, want yes/Tampa, US", got.Status, got.Region)
	}

	// netflix_cdn is banned on a 403.
	banned, bannedDone := server(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	got = runCheck(t, "netflix_cdn", map[string]string{"api.fast.com": banned})
	bannedDone()
	if got.Status != model.UnlockBanned {
		t.Errorf("netflix_cdn 403: got %q, want banned", got.Status)
	}

	// prime_video follows the redirect to the locale page.
	pv, pvDone := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/us", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(`<script>var x={"currentTerritory":"US"};</script>`))
	})
	got = runCheck(t, "prime_video", map[string]string{"www.primevideo.com": pv})
	pvDone()
	if got.Status != model.UnlockYes || got.Region != "US" {
		t.Errorf("prime_video: got %q/%q, want yes/US", got.Status, got.Region)
	}
}

func TestAppleRegion(t *testing.T) {
	url, done := server(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("jp\n")) })
	defer done()
	got := runCheck(t, "apple", map[string]string{"gspe1-ssl.ls.apple.com": url})
	if got.Status != model.UnlockYes || got.Region != "JP" {
		t.Fatalf("apple: got %q/%q, want yes/JP", got.Status, got.Region)
	}
}

// TestTimeoutIsFailed proves a per-service timeout becomes "failed", never no or
// banned.
func TestTimeoutIsFailed(t *testing.T) {
	url, done := server(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	})
	defer done()
	c := clientFor("", DefaultUserAgent, time.Second, map[string]string{"www.reddit.com": url})
	got := runOne(context.Background(), c, "reddit", 50*time.Millisecond)
	if got.Status != model.UnlockFailed {
		t.Fatalf("status %q, want failed", got.Status)
	}
	if got.Detail != "timeout" {
		t.Errorf("detail %q, want timeout", got.Detail)
	}
}

// TestConnectFailureIsFailed uses a closed port: a refused connection is a
// failed check, not a verdict about the IP.
func TestConnectFailureIsFailed(t *testing.T) {
	hosts := map[string]string{"api.binance.com": "http://127.0.0.1:9", "www.binance.com": "http://127.0.0.1:9"}
	got := runCheck(t, "binance", hosts)
	if got.Status != model.UnlockFailed {
		t.Fatalf("status %q, want failed", got.Status)
	}
}

// TestFamilyControl checks the dialer pin directly: an ipv6-pinned round refuses
// tcp4 connects and vice versa.
func TestFamilyControl(t *testing.T) {
	v6 := familyControl("ipv6")
	if err := v6("tcp4", "1.2.3.4:443", nil); err == nil {
		t.Error("ipv6 pin allowed a tcp4 connect")
	}
	if err := v6("tcp6", "[::1]:443", nil); err != nil {
		t.Errorf("ipv6 pin refused a tcp6 connect: %v", err)
	}
	v4 := familyControl("ipv4")
	if err := v4("tcp6", "[::1]:443", nil); err == nil {
		t.Error("ipv4 pin allowed a tcp6 connect")
	}
	if err := v4("tcp4", "1.2.3.4:443", nil); err != nil {
		t.Errorf("ipv4 pin refused a tcp4 connect: %v", err)
	}
	if familyControl("") != nil {
		t.Error("auto family should not install a control hook")
	}
}

// TestIPv6PinCannotReachV4Server proves the pin is enforced end to end: an
// ipv6-pinned client cannot talk to an IPv4-only httptest server.
func TestIPv6PinCannotReachV4Server(t *testing.T) {
	url, done := server(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("US\n")) })
	defer done()
	c := clientFor("ipv6", DefaultUserAgent, time.Second, map[string]string{"gspe1-ssl.ls.apple.com": url})
	got := runOne(context.Background(), c, "apple", time.Second)
	if got.Status != model.UnlockFailed {
		t.Fatalf("status %q, want failed (ipv6 pin must not reach a v4 server)", got.Status)
	}
}

// TestRunCanceled proves cancelling the round short-circuits every service.
func TestRunCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := Run(ctx, Options{Services: []string{"netflix", "apple"}})
	if len(got) != 2 {
		t.Fatalf("got %d results, want 2", len(got))
	}
	for _, r := range got {
		if r.Status != model.UnlockFailed {
			t.Errorf("%s: status %q, want failed", r.Service, r.Status)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("got %q", got)
	}
	if got := truncate(strings.Repeat("a", 100), 40); len(got) != 40 {
		t.Errorf("ascii truncate len %d, want 40", len(got))
	}
	// Never split a multi-byte rune.
	s := strings.Repeat("é", 40) // 80 bytes
	got := truncate(s, 41)
	if len(got) > 41 {
		t.Errorf("rune truncate len %d > 41", len(got))
	}
	if !strings.HasPrefix(s, got) {
		t.Errorf("rune truncate is not a prefix")
	}
}

// TestExchangeChecksAskTheApi pins the fix for the two exchange rows: the
// marketing sites answer 202/200 everywhere, only the APIs answer 451 / 403 for
// a blocked country, which is the verdict the panel carries.
func TestExchangeChecksAskTheApi(t *testing.T) {
	cases := []struct {
		service, host, path string
		blocked             int
		want                string
		wantDetail          string
	}{
		{"binance", "api.binance.com", "/api/v3/ping", http.StatusUnavailableForLegalReasons, model.UnlockNo, "HTTP 451"},
		{"bybit", "api.bybit.com", "/v5/market/time", http.StatusForbidden, model.UnlockNo, "CDN 按国家阻断"},
	}
	for _, c := range cases {
		blocked, done := server(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == c.path {
				w.WriteHeader(c.blocked)
				return
			}
			w.WriteHeader(http.StatusOK)
		})
		got := runCheck(t, c.service, map[string]string{c.host: blocked})
		done()
		if got.Status != c.want || got.Detail != c.wantDetail {
			t.Errorf("%s blocked: %+v", c.service, got)
		}
		open, doneOpen := server(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
		got = runCheck(t, c.service, map[string]string{c.host: open})
		doneOpen()
		if got.Status != model.UnlockYes {
			t.Errorf("%s open: %+v", c.service, got)
		}
	}
	// 网页站被拦、API 正常时仍然算解锁：网页站的 202 挑战页本来就不能当结论。
	page, donePage := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "" || strings.HasPrefix(r.URL.Path, "/api/") {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
	got := runCheck(t, "binance", map[string]string{"api.binance.com": page, "www.binance.com": page})
	donePage()
	if got.Status != model.UnlockYes {
		t.Errorf("challenged marketing page: %+v", got)
	}
}

// TestYoutubeCDNReadsTheAirportCodeInTheMiddle reproduces the two hostnames the
// live nodes answered with.
func TestYoutubeCDNReadsTheAirportCodeInTheMiddle(t *testing.T) {
	for _, c := range []struct{ line, want string }{
		{"2a12:a303:11e:9f::a => lcnrtb-bg (2a12:a303:11e::/48) [u]", "Tokyo, Japan"},
		{"2607:8700:5500:39f1::2 => lax31s13 (2607:8700:5500::/48)", "Los Angeles, United States"},
		{"1.2.3.4 => unknownhost", ""},
	} {
		url, done := server(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(c.line))
		})
		got := runCheck(t, "youtube_cdn", map[string]string{"redirector.googlevideo.com": url})
		done()
		if c.want == "" {
			if got.Status != model.UnlockFailed {
				t.Errorf("%q: %+v, want failed", c.line, got)
			}
			continue
		}
		if got.Status != model.UnlockYes || got.Region != c.want {
			t.Errorf("%q: %+v, want %s", c.line, got, c.want)
		}
	}
}

// TestBingKeepsTheRegionClean: the risky marker is a detail, not a region name.
func TestBingKeepsTheRegionClean(t *testing.T) {
	url, done := server(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html>sj_cook.set("SRCHHPGUSR","HV" Region:"JP"</html>`))
	})
	got := runCheck(t, "bing", map[string]string{"www.bing.com": url})
	done()
	if got.Status != model.UnlockYes || got.Region != "JP" || got.Detail != "risky" {
		t.Fatalf("bing: %+v", got)
	}
}
