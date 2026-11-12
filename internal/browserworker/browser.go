package browserworker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// Launch describes one Chromium process. ProxyAddr is the worker's loopback
// CONNECT proxy; empty only for the self-test, which never loads a remote page.
type Launch struct {
	Bin       string
	Profile   string
	ProxyAddr string
	Locale    string
	Timezone  string
}

// Args are Chromium's command-line flags. The sandbox is never disabled: the
// result is refused if it does not hold (see sandboxEvidence), and Args asserts
// that no flag turns it off. Compared with the Python worker this adds: no local
// DNS at all (proxied names are resolved by the Agent), no background services
// that phone home, and the popup blocker left on.
func (l Launch) Args() []string {
	args := []string{
		"--headless",
		"--remote-debugging-port=0",
		"--remote-debugging-address=127.0.0.1",
		"--user-data-dir=" + l.Profile,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-background-networking",
		"--disable-component-update",
		"--disable-domain-reliability",
		"--disable-sync",
		"--disable-breakpad",
		"--disable-client-side-phishing-detection",
		"--disable-default-apps",
		"--disable-extensions",
		"--disable-dev-shm-usage",
		"--metrics-recording-only",
		"--password-store=basic",
		"--use-mock-keychain",
		"--disable-features=DnsOverHttps,OptimizationHints,MediaRouter,AutofillServerCommunication,Translate,InterestFeedContentSuggestions",
		"--disable-quic",
		"--force-webrtc-ip-handling-policy=disable_non_proxied_udp",
		"--webrtc-ip-handling-policy=disable_non_proxied_udp",
		"--host-resolver-rules=MAP * ~NOTFOUND , EXCLUDE 127.0.0.1",
	}
	if l.ProxyAddr != "" {
		args = append(args, "--proxy-server=http://"+l.ProxyAddr, "--proxy-bypass-list=<-loopback>")
	}
	if l.Locale != "" {
		args = append(args, "--lang="+l.Locale, "--accept-lang="+l.Locale)
	}
	return append(args, "about:blank")
}

var devtools = regexp.MustCompile(`DevTools listening on (ws://\S+)`)

// Browser is a running Chromium in its own process group.
type Browser struct {
	*rod.Browser
	cmd     *exec.Cmd
	launch  Launch
	Version string
}

// Start launches Chromium and connects to it. Cancelling ctx (deadline or the
// Server cancelling the job) aborts every CDP call; Close kills the process group.
func Start(ctx context.Context, l Launch) (*Browser, error) {
	args := l.Args()
	for _, arg := range args {
		if strings.Contains(arg, "no-sandbox") || strings.Contains(arg, "disable-setuid-sandbox") || strings.Contains(arg, "single-process") || strings.Contains(arg, "no-zygote") {
			return nil, fmt.Errorf("refusing to start Chromium with %s", arg)
		}
	}
	cmd := exec.Command(l.Bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	env := []string{"HOME=" + l.Profile, "PATH=/usr/bin:/bin"}
	if l.Timezone != "" {
		env = append(env, "TZ="+l.Timezone)
	}
	if l.Locale != "" {
		env = append(env, "LANG="+strings.ReplaceAll(l.Locale, "-", "_")+".UTF-8")
	}
	cmd.Env = env
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	b := &Browser{cmd: cmd, launch: l}
	found := make(chan string, 1)
	var (
		tailMu sync.Mutex
		tail   []string // Chromium's last stderr lines, for a launch failure
	)
	go func() {
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 64<<10), 64<<10)
		sent := false
		for scanner.Scan() {
			line := scanner.Text()
			if match := devtools.FindStringSubmatch(line); match != nil && !sent {
				found <- match[1]
				sent = true
				continue
			}
			if !sent {
				tailMu.Lock()
				tail = append(tail, line)
				if len(tail) > 4 {
					tail = tail[1:]
				}
				tailMu.Unlock()
			}
		}
		io.Copy(io.Discard, stderr)
		if !sent {
			close(found)
		}
	}()
	var endpoint string
	select {
	case endpoint = <-found:
	case <-time.After(30 * time.Second):
	case <-ctx.Done():
	}
	if endpoint == "" {
		b.Close()
		tailMu.Lock()
		detail := strings.TrimSpace(strings.Join(tail, " | "))
		tailMu.Unlock()
		if len(detail) > 600 {
			detail = detail[:600]
		}
		if detail != "" {
			return nil, errors.New("chromium did not start: " + detail)
		}
		return nil, errors.New("chromium did not start")
	}
	b.Browser = rod.New().ControlURL(endpoint).Context(ctx)
	if err = b.Browser.Connect(); err != nil {
		b.Close()
		return nil, err
	}
	version, err := (proto.BrowserGetVersion{}).Call(b.Browser)
	if err != nil {
		b.Close()
		return nil, err
	}
	b.Version = version.Product
	return b, nil
}

// PID is the Chromium browser process, the root of every renderer.
func (b *Browser) PID() int {
	if b.cmd == nil || b.cmd.Process == nil {
		return 0
	}
	return b.cmd.Process.Pid
}

// Close ends the browser and everything it started.
func (b *Browser) Close() {
	if b.Browser != nil {
		done := make(chan struct{})
		go func() { b.Browser.Close(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	}
	if pid := b.PID(); pid > 0 {
		syscall.Kill(-pid, syscall.SIGKILL)
		for _, child := range descendants(pid) {
			syscall.Kill(child, syscall.SIGKILL)
		}
		b.cmd.Wait()
	}
}

// pageScript runs before any page script in every document of a page: service
// workers can never register (Playwright's service_workers="block") and pages
// cannot open WebSockets (the Python worker closed every one).
const pageScript = `(() => {
  if (navigator.serviceWorker) Object.defineProperty(navigator.serviceWorker, "register", { value: () => Promise.reject(new DOMException("blocked", "SecurityError")) });
  const Blocked = function () { throw new DOMException("blocked", "SecurityError"); };
  Object.defineProperty(window, "WebSocket", { value: Blocked });
})()`

// Page opens a tab with the job's locale, time zone and (only in the
// experiment phase) the selected position, before anything is loaded.
func (b *Browser) Page(geolocation *proto.EmulationSetGeolocationOverride) (*rod.Page, error) {
	page, err := b.Browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return nil, err
	}
	steps := []func() error{
		func() error { return proto.NetworkEnable{}.Call(page) },
		func() error { return proto.NetworkSetBypassServiceWorker{Bypass: true}.Call(page) },
		func() error {
			return proto.NetworkSetBlockedURLs{Urls: []string{"ws://*", "wss://*"}}.Call(page)
		},
		func() error { _, err := page.EvalOnNewDocument(pageScript); return err },
	}
	if b.launch.Locale != "" {
		steps = append(steps,
			func() error { return proto.EmulationSetLocaleOverride{Locale: b.launch.Locale}.Call(page) },
			func() error {
				return proto.NetworkSetUserAgentOverride{UserAgent: b.userAgent(), AcceptLanguage: b.launch.Locale}.Call(page)
			})
	}
	if b.launch.Timezone != "" {
		steps = append(steps, func() error { return proto.EmulationSetTimezoneOverride{TimezoneID: b.launch.Timezone}.Call(page) })
	}
	if geolocation != nil {
		steps = append(steps, func() error { return geolocation.Call(page) })
	}
	for _, step := range steps {
		if err := step(); err != nil {
			page.Close()
			return nil, err
		}
	}
	return page, nil
}

// userAgent drops the "HeadlessChrome" token headless-shell advertises; the
// Python worker's Playwright did the same, and Google answers it differently.
func (b *Browser) userAgent() string {
	version, err := (proto.BrowserGetVersion{}).Call(b.Browser)
	if err != nil {
		return ""
	}
	return strings.Replace(version.UserAgent, "HeadlessChrome/", "Chrome/", 1)
}

// Response is the main document's answer to a navigation.
type Response struct {
	Status      int
	CFMitigated string
	RequestID   proto.NetworkRequestID
}

// Body is the main document's response body (Playwright's response.text()).
func (r *Response) Body(page *rod.Page) (string, error) {
	if r == nil {
		return "", errors.New("no response")
	}
	body, err := (proto.NetworkGetResponseBody{RequestID: r.RequestID}).Call(page)
	if err != nil {
		return "", err
	}
	if body.Base64Encoded {
		return "", errors.New("binary response")
	}
	return body.Body, nil
}

// Navigate loads target and waits for DOMContentLoaded (Playwright's
// wait_until="domcontentloaded"), returning the main document's response.
func Navigate(page *rod.Page, target string, timeout time.Duration) (*Response, error) {
	// Events are matched to this navigation by its loader id: a new tab's own
	// about:blank can finish after the tab is handed over, and its
	// DOMContentLoaded must not be taken for the page being loaded here.
	// A page may also replace itself before DOMContentLoaded (Google's search
	// page re-navigates with &sei=… and lands on /sorry/): any document that
	// commits after ours belongs to this navigation, and the first of them to
	// reach DOMContentLoaded ends it. The response stays the one we asked for.
	var (
		mu        sync.Mutex
		responses = map[proto.NetworkLoaderID]*Response{}
		loaded    = map[proto.NetworkLoaderID]bool{}
		commits   []proto.NetworkLoaderID // main-frame documents, in commit order
		changed   = make(chan struct{}, 1)
	)
	notify := func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	}
	frame := page.FrameID
	page = page.Timeout(timeout)
	defer page.CancelTimeout()
	if err := (proto.PageEnable{}).Call(page); err != nil {
		return nil, err
	}
	if err := (proto.PageSetLifecycleEventsEnabled{Enabled: true}).Call(page); err != nil {
		return nil, err
	}
	go page.EachEvent(func(e *proto.NetworkResponseReceived) {
		if e.Type == proto.NetworkResourceTypeDocument && e.FrameID == frame {
			mu.Lock()
			responses[e.LoaderID] = &Response{Status: e.Response.Status, CFMitigated: header(e.Response.Headers, "cf-mitigated"), RequestID: e.RequestID}
			mu.Unlock()
		}
	}, func(e *proto.PageLifecycleEvent) {
		if e.FrameID != frame {
			return
		}
		mu.Lock()
		switch e.Name {
		case "init":
			commits = append(commits, e.LoaderID)
		case "DOMContentLoaded":
			loaded[e.LoaderID] = true
		}
		mu.Unlock()
		notify()
	})()
	result, err := (proto.PageNavigate{URL: target}).Call(page)
	if err != nil {
		return nil, err
	}
	if result.ErrorText != "" {
		// The guard blocked the main document (an unapproved origin or redirect).
		if strings.Contains(result.ErrorText, "ERR_BLOCKED_BY_CLIENT") {
			return nil, errOriginNotAllowed
		}
		// ERR_TUNNEL_CONNECTION_FAILED and the like: no document arrives.
		return nil, errors.New("navigation failed: " + result.ErrorText)
	}
	for {
		mu.Lock()
		response, done, ours := responses[result.LoaderID], false, false
		for _, loader := range commits {
			ours = ours || loader == result.LoaderID
			if ours && loaded[loader] {
				done = true
			}
		}
		done = done || loaded[result.LoaderID]
		mu.Unlock()
		if done {
			return response, nil
		}
		select {
		case <-changed:
		case <-page.GetContext().Done():
			return nil, errors.New("navigation timed out: " + target)
		}
	}
}

func header(headers proto.NetworkHeaders, name string) string {
	for key, value := range headers {
		if strings.EqualFold(key, name) {
			return value.Str()
		}
	}
	return ""
}

// Snapshot is what the extraction reads from a rendered page.
type Snapshot struct {
	URL, Title, Markup, Body string
}

// evaluate runs expression in the main frame's current document. It bypasses
// rod's cached execution context on purpose: pages here navigate themselves
// (redirects, Google's search page), and a read must never see the document
// that was there before.
func evaluate(page *rod.Page, expression string, out any) error {
	result, err := (proto.RuntimeEvaluate{Expression: expression, ReturnByValue: true, AwaitPromise: true}).Call(page)
	if err != nil {
		return err
	}
	if result.ExceptionDetails != nil {
		return errors.New("page script failed: " + result.ExceptionDetails.Text)
	}
	return result.Result.Value.Unmarshal(out)
}

// snapshotJS is Playwright's page.url / title() / content() / body inner_text().
const snapshotJS = `(() => {
  let markup = document.doctype ? new XMLSerializer().serializeToString(document.doctype) : "";
  if (document.documentElement) markup += document.documentElement.outerHTML;
  return { url: location.href, title: document.title, markup, body: document.body ? document.body.innerText : "" };
})()`

func snapshot(page *rod.Page) (Snapshot, error) {
	var raw struct {
		URL    string `json:"url"`
		Title  string `json:"title"`
		Markup string `json:"markup"`
		Body   string `json:"body"`
	}
	if err := evaluate(page, snapshotJS, &raw); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{URL: raw.URL, Title: raw.Title, Markup: raw.Markup, Body: raw.Body}, nil
}

func readRegion(page *rod.Page, source string) (Region, error) {
	var region Region
	quoted, _ := json.Marshal(source)
	err := evaluate(page, "("+regionJS+")("+string(quoted)+")", &region)
	return region, err
}

// profileDir is a private directory for one browser profile.
func profileDir(parent, prefix string) (string, error) {
	if err := os.MkdirAll(parent, 0700); err != nil {
		return "", err
	}
	return os.MkdirTemp(parent, prefix)
}
