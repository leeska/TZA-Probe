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
	Upgrader Upgrader
	Restart  func()
}

func (a Agent) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		var upgrading sync.Mutex
		for {
			m, err := a.Collector.Collect(ctx)
			var reply model.ReportReply
			if err == nil {
				if a.Upgrader != nil {
					m.Capabilities = append(m.Capabilities, model.CapabilitySelfUpdate)
				}
				reply, err = a.Transport.Report(ctx, m)
			}
			if err != nil && ctx.Err() == nil {
				slog.Warn("metrics report failed", "error", err)
			}
			// 下载十几 MB 不能卡住每 5 秒一次的上报；同一时间只跑一次升级。
			if reply.Upgrade != nil && a.Upgrader != nil && upgrading.TryLock() {
				wg.Add(1)
				go func(up model.AgentUpgrade) {
					defer wg.Done()
					defer upgrading.Unlock()
					a.upgrade(ctx, up)
				}(*reply.Upgrade)
			}
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
	endpoint, token string
	client          *http.Client
}

func NewTransport(endpoint, token string, allowHTTP bool) (*HTTPTransport, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") != "" {
		return nil, errors.New("endpoint must be an absolute server origin without credentials or path")
	}
	if len(token) != 64 {
		return nil, errors.New("agent token must have 64 characters")
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
		return fmt.Errorf("server returned HTTP %d", resp.StatusCode)
	}
	if out != nil && resp.StatusCode != http.StatusNoContent {
		return json.NewDecoder(io.LimitReader(resp.Body, 256<<10)).Decode(out)
	}
	return nil
}
func (t *HTTPTransport) Report(ctx context.Context, m model.Metrics) (model.ReportReply, error) {
	var reply model.ReportReply
	err := t.request(ctx, "POST", "/api/v1/agent/report", model.Report{Protocol: model.Protocol, Metrics: m}, &reply)
	return reply, err
}
func (t *HTTPTransport) Jobs(ctx context.Context) ([]model.Job, error) {
	var jobs []model.Job
	err := t.request(ctx, "GET", "/api/v1/agent/jobs", nil, &jobs)
	return jobs, err
}
func (t *HTTPTransport) Finish(ctx context.Context, r model.Result) error {
	return t.request(ctx, "POST", "/api/v1/agent/results", r, nil)
}
