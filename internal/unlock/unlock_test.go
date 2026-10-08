package unlock

import (
	"context"
	"fmt"
	"io"
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
	// A 200 page is either the title or the "not available in your country"
	// notice — the status code cannot tell them apart, so the fixtures serve
	// bodies and the check has to read them.
	const notice = `<div>Oh no! This title currently isn’t available to watch in your country.</div>`
	const ww = `"requestCountry":{"supportedLocales":["en"],"id":"WW","countryName":"Nowhere"}`
	regionBody := func(cc string) string {
		return `"requestCountry":{"supportedLocales":["en"],"id":"` + cc + `","countryName":"X"}`
	}
	newServer := func(c812, c701 int, b812, b701 string) (string, func()) {
		return server(t, func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasPrefix(r.URL.Path, "/title/81280792"):
				w.WriteHeader(c812)
				_, _ = io.WriteString(w, b812)
			case strings.HasPrefix(r.URL.Path, "/title/70143836"):
				w.WriteHeader(c701)
				_, _ = io.WriteString(w, b701)
			case r.URL.Path == "/de-en/title/81280792":
				// region-redirect landing page without the region block
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, "<html>LEGO Ninjago</html>")
			default:
				w.WriteHeader(http.StatusOK)
			}
		})
	}

	cases := []struct {
		name       string
		c812, c701 int
		b812, b701 string
		want       string
		region     string
	}{
		{"notice means originals only", 200, 200, notice, notice, model.UnlockOriginalsOnly, ""},
		{"both titles gone means originals only", 404, 404, "", "", model.UnlockOriginalsOnly, ""},
		{"both blocked", 403, 403, "", "", model.UnlockBanned, ""},
		{"one withheld title still counts as a region", 200, 200, ww, notice, model.UnlockYes, "WW"},
		{"region comes from the page", 200, 200, regionBody("JP"), regionBody("JP"), model.UnlockYes, "JP"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url, done := newServer(tc.c812, tc.c701, tc.b812, tc.b701)
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

	// The region survives on the URL alone when the body has no region block:
	// the title 301s to its per-region path, which is what the panel shows for
	// a full catalogue.
	t.Run("region from the redirect path", func(t *testing.T) {
		url, done := server(t, func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/title/81280792") {
				http.Redirect(w, r, "http://"+r.Host+"/de-en/title/81280792", http.StatusMovedPermanently)
				return
			}
			if strings.HasPrefix(r.URL.Path, "/title/70143836") {
				http.Redirect(w, r, "http://"+r.Host+"/de-en/title/70143836", http.StatusMovedPermanently)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "<html>LEGO Ninjago</html>")
		})
		defer done()
		got := runCheck(t, "netflix", map[string]string{"www.netflix.com": url})
		if got.Status != model.UnlockYes || got.Region != "DE" {
			t.Fatalf("got %q/%q, want yes/DE", got.Status, got.Region)
		}
	})
}

func TestGeminiRegion(t *testing.T) {
	newServer := func(page string) (string, func()) {
		return server(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, page)
		})
	}
	// The `45631641` flag some checks grep is not a country gate: it reads
	// false on egresses a browser still gets the whole app from, so a page
	// carrying it must not be turned into "no".
	cases := []struct {
		name   string
		page   string
		want   string
		region string
	}{
		{"region triple", `x,2,1,200,"USA"y`, model.UnlockYes, "USA"},
		{"flag false is not a verdict", `,2,1,200,"DEU"y\"45631641\":false`, model.UnlockYes, "DEU"},
		{"flag true", `,2,1,200,"JPN"y\"45631641\":true`, model.UnlockYes, "JPN"},
		{"old marker without a region", `45631641,null,true`, model.UnlockYes, ""},
		{"neither", `<html>nothing</html>`, model.UnlockNo, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url, done := newServer(tc.page)
			defer done()
			got := runCheck(t, "gemini", map[string]string{"gemini.google.com": url})
			if got.Status != tc.want || got.Region != tc.region {
				t.Fatalf("got %q/%q, want %q/%q", got.Status, got.Region, tc.want, tc.region)
			}
		})
	}
}

func TestDisneyPlusDeviceFlow(t *testing.T) {
	// One server stands in for the whole bamgrid host: the devices call hands
	// out an assertion, the token call either refuses the IP or hands out a
	// refresh token, and the device query names the catalogue.
	newServer := func(tokenBody, gqlBody string, gqlCode int, preview string) (string, func()) {
		return server(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/devices":
				_, _ = io.WriteString(w, `{"assertion":"assert-1"}`)
			case "/token":
				_, _ = io.WriteString(w, tokenBody)
			case "/graph/v1/device/graphql":
				w.WriteHeader(gqlCode)
				_, _ = io.WriteString(w, gqlBody)
			default: // the preview landing page
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, preview)
			}
		})
	}
	run := func(url string) model.UnlockResult {
		return runCheck(t, "disneyplus", map[string]string{
			"disney.api.edge.bamgrid.com": url,
			"disneyplus.com":              url,
		})
	}

	t.Run("blocked egress", func(t *testing.T) {
		url, done := newServer(`{"error":"unauthorized_client","error_description":"forbidden-location"}`, "", 200, "")
		defer done()
		got := run(url)
		if got.Status != model.UnlockBanned {
			t.Fatalf("got %q, want banned (detail %q)", got.Status, got.Detail)
		}
	})
	t.Run("region from the device session", func(t *testing.T) {
		url, done := newServer(`{"refresh_token":"rt-1"}`, `{"countryCode":"HK","inSupportedLocation":true}`, 200, "<html>hi</html>")
		defer done()
		got := run(url)
		if got.Status != model.UnlockYes || got.Region != "HK" {
			t.Fatalf("got %q/%q, want yes/HK", got.Status, got.Region)
		}
	})
	t.Run("announced but not launched", func(t *testing.T) {
		url, done := newServer(`{"refresh_token":"rt-1"}`, `{"countryCode":"DE","inSupportedLocation":false}`, 200, "<html>hi</html>")
		defer done()
		got := run(url)
		if got.Status != model.UnlockNo {
			t.Fatalf("got %q, want no (detail %q)", got.Status, got.Detail)
		}
	})
	t.Run("no assertion is a failure, never a verdict", func(t *testing.T) {
		url, done := server(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/devices" {
				_, _ = io.WriteString(w, `{}`)
				return
			}
			_, _ = io.WriteString(w, `{"countryCode":"US","inSupportedLocation":true}`)
		})
		defer done()
		got := run(url)
		if got.Status != model.UnlockFailed {
			t.Fatalf("got %q, want failed", got.Status)
		}
	})
	t.Run("preview landing page means no", func(t *testing.T) {
		url, done := server(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/devices":
				_, _ = io.WriteString(w, `{"assertion":"assert-1"}`)
			case "/token":
				_, _ = io.WriteString(w, `{"refresh_token":"rt-1"}`)
			case "/graph/v1/device/graphql":
				_, _ = io.WriteString(w, `{"countryCode":"US","inSupportedLocation":true}`)
			case "/unavailable/":
				_, _ = io.WriteString(w, "<html>not here</html>")
			default:
				http.Redirect(w, r, "http://"+r.Host+"/unavailable/", http.StatusFound)
			}
		})
		defer done()
		got := run(url)
		if got.Status != model.UnlockNo {
			t.Fatalf("got %q, want no (detail %q)", got.Status, got.Detail)
		}
	})
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

func TestClaudeStates(t *testing.T) {
	for _, tc := range []struct {
		name   string
		code   int
		loc    string
		want   string
		detail string
	}{
		{"root", 200, "", model.UnlockYes, ""},
		{"sign-in page", 200, "/login?returnTo=%2Fnew", model.UnlockYes, ""},
		{"region block", 200, "/app-unavailable-in-region", model.UnlockNo, ""},
		{"cloudflare 403", 403, "", model.UnlockFailed, "HTTP 403"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			url, done := server(t, func(w http.ResponseWriter, r *http.Request) {
				if tc.loc != "" && r.URL.Path == "/" {
					http.Redirect(w, r, tc.loc, http.StatusFound)
					return
				}
				w.WriteHeader(tc.code)
			})
			got := runCheck(t, "claude", map[string]string{"claude.ai": url})
			done()
			if got.Status != tc.want || got.Detail != tc.detail {
				t.Errorf("claude %s: got %q/%q, want %q/%q", tc.name, got.Status, got.Detail, tc.want, tc.detail)
			}
		})
	}
}

func TestTruncatedBodyIsNotAVerdict(t *testing.T) {
	// A body cut short must not read as "no": the markers live deep in these
	// documents (Prime Video's "currentTerritory" sat at 174 KB of 520 KB), so
	// a half-read page looks exactly like a blocked one.
	url, done := server(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "5000")
		_, _ = io.WriteString(w, "<html>no territory marker in what arrived</html>")
	})
	got := runCheck(t, "prime_video", map[string]string{"www.primevideo.com": url})
	done()
	if got.Status != model.UnlockFailed {
		t.Errorf("truncated body: status %q, want %q", got.Status, model.UnlockFailed)
	}
}

func TestRedditStates(t *testing.T) {
	// 403 alone is not a verdict: only Reddit's own refusal copy is. A bare
	// 403 is the shape a non-browser client gets (measured: h2 got 403 and a
	// block page while h1 from the same IP got the site).
	for _, tc := range []struct {
		name   string
		code   int
		body   string
		want   string
		detail string
	}{
		{"ok", 200, "<html>Reddit</html>", model.UnlockYes, ""},
		{"blocked page", 403, `<div>You've been blocked by network security.</div>`, model.UnlockNo, ""},
		{"rate limit", 403, "Whoa there, pardner!", model.UnlockNo, ""},
		{"bare 403", 403, "nope", model.UnlockFailed, "HTTP 403"},
		{"server error", 500, "", model.UnlockFailed, "HTTP 500"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			url, done := server(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.code)
				_, _ = io.WriteString(w, tc.body)
			})
			got := runCheck(t, "reddit", map[string]string{"www.reddit.com": url})
			done()
			if got.Status != tc.want || got.Detail != tc.detail {
				t.Errorf("reddit %d/%q: got %q/%q, want %q/%q", tc.code, tc.body, got.Status, got.Detail, tc.want, tc.detail)
			}
		})
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
