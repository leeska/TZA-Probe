package browserworker

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"log/slog"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/leeska/TZA-ControlCenter/internal/ipguard"
)

//go:embed region.js
var regionBody string

// regionJS is the Python worker's in-page reader, byte for byte (region.js is
// its function body): visible labels → region codes via Intl.DisplayNames.
var regionJS = "(source) => {" + regionBody + "}"

// failure is a reason the Server knows; anything else is reported as browser_failed.
type failure string

func (f failure) Error() string { return string(f) }

const (
	errOriginNotAllowed failure = "origin_not_allowed"
	errSandbox          failure = "sandbox_unverified"
	errEgress           failure = "egress_unavailable"
	errEgressMismatch   failure = "egress_mismatch"
	errUnavailable      failure = "challenge_or_unavailable"
	errChallenge        failure = "challenge_detected"
	errDenied           failure = "http_denied"
	errForbidden        failure = "http_forbidden"
)

// pageFailures are the service-page outcomes that still finish the job (the
// egress is re-checked and the result reported as failed with that reason).
var pageFailures = map[failure]bool{errUnavailable: true, errChallenge: true, errDenied: true, errForbidden: true}

// Runner executes jobs. Isolation is checked once per job, as the Python worker did.
type Runner struct {
	Socket   string
	State    string
	Chromium string
	// ProcRoot is /proc; tests point it elsewhere.
	ProcRoot string
	// interfaces is overridable in tests.
	interfaces func() []Iface
}

func (r *Runner) ifaces() []Iface {
	if r.interfaces != nil {
		return r.interfaces()
	}
	return interfaces()
}

// guard blocks every request outside the allowlist (Playwright context.route)
// and remembers the denied origins for the evidence.
type guard struct {
	mu     sync.Mutex
	denied map[string]bool
}

func (g *guard) install(page *rod.Page, service string) *rod.HijackRouter {
	router := page.HijackRequests()
	router.MustAdd("*", func(h *rod.Hijack) {
		raw := h.Request.URL().String()
		if Permitted(service, raw) {
			h.ContinueRequest(&proto.FetchContinueRequest{})
			return
		}
		u := h.Request.URL()
		g.mu.Lock()
		origin := u.Scheme + "://" + u.Hostname()
		if len(origin) > 200 {
			origin = origin[:200]
		}
		g.denied[origin] = true
		g.mu.Unlock()
		h.Response.Fail(proto.NetworkErrorReasonBlockedByClient)
	})
	go router.Run()
	return router
}

func (g *guard) list() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := sortedKeys(g.denied)
	if len(out) > 32 {
		out = out[:32]
	}
	return out
}

// session is one job's browser and helpers.
type session struct {
	browser *Browser
	job     ipguard.Job
	guard   *guard
	geo     *proto.EmulationSetGeolocationOverride
}

func (s *session) page() (*rod.Page, *rod.HijackRouter, error) {
	page, err := s.browser.Page(s.geo)
	if err != nil {
		return nil, nil, err
	}
	return page, s.guard.install(page, s.job.Service), nil
}

func closePage(page *rod.Page, router *rod.HijackRouter) {
	if router != nil {
		router.Stop()
	}
	if page != nil {
		page.Close()
	}
}

func (s *session) egress() (string, error) {
	page, router, err := s.page()
	if err != nil {
		return "", err
	}
	defer closePage(page, router)
	response, err := Navigate(page, "https://api64.ipify.org", 30*time.Second)
	if err != nil || response == nil || response.Status != 200 {
		status := 0
		if response != nil {
			status = response.Status
		}
		slog.Warn("egress check failed", "job", s.job.ID, "error", err, "status", status)
		return "", errEgress
	}
	// The response body, as the Python worker read it; a text document's DOM
	// can still be empty at DOMContentLoaded.
	text, err := response.Body(page)
	if err != nil {
		slog.Warn("egress check failed", "job", s.job.ID, "error", err)
		return "", errEgress
	}
	address, err := netip.ParseAddr(strings.TrimSpace(text))
	if err != nil {
		if len(text) > 80 {
			text = text[:80]
		}
		slog.Warn("egress check failed", "job", s.job.ID, "error", err, "body", text)
		return "", errEgress
	}
	if address.String() != s.job.Policy.ExpectedIP {
		return "", errEgressMismatch
	}
	return address.String(), nil
}

// read polls a loaded page until the reading settles, it shows a challenge or
// the budget runs out — the worker's read_service_page.
func read(ctx context.Context, page *rod.Page, adapter, destination string, budget time.Duration) (Extraction, error) {
	source := map[string]string{"google_home": "rendered_footer", "google_terms": "rendered_terms_country", "meta_ads_library": "rendered_country_selector"}[adapter]
	deadline := time.Now().Add(budget)
	for {
		shot, err := snapshot(page)
		var region Region
		if err == nil {
			region, err = readRegion(page, source)
		}
		if err != nil {
			// The page is still navigating (context destroyed): retry within the budget.
			if time.Now().After(deadline) || ctx.Err() != nil {
				return Extraction{}, err
			}
			time.Sleep(500 * time.Millisecond)
			continue
		}
		out := Extract(adapter, shot.Body, shot.Markup, shot.Title, shot.URL, destination, region)
		if adapter == "meta_ads_library" {
			out.Recognized = out.Recognized && strings.TrimSpace(shot.Body) != ""
		}
		ready := out.Signal.Status == "observed"
		if adapter == "meta_ads_library" || (adapter == "google_home" && !out.Signal.NeutralEntry) {
			ready = out.Recognized
		}
		if ready || out.Challenge || time.Now().After(deadline) {
			return out, nil
		}
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func finalOrigin(page *rod.Page) string {
	u, err := url.Parse(pageURL(page))
	if err != nil {
		return ""
	}
	return "https://" + u.Host
}

func pageURL(page *rod.Page) string {
	var href string
	if evaluate(page, "location.href", &href) != nil {
		return ""
	}
	return href
}

func (s *session) terms(ctx context.Context) *ipguard.RegionObservation {
	result := &ipguard.RegionObservation{Page: ipguard.Page{Outcome: "unknown", Signal: "google_terms_country"}, Reason: "country_unavailable", Assessment: "unknown"}
	page, router, err := s.page()
	if err != nil {
		result.Reason = "browser_failed"
		return result
	}
	defer func() {
		closePage(page, router)
		proto.StorageClearCookies{}.Call(s.browser.Browser)
	}()
	destination := "https://policies.google.com/terms"
	response, err := Navigate(page, destination, 20*time.Second)
	if err != nil {
		result.Reason = "browser_failed"
		return result
	}
	if response == nil || response.Status != 200 {
		result.Reason = string(errDenied)
		if response != nil && response.Status == 403 {
			result.Reason = string(errForbidden)
		}
		return result
	}
	out, err := read(ctx, page, "google_terms", destination, 5*time.Second)
	if err != nil {
		slog.Warn("terms page failed", "job", s.job.ID, "error", err)
		result.Reason = "browser_failed"
		return result
	}
	if out.Signal.Status == "observed" {
		result.Page = ipguard.Page{Outcome: "normal", FinalOrigin: finalOrigin(page), Signal: "google_terms_country", Value: out.Signal.Value}
		result.Reason = ""
	}
	return result
}

func unavailable(response *Response) bool {
	return response == nil || response.Status == 429 || response.CFMitigated == "challenge"
}

// observe loads the service page and fills result.Page — observe_service.
func (s *session) observe(ctx context.Context, result *ipguard.Result) (*rod.Page, *rod.HijackRouter, error) {
	service, _ := ipguard.Lookup(s.job.Service)
	experiment := s.job.Phase == "experiment"
	destination := service.Observe
	if experiment && service.Experiment != "" {
		destination = strings.ReplaceAll(service.Experiment, "{country}", s.job.Policy.Country)
	}
	page, router, err := s.page()
	if err != nil {
		return nil, nil, err
	}
	response, err := Navigate(page, destination, 30*time.Second)
	if err != nil {
		return page, router, err
	}
	if experiment || service.Adapter != "meta_ads_library" {
		dwell := 2 * time.Second
		if experiment {
			dwell = time.Duration(s.job.Policy.DwellSeconds) * time.Second
		}
		select {
		case <-ctx.Done():
			return page, router, ctx.Err()
		case <-time.After(dwell):
		}
	}
	if unavailable(response) {
		return page, router, errUnavailable
	}
	result.Page.FinalOrigin = finalOrigin(page)
	var out Extraction
	if service.Adapter == "google_home" {
		out, err = read(ctx, page, "google_home", destination, 5*time.Second)
		if err == nil && !experiment && destination == service.Observe && response.Status < 400 && !out.Challenge && out.Signal.Status != "observed" {
			// One neutral search page when the homepage footer names no country.
			destination = "https://www.google.com/search?q=Google"
			if response, err = Navigate(page, destination, 30*time.Second); err == nil {
				out, err = read(ctx, page, "google_home", destination, 10*time.Second)
			}
		}
	} else {
		out, err = read(ctx, page, service.Adapter, destination, 18*time.Second)
	}
	if err != nil {
		return page, router, err
	}
	if unavailable(response) {
		return page, router, errUnavailable
	}
	if out.Challenge {
		return page, router, errChallenge
	}
	metric := ipguard.CountrySignal(s.job.Service)
	result.Page = ipguard.Page{Outcome: "unknown", FinalOrigin: finalOrigin(page), Signal: metric}
	if out.Recognized {
		result.Page.Outcome = "normal"
	}
	if out.Signal.Metric == metric && out.Signal.Status == "observed" {
		result.Page.Value = out.Signal.Value
	}
	if response.Status >= 400 && !(response.Status == 403 && service.Adapter == "meta_ads_library" && out.Recognized) {
		if response.Status == 403 {
			return page, router, errForbidden
		}
		return page, router, errDenied
	}
	return page, router, nil
}

// profileIdentity names the persistent experiment profile: one per node,
// service and exact policy, so a changed setting never reuses old cookies.
func profileIdentity(job ipguard.Job) string {
	raw, _ := json.Marshal(struct {
		Node    string         `json:"node"`
		Service string         `json:"service"`
		Policy  ipguard.Policy `json:"policy"`
	}{job.NodeID, job.Service, job.Policy})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Execute runs one job and always returns a result for the Server to judge.
func (r *Runner) Execute(ctx context.Context, job ipguard.Job) ipguard.Result {
	result := ipguard.Result{Execution: "failed", Reason: "worker_failed", Page: ipguard.Page{Outcome: "unknown"}, DeniedOrigins: []string{}, Assessment: "unknown"}
	result.NetworkIsolated = Isolated(r.ifaces())
	if !result.NetworkIsolated || os.Geteuid() == 0 {
		result.Reason = "worker_network_isolation_required"
		return result
	}
	experiment := job.Phase == "experiment"
	var profile string
	var err error
	if experiment {
		profile = filepath.Join(r.State, "profiles", profileIdentity(job))
		err = os.MkdirAll(profile, 0700)
	} else {
		profile, err = profileDir(r.State, "observe-")
	}
	if err != nil {
		result.Reason = "browser_failed"
		return result
	}
	if !experiment {
		defer os.RemoveAll(profile)
	}
	g := &guard{denied: map[string]bool{}}
	defer func() { result.DeniedOrigins = g.list() }()

	tunnel, err := OpenTunnel(r.Socket, job)
	if err != nil {
		result.Reason = "browser_failed"
		return result
	}
	defer tunnel.Close()
	browser, err := Start(ctx, Launch{Bin: r.Chromium, Profile: profile, ProxyAddr: tunnel.Addr, Locale: job.Policy.Locale, Timezone: job.Policy.Timezone})
	if err != nil {
		result.Reason = "browser_failed"
		return result
	}
	defer browser.Close()
	result.BrowserVersion = browser.Version
	s := &session{browser: browser, job: job, guard: g}

	// Position only in the experiment phase, granted only to the service's main site.
	proto.BrowserResetPermissions{}.Call(browser.Browser)
	if experiment && job.Policy.Position != nil {
		latitude, longitude, accuracy := job.Policy.Position.Latitude, job.Policy.Position.Longitude, 100.0
		s.geo = &proto.EmulationSetGeolocationOverride{Latitude: &latitude, Longitude: &longitude, Accuracy: &accuracy}
		origin := "https://www.google.com"
		if job.Service == "meta" {
			origin = "https://www.facebook.com"
		}
		if err := (proto.BrowserGrantPermissions{Permissions: []proto.BrowserPermissionType{proto.BrowserPermissionTypeGeolocation}, Origin: origin}).Call(browser.Browser); err != nil {
			result.Reason = "browser_failed"
			return result
		}
		result.GeolocationOverride = true
	}

	fail := func(err error) ipguard.Result {
		var known failure
		if errors.As(err, &known) {
			result.Reason = string(known)
		} else {
			result.Reason = "browser_failed"
			slog.Warn("browser job failed", "job", job.ID, "error", err)
		}
		return result
	}

	// Sandbox: a renderer must exist to be inspected.
	probe, err := browser.Page(nil)
	if err != nil {
		return fail(err)
	}
	if _, err = Navigate(probe, "data:text/html,<p>sandbox</p>", 10*time.Second); err != nil {
		probe.Close()
		return fail(err)
	}
	result.Sandbox, _ = SandboxEvidence(r.procRoot(), browser.PID())
	probe.Close()
	if !result.Sandbox {
		return fail(errSandbox)
	}

	if result.EgressBefore, err = s.egress(); err != nil {
		return fail(err)
	}
	if job.Service == "google" && !experiment {
		result.GoogleTerms = s.terms(ctx)
	}
	page, router, err := s.observe(ctx, &result)
	primary := ""
	var known failure
	if err != nil {
		if !errors.As(err, &known) || !pageFailures[known] {
			closePage(page, router)
			return fail(err)
		}
		primary = string(known)
	}
	if page != nil && !Permitted(job.Service, pageURL(page)) {
		closePage(page, router)
		return fail(errOriginNotAllowed)
	}
	closePage(page, router)
	if result.EgressAfter, err = s.egress(); err != nil {
		return fail(err)
	}
	result.Execution, result.Reason = "completed", primary
	if primary != "" {
		result.Execution = "failed"
	}
	return result
}

func (r *Runner) procRoot() string {
	if r.ProcRoot != "" {
		return r.ProcRoot
	}
	return "/proc"
}

func decodeJSON(raw []byte, out any) error {
	return json.Unmarshal(raw, out)
}
