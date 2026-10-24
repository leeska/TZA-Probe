// Package agent orchestrates injected collector, transport and probe adapters.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

type Collector interface {
	Collect(context.Context) (model.Metrics, error)
}
type Prober interface {
	Run(context.Context, model.Job) model.Result
}
type Transport interface {
	Report(context.Context, model.Metrics) (model.ReportReply, error)
	Jobs(context.Context) ([]model.Job, error)
	Finish(context.Context, model.Result) error
}

// Upgrader applies an upgrade the Server offered (see Updater).
type Upgrader interface {
	Update(context.Context, model.AgentUpgrade) error
}
type Agent struct {
	Collector Collector
	Prober    Prober
	Transport Transport
	// Upgrader is set when the Agent runs under a service unit that restarts it
	// (--self-update); nil leaves upgrades to the install command. Restart is
	// called once a new binary is in place and must make Run return.
	Upgrader          Upgrader
	Restart           func()
	reportInterval    time.Duration
	heartbeatInterval time.Duration
	collectionTimeout time.Duration
}

func (a Agent) Run(ctx context.Context) {
	if a.reportInterval <= 0 {
		a.reportInterval = 5 * time.Second
	}
	if a.heartbeatInterval <= 0 {
		a.heartbeatInterval = 5 * time.Second
	}
	if a.collectionTimeout <= 0 {
		a.collectionTimeout = 10 * time.Second
	}
	var wg sync.WaitGroup
	var upgrading sync.Mutex
	health := initialHealth(a.Upgrader)
	handleUpgrade := func(reply model.ReportReply) {
		if reply.Upgrade != nil && a.Upgrader != nil && upgrading.TryLock() {
			wg.Add(1)
			go func(up model.AgentUpgrade) { defer wg.Done(); defer upgrading.Unlock(); a.upgrade(ctx, up) }(*reply.Upgrade)
		}
	}
	wg.Add(2)
	if transport, ok := a.Transport.(HeartbeatTransport); ok {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(a.heartbeatInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					status, skip := health.snapshot(a.collectionTimeout)
					if skip {
						continue
					}
					reply, err := transport.Heartbeat(ctx, status)
					if err != nil && ctx.Err() == nil {
						slog.Warn("agent heartbeat failed", "error", err)
					}
					handleUpgrade(reply)
				}
			}
		}()
	}
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(a.reportInterval)
		defer ticker.Stop()
		for {
			health.begin()
			collectCtx, cancel := context.WithTimeout(ctx, a.collectionTimeout)
			m, err := a.Collector.Collect(collectCtx)
			cancel()
			if a.Upgrader != nil {
				m.Capabilities = append(append([]string{}, m.Capabilities...), model.CapabilitySelfUpdate)
			}
			health.collected(m, err)
			var reply model.ReportReply
			if err == nil {
				health.prepareReport(&m)
				reply, err = a.Transport.Report(ctx, m)
				health.reported(err)
				if err != nil && ctx.Err() == nil {
					slog.Warn("metrics report failed", "error", err)
				}
			} else if ctx.Err() == nil {
				slog.Warn("metrics collection failed", "error", err)
			}
			// 下载十几 MB 不能卡住每 5 秒一次的上报；同一时间只跑一次升级。
			handleUpgrade(reply)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	go func() {
		defer wg.Done()
		// Every leased job runs on its own and the loop keeps polling meanwhile:
		// a minutes-long unlock round or a minute-long trace must not stall the
		// latency probes. The Server bounds what is in flight (one lease per kind
		// at a time), so the Agent runs whatever it is handed.
		var workers sync.WaitGroup
		defer workers.Wait()
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		for {
			jobs, err := a.Transport.Jobs(ctx)
			if err != nil && ctx.Err() == nil {
				slog.Warn("job polling failed", "error", err)
			}
			for _, j := range jobs {
				workers.Add(1)
				go func(job model.Job) {
					defer workers.Done()
					a.runJob(ctx, job)
				}(j)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	wg.Wait()
}

func (a Agent) upgrade(ctx context.Context, up model.AgentUpgrade) {
	slog.Info("upgrade requested by the Server", "version", up.Version)
	err := a.Upgrader.Update(ctx, up)
	switch {
	case errors.Is(err, ErrAlreadyCurrent):
		slog.Info("agent binary already current, nothing to upgrade", "version", up.Version)
	case err != nil:
		if ctx.Err() == nil {
			slog.Warn("upgrade failed, keeping the running version", "error", err)
		}
	default:
		slog.Info("upgrade ready, restarting", "version", up.Version)
		if a.Restart != nil {
			a.Restart()
		}
	}
}

// pollInterval is how often the Agent asks for jobs.
var pollInterval = 10 * time.Second

// runJob executes one job and uploads its result, retrying the upload up to
// three times.
func (a Agent) runJob(ctx context.Context, job model.Job) {
	result := a.Prober.Run(ctx, job)
	for attempt := 0; attempt < 3; attempt++ {
		if err := a.Transport.Finish(ctx, result); err == nil {
			return
		}
		timer := time.NewTimer(time.Duration(attempt+1) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
	if ctx.Err() == nil {
		slog.Warn("result upload failed", "job", job.ID)
	}
}

type HTTPTransport struct {
	endpoint, token      string
	client               *http.Client
	heartbeatUnsupported atomic.Bool
}

func NewTransport(endpoint, token string, allowHTTP bool) (*HTTPTransport, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") != "" {
		return nil, errors.New("endpoint must be an absolute server origin without credentials or path")
	}
	if len(token) != 64 {
		return nil, errors.New("agent token must be 64 bytes long")
	}
	ip := net.ParseIP(u.Hostname())
	loopback := u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
	if u.Scheme != "https" && !(u.Scheme == "http" && (allowHTTP || loopback)) {
		return nil, errors.New("HTTPS required; allow_http is only for a trusted private network")
	}
	return &HTTPTransport{endpoint: strings.TrimSuffix(endpoint, "/"), token: token, client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (t *HTTPTransport) request(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, t.endpoint+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+t.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		// The cause (DNS, TLS, refused, timeout) is what an operator needs; the
		// error names the endpoint and path only, the token rides in a header.
		return fmt.Errorf("server connection failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		failure := &HTTPStatusError{Status: resp.StatusCode}
		if resp.StatusCode == http.StatusConflict {
			var challenge model.AgentSessionChallenge
			if json.NewDecoder(io.LimitReader(resp.Body, 1024)).Decode(&challenge) == nil {
				if len(challenge.Challenge) == 64 && strings.Trim(challenge.Challenge, "0123456789abcdef") == "" {
					failure.SessionChallenge = challenge.Challenge
				}
			}
		}
		return failure
	}
	if out != nil && resp.StatusCode != http.StatusNoContent {
		return json.NewDecoder(io.LimitReader(resp.Body, 256<<10)).Decode(out)
	}
	return nil
}

type HTTPStatusError struct {
	Status           int
	SessionChallenge string
}

func (e *HTTPStatusError) Error() string { return fmt.Sprintf("server returned HTTP %d", e.Status) }

func (t *HTTPTransport) Heartbeat(ctx context.Context, status model.AgentStatus) (model.ReportReply, error) {
	if t.heartbeatUnsupported.Load() {
		return model.ReportReply{}, nil
	}
	var reply model.ReportReply
	body := model.Heartbeat{Protocol: model.Protocol, Status: status}
	err := t.stateRequest(ctx, "/api/v1/agent/heartbeat", &body, &body.Status.AgentState, &reply)
	var httpErr *HTTPStatusError
	if errors.As(err, &httpErr) && (httpErr.Status == http.StatusNotFound || httpErr.Status == http.StatusMethodNotAllowed) {
		t.heartbeatUnsupported.Store(true)
		return model.ReportReply{}, nil
	}
	return reply, err
}
func (t *HTTPTransport) Report(ctx context.Context, m model.Metrics) (model.ReportReply, error) {
	var reply model.ReportReply
	body := model.Report{Protocol: model.Protocol, Metrics: m}
	err := t.stateRequest(ctx, "/api/v1/agent/report", &body, &body.Metrics.AgentState, &reply)
	return reply, err
}

// Retry only explicit session challenges, with a bound on competing owners.
// Ordinary conflicts (rate limiting), auth failures and transport errors retain
// their existing behavior. The proof lives only in this request's copy.
func (t *HTTPTransport) stateRequest(ctx context.Context, path string, body any, state *model.AgentState, out *model.ReportReply) error {
	for attempt := 0; ; attempt++ {
		err := t.request(ctx, "POST", path, body, out)
		var status *HTTPStatusError
		if attempt >= 2 || state.Session == "" || !errors.As(err, &status) || status.SessionChallenge == "" {
			return err
		}
		state.Proof = status.SessionChallenge
	}
}

func (t *HTTPTransport) Jobs(ctx context.Context) ([]model.Job, error) {
	var jobs []model.Job
	err := t.request(ctx, "GET", "/api/v1/agent/jobs", nil, &jobs)
	return jobs, err
}
func (t *HTTPTransport) Finish(ctx context.Context, r model.Result) error {
	return t.request(ctx, "POST", "/api/v1/agent/results", r, nil)
}
