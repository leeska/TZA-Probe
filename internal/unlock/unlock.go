// Package unlock runs the per-service egress checks a node performs against its
// own outbound IP: streaming, AI, exchanges and a handful of region lookups. It
// is linked into the Agent only; the Server never imports it.
//
// The package is standard-library only. It never shells out and never downloads
// an executable. Every check is an independent HTTPS request whose verdict is
// one of model's unlock words (yes / no / originals_only / banned / failed).
// A transport error or a timeout is always "failed" — never "no" or "banned",
// which are verdicts about the IP and must not be forged from a network fault.
package unlock

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/leeska/TZA-Probe/internal/model"
)

const (
	// DefaultTimeout is the per-service budget when Options.Timeout is unset.
	DefaultTimeout = 20 * time.Second
	// DefaultConcurrency is how many services run at once when unset.
	DefaultConcurrency = 8
	// DefaultUserAgent is the browser UA the streaming checks expect.
	DefaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36"

	// maxRegion / maxDetail mirror model.Result.Validate's caps.
	maxRegion = 40
	maxDetail = 80

	// maxBody caps how much of a response we read into memory; the largest page
	// any check looks at is Disney+'s ~2 MB home page.
	maxBody = 4 << 20
)

// Options configures one round of checks.
type Options struct {
	// Services is the catalogue ids to run; empty means all of them.
	Services []string
	// Family pins the egress family: "ipv4", "ipv6", or "" to follow the
	// system default (dual-stack hosts may pick either).
	Family string
	// Timeout is the per-service budget; zero uses DefaultTimeout.
	Timeout time.Duration
	// Concurrency is the number of services in flight; zero uses
	// DefaultConcurrency.
	Concurrency int
	// UserAgent overrides the default browser UA.
	UserAgent string

	// hosts is a test hook: it rewrites a request's scheme+host to an httptest
	// server address, keyed by the original host. Unexported so callers cannot
	// accidentally redirect production traffic.
	hosts map[string]string
}

// Client carries the shared HTTP plumbing for one round. Two clients share one
// Transport: follow follows redirects, direct returns the first response (the
// checks that read a Location header or a raw status code need that).
type Client struct {
	follow  *http.Client
	direct  *http.Client
	plain   *http.Client // HTTP/1.1 only: the hosts that refuse this client's h2
	ua      string
	timeout time.Duration
	family  string
	hosts   map[string]string
}

func clientFor(family, ua string, timeout time.Duration, hosts map[string]string) *Client {
	if ua == "" {
		ua = DefaultUserAgent
	}
	follow, direct, plain := newHTTPClients(family)
	return &Client{follow: follow, direct: direct, plain: plain, ua: ua, timeout: timeout, family: family, hosts: hosts}
}

// newHTTPClients builds the redirect-following and redirect-stopping clients
// over one shared Transport, plus the HTTP/1.1-only client one check needs (see
// doPlain). When family is ipv4/ipv6 the dialer refuses the other family at the
// connect syscall, so name resolution cannot leak a v6 connect on an ipv4-pinned
// round (and vice versa).
//
// HTTP/2 by default — browsers speak it and every check except Reddit was
// measured against the reference checkers on this transport. Reddit is the
// measured exception: www.reddit.com answered this client's h2 with a 403 and
// its block page while h1 got the site from the same IP in the same second (the
// other direction happens too: Cloudflare 403s this client's h1 for
// claude.ai), so the transport is picked per check off a measurement, not a
// guess.
func newHTTPClients(family string) (*http.Client, *http.Client, *http.Client) {
	dialer := &net.Dialer{
		Timeout:   15 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   familyControl(family),
	}
	newTransport := func(forceH2 bool) *http.Transport {
		return &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           dialer.DialContext,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   15 * time.Second,
			ExpectContinueTimeout: time.Second,
			ForceAttemptHTTP2:     forceH2,
			TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		}
	}
	transport := newTransport(true)
	follow := &http.Client{Transport: transport}
	direct := &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	plain := &http.Client{Transport: newTransport(false)}
	return follow, direct, plain
}

// familyControl implements the family pin. Go calls Control with the concrete
// network the connect uses ("tcp4" / "tcp6"), which is what makes a pin real
// rather than a hint to the resolver.
func familyControl(family string) func(network, address string, c syscall.RawConn) error {
	if family != "ipv4" && family != "ipv6" {
		return nil
	}
	return func(network, address string, _ syscall.RawConn) error {
		switch {
		case family == "ipv4" && network == "tcp6":
			return errors.New("unlock: ipv6 disabled for this round")
		case family == "ipv6" && network == "tcp4":
			return errors.New("unlock: ipv4 disabled for this round")
		}
		return nil
	}
}

// url rewrites a request URL onto its httptest stand-in when one is registered
// for the host. Outside tests (no overrides) it returns the URL untouched.
func (c *Client) url(raw string) string {
	if len(c.hosts) == 0 {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	base, ok := c.hosts[u.Host]
	if !ok {
		return raw
	}
	b, err := url.Parse(base)
	if err != nil {
		return raw
	}
	u.Scheme, u.Host = b.Scheme, b.Host
	return u.String()
}

// do issues one request. follow picks the redirect policy; hdr entries override
// the defaults (notably User-Agent for the SD Gundam check).
func (c *Client) do(ctx context.Context, follow bool, method, raw string, hdr map[string]string, body io.Reader) (*http.Response, error) {
	cl := c.direct
	if follow {
		cl = c.follow
	}
	return c.doOn(ctx, cl, method, raw, hdr, body)
}

// doPlain issues one request over the HTTP/1.1-only client, for the hosts that
// refuse the same client's HTTP/2 (www.reddit.com answered h2 with a 403 and
// its block page, h1 with the site, same IP, same second).
func (c *Client) doPlain(ctx context.Context, method, raw string, hdr map[string]string, body io.Reader) (*http.Response, error) {
	return c.doOn(ctx, c.plain, method, raw, hdr, body)
}

// doOn is the request path do and doPlain share.
func (c *Client) doOn(ctx context.Context, cl *http.Client, method, raw string, hdr map[string]string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.url(raw), body)
	if err != nil {
		return nil, err
	}
	if c.ua != "" {
		req.Header.Set("User-Agent", c.ua)
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "*/*")
	}
	// Browsers always send a language; without one the CDNs served us their
	// en-US defaults (a Hong Kong egress was told youtube GL=US and Play
	// "United States"), so every request carries one unless a check overrides.
	if req.Header.Get("Accept-Language") == "" {
		req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return c.send(ctx, req, cl)
}

// send issues a request, retrying the transport faults that are the network
// rather than the IP's verdict (a reset or an EOF mid-handshake). Real timeouts
// are not retried: the round's budget is what ran out, and another try would
// only spend more of it.
func (c *Client) send(ctx context.Context, req *http.Request, cl *http.Client) (*http.Response, error) {
	resp, err := cl.Do(req)
	for attempt := 1; err != nil && attempt <= 2 && ctx.Err() == nil && retryable(err); attempt++ {
		if req.GetBody == nil {
			return resp, err
		}
		body, gerr := req.GetBody()
		if gerr != nil {
			return resp, err
		}
		next, nerr := http.NewRequestWithContext(ctx, req.Method, req.URL.String(), body)
		if nerr != nil {
			return resp, err
		}
		next.Header = req.Header.Clone()
		req = next
		select {
		case <-ctx.Done():
			return resp, err
		case <-time.After(time.Duration(attempt) * 400 * time.Millisecond):
		}
		resp, err = cl.Do(req)
	}
	return resp, err
}

// retryable reports whether a transport error is worth one more attempt.
func retryable(err error) bool {
	if err == nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return false
	}
	return true
}

// read reads the whole body and reports a failure to read it. A half-read body
// must never be judged as if the markers it never reached were absent: Prime
// Video's "currentTerritory" sat 174 KB into a 520 KB document, so a connection
// cut short looks exactly like a blocked page. maxBody caps the read; hitting
// the cap is not an error.
func read(resp *http.Response) (string, error) {
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	return string(b), err
}

// drain closes a response we do not care to read.
func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
}

// res builds a verdict sans service id (runOne fills that in).
func res(status, region, detail string) model.UnlockResult {
	return model.UnlockResult{Status: status, Region: region, Detail: detail}
}

// netErr turns a transport fault into a "failed" verdict. It is deliberately
// not allowed to become no/banned: those describe the IP, this describes the
// check.
func netErr(err error) model.UnlockResult {
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return res(model.UnlockFailed, "", "timeout")
	case errors.Is(err, context.Canceled):
		return res(model.UnlockFailed, "", "canceled")
	default:
		return res(model.UnlockFailed, "", err.Error())
	}
}

// truncate cuts s to at most n bytes without splitting a UTF-8 rune.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// quotedAfter returns the text between marker and the next double quote, or ""
// when the marker is absent. Used for the many `"key":"XX"` style probes.
func quotedAfter(s, marker string) string {
	i := strings.Index(s, marker)
	if i < 0 {
		return ""
	}
	rest := s[i+len(marker):]
	j := strings.IndexByte(rest, '"')
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// boolAfter returns "true" or "false" for an unquoted `"key":true` literal, ""
// when the key is absent or does not carry a boolean.
func boolAfter(s, marker string) string {
	i := strings.Index(s, marker)
	if i < 0 {
		return ""
	}
	rest := strings.TrimLeft(s[i+len(marker):], " ")
	switch {
	case strings.HasPrefix(rest, "true"):
		return "true"
	case strings.HasPrefix(rest, "false"):
		return "false"
	}
	return ""
}

// okStatus reports the statuses Netflix treats as "region page served".
func okStatus(code int) bool { return code == http.StatusOK || code == http.StatusMovedPermanently }

// Run executes one round: every requested service once, in catalogue order.
// It always returns len(services) results; a service that fails carries
// model.UnlockFailed plus a short reason. The whole round can be interrupted
// through ctx, and each service additionally has its own budget.
func Run(ctx context.Context, opts Options) []model.UnlockResult {
	ids := opts.Services
	if len(ids) == 0 {
		ids = model.UnlockServiceIDs()
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	workers := opts.Concurrency
	if workers <= 0 {
		workers = DefaultConcurrency
	}
	if workers > len(ids) {
		workers = len(ids)
	}
	if workers < 1 {
		workers = 1
	}
	client := clientFor(opts.Family, opts.UserAgent, timeout, opts.hosts)

	out := make([]model.UnlockResult, len(ids))
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				out[i] = res(model.UnlockFailed, "", "canceled")
				out[i].Service = id
				return
			}
			defer func() { <-sem }()
			out[i] = runOne(ctx, client, id, timeout)
		}(i, id)
	}
	wg.Wait()
	return out
}

// runOne runs a single service under its own context and normalises the result
// to the wire contract.
func runOne(ctx context.Context, c *Client, id string, timeout time.Duration) model.UnlockResult {
	fn, ok := checks[id]
	if !ok {
		return model.UnlockResult{Service: id, Status: model.UnlockFailed, Detail: "no such check"}
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	r := fn(rctx, c)
	r.Service = id
	if !model.ValidUnlockStatus(r.Status) {
		return model.UnlockResult{Service: id, Status: model.UnlockFailed, Detail: "invalid verdict"}
	}
	r.Region = truncate(r.Region, maxRegion)
	r.Detail = truncate(r.Detail, maxDetail)
	return r
}
